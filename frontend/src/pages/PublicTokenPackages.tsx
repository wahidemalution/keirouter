// Token AI Frontier landing at `/token-pack`. Reads the public package list
// (/portal/token/packages): tokens packages as cards, the days package as an
// add-on, and the models a token key may call. Featured packages carry a
// "Rekomendasi" label. Buying happens in the portal;
// while checkout is closed the cards show without a buy link.

import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ArrowRight, CalendarPlus, Cpu, Send } from "lucide-react";
import { PublicLayout, TELEGRAM_URL } from "../components/PublicLayout";

const FOCUS = "focus:outline-none focus-visible:ring-2 focus-visible:ring-[var(--green)] focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--bg)]";
const LINK = "text-[var(--ink)] underline underline-offset-2 hover:opacity-80";
const TERMS_URL = "/ketentuan#paket-token";
const BUY_URL = "/portal/token";

// Shape of GET /portal/token/packages (token_portal.go). Kept here because
// the public pages stay off lib/api.ts.
interface TokenOffer {
  id: string;
  name: string;
  kind: "tokens" | "days";
  tokens: number;
  days: number;
  price_idr: number;
  featured: boolean;
}

interface TokenCatalog {
  on_sale: boolean;
  packages: TokenOffer[];
  models: string[];
  terms_version: string;
}

async function fetchTokenCatalog(): Promise<TokenCatalog> {
  const resp = await fetch("/portal/token/packages");
  if (!resp.ok) throw new Error(`token packages: HTTP ${resp.status}`);
  const data = (await resp.json()) as Partial<TokenCatalog>;
  return {
    on_sale: !!data.on_sale,
    packages: data.packages ?? [],
    models: data.models ?? [],
    terms_version: data.terms_version ?? "",
  };
}

function formatIDR(n: number): string {
  return new Intl.NumberFormat("id-ID", { style: "currency", currency: "IDR", maximumFractionDigits: 0 }).format(n);
}

const DECIMAL = new Intl.NumberFormat("id-ID", { maximumFractionDigits: 1 });

/** 20000000 -> "20 juta", 1000000000 -> "1 miliar". */
function tokenAmount(n: number): string {
  if (n >= 1_000_000_000) return `${DECIMAL.format(n / 1_000_000_000)} miliar`;
  if (n >= 1_000_000) return `${DECIMAL.format(n / 1_000_000)} juta`;
  return new Intl.NumberFormat("id-ID").format(n);
}

function pricePerMillion(p: TokenOffer): number {
  return Math.round(p.price_idr / (p.tokens / 1_000_000));
}

/** Saving per token against the smallest tokens package; 0 for that one. */
function discountPct(p: TokenOffer, base?: TokenOffer): number {
  if (!base || p.id === base.id || p.tokens <= 0 || base.tokens <= 0) return 0;
  const pct = Math.round((1 - p.price_idr / p.tokens / (base.price_idr / base.tokens)) * 100);
  return pct > 0 ? pct : 0;
}

