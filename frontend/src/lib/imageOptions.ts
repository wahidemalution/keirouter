// Options, prices and wording for the portal "Buat Gambar" page. The option
// lists match what the server accepts per image family.

import { formatImagePrice } from "../components/chains/chainUtils.ts";

export interface FamilyOptions {
  sizes?: string[];
  aspectRatios?: string[];
  resolutions?: string[];
}

const FAMILY_OPTIONS: Record<string, FamilyOptions> = {
  "gpt-image": { sizes: ["1024x1024", "1536x1024", "1024x1536", "auto"] },
  "grok-imagine": {
    aspectRatios: ["1:1", "16:9", "9:16", "4:3", "3:4", "3:2", "2:3", "21:9"],
    resolutions: ["1k", "2k"],
  },
  "gemini-image": { aspectRatios: ["1:1", "16:9", "9:16", "4:3", "3:4"] },
};

export function optionsForFamily(family: string): FamilyOptions {
  return FAMILY_OPTIONS[family] ?? {};
}

export const FAMILY_LABELS: Record<string, string> = {
  "gpt-image": "GPT Image",
  "grok-imagine": "Grok Imagine",
  "gemini-image": "Gemini Image",
};

/**
 * Resolutions a model can make. grok-imagine offers 2K only when the server
 * sent a 2K price; without one a 2K image cannot be billed.
 */
export function resolutionsFor(model: { family: string; price_2k_micro?: number }): string[] | undefined {
  const all = optionsForFamily(model.family).resolutions;
  if (!all) return undefined;
  return model.price_2k_micro ? all : all.filter((r) => r !== "2k");
}

/**
 * The per-image price the page shows and sends as expected_price_micro: the
 * 2K price for a 2K image, else the standard price. 0 when 2K has no price.
 */
export function unitPriceMicro(priceMicro: number, price2kMicro: number, resolution: string): number {
  return resolution.toLowerCase() === "2k" ? price2kMicro : priceMicro;
}

/** Price of n images at unitPriceMicro. */
export function totalPriceMicro(priceMicro: number, price2kMicro: number, resolution: string, n: number): number {
  return unitPriceMicro(priceMicro, price2kMicro, resolution) * n;
}

/** Micro-USD as "$0,0084", formatted like the admin chain editor's prices. */
export function formatImagePriceMicro(micro: number): string {
  return formatImagePrice(micro / 1_000_000);
}

/** Time left before a finished job's images are removed. */
export function deletesIn(expiresAt: string | null | undefined, now: number = Date.now()): string {
  if (!expiresAt) return "";
  const ms = Date.parse(expiresAt) - now;
  if (!Number.isFinite(ms) || ms <= 0) return "segera dihapus";
  const hours = Math.floor(ms / 3_600_000);
  if (hours >= 1) return `dihapus dalam ${hours} jam`;
  return `dihapus dalam ${Math.max(1, Math.floor(ms / 60_000))} menit`;
}

const ALT_MAX = 120;

export function imageAlt(prompt: string): string {
  const p = prompt.trim().replace(/\s+/g, " ");
  return p.length > ALT_MAX ? `${p.slice(0, ALT_MAX).trimEnd()}…` : p;
}

const ERROR_TEXT: Record<string, string> = {
  empty_prompt: "Tulis prompt terlebih dahulu.",
  prompt_too_long: "Prompt terlalu panjang. Maksimal 4000 karakter.",
  invalid_n: "Jumlah gambar harus 1 sampai 4.",
  invalid_size: "Ukuran ini tidak didukung model yang dipilih.",
  invalid_resolution: "Resolusi ini tidak didukung model yang dipilih.",
  invalid_aspect_ratio: "Rasio aspek ini tidak didukung model yang dipilih.",
  chain_required: "Pilih model terlebih dahulu.",
  insufficient_balance: "Saldo PAYG tidak cukup untuk membuat gambar.",
  key_disabled: "API key Anda sedang dinonaktifkan. Hubungi admin.",
  model_not_available: "Model ini tidak lagi tersedia untuk akun Anda. Pilih model lain.",
  too_many_active: "Masih ada 2 job yang berjalan. Tunggu salah satunya selesai, lalu coba lagi.",
  queue_full: "Antrean sedang penuh. Coba lagi dalam beberapa menit.",
  image_generation_unavailable: "Pembuatan gambar sedang tidak tersedia. Coba lagi nanti.",
  job_active: "Job ini masih berjalan dan belum bisa dihapus.",
  request_too_large: "Permintaan terlalu besar. Persingkat prompt Anda.",
  price_changed: "Harga berubah, silakan cek lagi lalu buat ulang.",
};

/** User-facing sentence for an error code from the image endpoints. */
export function imageErrorText(code: string | undefined, fallback: string): string {
  return (code && ERROR_TEXT[code]) || fallback;
}