export default function PublicTokenPackages() {
  const q = useQuery({ queryKey: ["public-token-packages"], queryFn: fetchTokenCatalog, staleTime: 60_000, retry: 1 });
  const data = q.data;
  const onSale = !!data?.on_sale;
  const tokenPkgs = (data?.packages ?? []).filter((p) => p.kind === "tokens" && p.tokens > 0);
  const daysPkgs = (data?.packages ?? []).filter((p) => p.kind === "days");
  const models = data?.models ?? [];
  const base = tokenPkgs.reduce<TokenOffer | undefined>((a, p) => (!a || p.tokens < a.tokens ? p : a), undefined);
  const cheapestPerM = tokenPkgs.reduce<number | null>((min, p) => {
    const v = pricePerMillion(p);
    return min === null || v < min ? v : min;
  }, null);

  useEffect(() => {
    document.title = "Tokenizer - Token AI Frontier";
  }, []);

  return (
    <PublicLayout>
      <section className="max-w-3xl">
        <h1 className="text-[34px] font-semibold leading-[1.08] tracking-[-1.2px] text-[var(--ink)] sm:text-[46px]">
          Token AI Frontier
        </h1>
        <p className="mt-5 max-w-2xl text-[18px] font-medium leading-snug text-[var(--ink)] sm:text-[20px]">
          Bayar sekali di depan, lalu fokus ke kerjaan. Tanpa tagihan kejutan di akhir bulan.
        </p>
        <p className="mt-3 max-w-2xl text-[16px] leading-relaxed text-[var(--muted)]">
          Model AI frontier lewat satu key. Makin besar paketnya, makin murah per token
          {cheapestPerM !== null ? `: mulai ${formatIDR(cheapestPerM)} per 1 juta token.` : "."}
        </p>
      </section>

      <section className="mt-10" aria-labelledby="packages-title">
        <h2 id="packages-title" className="sr-only">Pilih paket</h2>
        {q.isLoading ? (
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3" aria-busy="true" aria-label="Memuat paket">
            {[0, 1, 2].map((i) => (
              <div key={i} className="h-[236px] animate-pulse rounded-xl border border-[var(--line)] bg-[var(--paper)]" />
            ))}
          </div>
        ) : q.isError ? (
          <p role="alert" className="rounded-xl border border-dashed border-[var(--line)] p-8 text-center text-[14px] text-[var(--muted)]">
            Daftar paket gagal dimuat. Coba muat ulang halaman.
          </p>
        ) : tokenPkgs.length === 0 ? (
          <div className="rounded-xl border border-dashed border-[var(--line)] p-8 text-center">
            <p className="text-[15px] font-medium text-[var(--ink)]">Paket token belum tersedia</p>
            <p className="mt-1.5 text-[14px] text-[var(--muted)]">
              Pengumuman pembukaan paket kami kirim di{" "}
              <a href={TELEGRAM_URL} target="_blank" rel="noopener noreferrer" className={LINK}>grup Telegram</a>.
            </p>
          </div>
        ) : (
          <>
            {!onSale && (
              <p role="status" className="mb-4 rounded-xl border border-[var(--line)] bg-[var(--paper)] px-4 py-3 text-[14px] text-[var(--muted)]">
                Pembayaran belum dibuka, jadi paket di bawah belum bisa dibeli.
              </p>
            )}
            <ul className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
              {tokenPkgs.map((p) => (
                <PackageCard key={p.id} pkg={p} discount={discountPct(p, base)} onSale={onSale} />
              ))}
            </ul>
            {daysPkgs.map((p) => (
              <DaysAddOn key={p.id} pkg={p} />
            ))}
          </>
        )}
      </section>

      <section className="mt-12" aria-labelledby="models-title">
        <h2 id="models-title" className="text-[18px] font-semibold tracking-[-0.3px] text-[var(--ink)]">
          Available Models
        </h2>
        <p className="mt-1.5 max-w-2xl text-[14px] leading-relaxed text-[var(--muted)]">
          Lewat endpoint chat format OpenAI, Anthropic, atau Gemini. Aturan masa aktif dan pemakaian ada di{" "}
          <a href={TERMS_URL} className={LINK}>ketentuan layanan</a>.
        </p>
        {q.isLoading ? (
          <div className="mt-4 h-24 animate-pulse rounded-xl border border-[var(--line)] bg-[var(--paper)]" aria-hidden="true" />
        ) : q.isError ? (
          <p role="alert" className="mt-4 text-[14px] text-[var(--muted)]">Daftar model gagal dimuat. Coba muat ulang halaman.</p>
        ) : models.length === 0 ? (
          <p className="mt-4 text-[14px] text-[var(--muted)]">Daftar model belum tersedia.</p>
        ) : (
          <ul className="mt-4 divide-y divide-[var(--line)] overflow-hidden rounded-xl border border-[var(--line)] bg-[var(--paper)]">
            {models.map((m) => {
              const fam = modelFamily(m);
              return (
                <li key={m} className="flex min-h-14 items-center gap-3 px-4 py-2.5 sm:px-5">
                  <FamilyLogo family={fam} />
                  <span className="min-w-0 flex-1 font-mono text-[14px] text-[var(--ink)] [overflow-wrap:anywhere]">{m}</span>
                  {fam && <span className="shrink-0 text-[13px] text-[var(--muted)]">{fam.label}</span>}
                </li>
              );
            })}
          </ul>
        )}
      </section>

      <section className="mt-16 rounded-xl border border-[var(--line)] bg-[var(--paper)] p-6 sm:p-8">
        <div className="flex flex-col gap-5 md:flex-row md:items-center md:justify-between">
          <div className="max-w-xl">
            <h2 className="text-[20px] font-semibold tracking-[-0.3px] text-[var(--ink)]">Beli paket dari portal</h2>
            <p className="mt-2 text-[14px] leading-relaxed text-[var(--muted)]">
              Sisa token dan tanggal berakhir bisa kamu pantau di halaman yang sama. Ada pertanyaan? Tanya di Telegram.
            </p>
          </div>
          <div className="flex flex-col gap-3 sm:flex-row">
            <a
              href={BUY_URL}
              className={`inline-flex h-11 items-center justify-center gap-2 rounded-lg bg-[var(--action)] px-5 text-[14px] font-medium text-[var(--action-fg)] no-underline transition-opacity hover:opacity-90 ${FOCUS}`}
            >
              Buka portal token
              <ArrowRight className="h-4 w-4" aria-hidden="true" />
            </a>
            <a
              href={TELEGRAM_URL}
              target="_blank"
              rel="noopener noreferrer"
              className={`inline-flex h-11 items-center justify-center gap-2 rounded-lg border border-[var(--line)] bg-[var(--bg)] px-5 text-[14px] font-medium text-[var(--ink)] no-underline transition-colors hover:border-[var(--line-strong)] ${FOCUS}`}
            >
              <Send className="h-4 w-4" strokeWidth={1.75} aria-hidden="true" />
              Grup Telegram
            </a>
          </div>
        </div>
      </section>
    </PublicLayout>
  );
}

function PackageCard({ pkg: p, discount, onSale }: { pkg: TokenOffer; discount: number; onSale: boolean }) {
  return (
    <li
      className={`flex flex-col rounded-xl border bg-[var(--paper)] p-5 sm:p-6 ${
        p.featured ? "border-[var(--green)] ring-1 ring-inset ring-[var(--green)]" : "border-[var(--line)]"
      }`}
    >
      <div className="flex flex-wrap items-center gap-2">
        <h3 className="text-[15px] font-semibold text-[var(--ink)]">{p.name}</h3>
        {p.featured && (
          <span className="rounded-md bg-[var(--accent-bg)] px-2 py-0.5 text-[12px] font-medium text-[var(--green)]">Rekomendasi</span>
        )}
      </div>
      <p className="mt-4 text-[26px] font-semibold leading-tight tracking-[-0.6px] text-[var(--ink)]">{tokenAmount(p.tokens)} token</p>
      <p className="mt-1 font-mono text-[20px] font-semibold tabular-nums text-[var(--ink)]">{formatIDR(p.price_idr)}</p>
      <dl className="mt-4 space-y-1.5 text-[13.5px]">
        <div className="flex justify-between gap-3">
          <dt className="text-[var(--muted)]">Per 1 juta token</dt>
          <dd className="font-mono tabular-nums text-[var(--ink)]">{formatIDR(pricePerMillion(p))}</dd>
        </div>
        <div className="flex justify-between gap-3">
          <dt className="text-[var(--muted)]">Masa aktif</dt>
          <dd className="tabular-nums text-[var(--ink)]">{p.days} hari</dd>
        </div>
        {discount > 0 && (
          <div className="flex justify-between gap-3">
            <dt className="text-[var(--muted)]">Dibanding paket terkecil</dt>
            <dd className="font-medium tabular-nums text-[var(--green)]">Hemat {discount}%</dd>
          </div>
        )}
      </dl>
      <div className="mt-auto pt-5">
        {onSale ? (
          <a
            href={BUY_URL}
            className={`inline-flex h-11 w-full items-center justify-center rounded-lg px-5 text-[14px] font-medium no-underline transition-opacity hover:opacity-90 ${
              p.featured ? "bg-[var(--action)] text-[var(--action-fg)]" : "border border-[var(--line)] bg-[var(--bg)] text-[var(--ink)]"
            } ${FOCUS}`}
            aria-label={`Beli ${p.name} di portal`}
          >
            Beli di portal
          </a>
        ) : (
          <span
            aria-disabled="true"
            className="inline-flex h-11 w-full cursor-not-allowed items-center justify-center rounded-lg border border-[var(--line)] bg-[var(--soft)] px-5 text-[14px] font-medium text-[var(--muted)]"
          >
            Belum dibuka
          </span>
        )}
      </div>
    </li>
  );
}

function DaysAddOn({ pkg: p }: { pkg: TokenOffer }) {
  return (
    <div className="mt-4 flex flex-col gap-3 rounded-xl border border-[var(--line)] bg-[var(--paper)] p-5 sm:flex-row sm:items-center sm:gap-5">
      <CalendarPlus className="h-5 w-5 shrink-0 text-[var(--green)]" strokeWidth={1.75} aria-hidden="true" />
      <div className="min-w-0 flex-1">
        <p className="text-[15px] font-semibold text-[var(--ink)]">
          Tambahan: {p.name}, <span className="font-mono tabular-nums">{formatIDR(p.price_idr)}</span>
        </p>
        <p className="mt-1 text-[14px] leading-relaxed text-[var(--muted)]">
          Memperpanjang masa aktif {p.days} hari tanpa menambah token. Hanya bisa dibeli saat paket token masih aktif.
        </p>
      </div>
    </div>
  );
}

type Family = { label: string; icon: string };

// Model-name prefix -> provider family, for the logo next to each model.
// Chain names are admin-chosen, so an unknown prefix shows a neutral tile.
const FAMILIES: { match: RegExp; family: Family }[] = [
  { match: /^(gpt|o\d|chatgpt|codex)/i, family: { label: "OpenAI", icon: "/providers/openai.png" } },
  { match: /^deepseek/i, family: { label: "DeepSeek", icon: "/providers/deepseek.png" } },
  { match: /^(kimi|moonshot)/i, family: { label: "Moonshot AI", icon: "/providers/kimi.png" } },
  { match: /^(glm|zhipu|z-ai)/i, family: { label: "Zhipu AI", icon: "/providers/glm.png" } },
  { match: /^(qwen|qwq)/i, family: { label: "Qwen", icon: "/providers/qwen.png" } },
  { match: /^(claude|anthropic)/i, family: { label: "Anthropic", icon: "/providers/anthropic.png" } },
  { match: /^(gemini|gemma)/i, family: { label: "Google", icon: "/providers/gemini.png" } },
  { match: /^grok/i, family: { label: "xAI", icon: "/providers/xai.png" } },
  { match: /^(mistral|codestral|magistral|devstral)/i, family: { label: "Mistral", icon: "/providers/mistral.png" } },
  { match: /^minimax/i, family: { label: "MiniMax", icon: "/providers/minimax.png" } },
  { match: /^mimo/i, family: { label: "Xiaomi", icon: "/providers/xiaomi-mimo.png" } },
];

function modelFamily(model: string): Family | null {
  const name = model.trim().replace(/^[^/]*\//, "");
  return FAMILIES.find((f) => f.match.test(name))?.family ?? null;
}

function FamilyLogo({ family }: { family: Family | null }) {
  const [failed, setFailed] = useState(false);
  if (!family || failed) {
    return (
      <span
        className="inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-lg border border-[var(--line)] bg-[var(--soft)] text-[var(--muted)]"
        aria-hidden="true"
      >
        <Cpu className="h-4 w-4" strokeWidth={1.75} />
      </span>
    );
  }
  return (
    <span className="inline-flex h-8 w-8 shrink-0 items-center justify-center overflow-hidden rounded-lg border border-[var(--line)] bg-white" aria-hidden="true">
      <img src={family.icon} alt="" className="h-full w-full object-contain p-1" onError={() => setFailed(true)} />
    </span>
  );
}
