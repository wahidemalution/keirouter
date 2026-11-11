// Paket Token landing at `/paket-token`. Reads the public package list
// (/portal/token/packages): tokens packages as cards, the days package as an
// add-on, and the models a token key may call. Buying happens in the portal;
// while checkout is closed the cards show without a buy link.

import { useEffect } from "react";
import { useQuery } from "@tanstack/react-query";
import { ArrowRight, CalendarPlus, Send } from "lucide-react";
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

const STEPS: { title: string; body: string }[] = [
  { title: "Masuk ke portal", body: "Login dengan akun Google. Paket token dibeli dari menu Token Packages." },
  { title: "Pilih paket dan bayar", body: "Centang persetujuan ketentuan, lalu bayar lewat gerbang pembayaran di portal." },
  { title: "Salin key token", body: "Setelah pembayaran dikonfirmasi, key token muncul di halaman yang sama. Pakai seperti API key biasa." },
];

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
    document.title = "Tokenizer - Paket Token";
  }, []);

  return (
    <PublicLayout>
      <section className="max-w-3xl">
        <h1 className="text-[34px] font-semibold leading-[1.08] tracking-[-1.2px] text-[var(--ink)] sm:text-[46px]">
          Paket Token GPT-6
        </h1>
        <p className="mt-5 max-w-2xl text-[16px] leading-relaxed text-[var(--muted)]">
          Beli token di muka, lalu pakai dengan key token sendiri. Key ini terpisah dari saldo pay-as-you-go, jadi
          pemakaiannya tidak memotong saldo
          {cheapestPerM !== null ? `. Harga mulai ${formatIDR(cheapestPerM)} per 1 juta token.` : "."}
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

      <section className="mt-16 grid gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(0,1.4fr)]">
        <div className="rounded-xl border border-[var(--line)] bg-[var(--paper)] p-5 sm:p-6">
          <h2 className="text-[18px] font-semibold tracking-[-0.3px] text-[var(--ink)]">Model yang bisa dipakai</h2>
          <p className="mt-1.5 text-[14px] leading-relaxed text-[var(--muted)]">
            Key token hanya menerima model di bawah ini, lewat endpoint chat format OpenAI, Anthropic, atau Gemini. Gambar, audio, embedding, dan pencarian tidak tersedia.
          </p>
          {q.isLoading ? (
            <div className="mt-4 h-8 w-2/3 animate-pulse rounded-md bg-[var(--soft)]" aria-hidden="true" />
          ) : models.length === 0 ? (
            <p className="mt-4 text-[14px] text-[var(--muted)]">Daftar model belum tersedia.</p>
          ) : (
            <ul className="mt-4 flex flex-wrap gap-2">
              {models.map((m) => (
                <li key={m} className="rounded-md border border-[var(--line)] bg-[var(--bg)] px-2.5 py-1 font-mono text-[13px] text-[var(--ink)] [overflow-wrap:anywhere]">
                  {m}
                </li>
              ))}
            </ul>
          )}
        </div>

        <div className="rounded-xl border border-[var(--line)] bg-[var(--paper)] p-5 sm:p-6">
          <h2 className="text-[18px] font-semibold tracking-[-0.3px] text-[var(--ink)]">Aturan singkat</h2>
          <ul className="mt-3 list-disc space-y-2 pl-5 text-[14px] leading-relaxed text-[var(--muted)]">
            <li>Token dihitung dari input ditambah output, termasuk reasoning dan input yang di-cache. 1 token = 1 token untuk semua model.</li>
            <li>Masa aktif dihitung sejak pembayaran paket pertama. Saat berakhir, sisa token hangus dan key token berhenti bekerja.</li>
            <li>Beli paket token lagi saat masih aktif: token bertambah, tanggal berakhir tetap.</li>
            <li>Paket +hari hanya memperpanjang masa aktif, tidak menambah token, dan hanya bisa dibeli saat paket masih aktif.</li>
            <li>Token tidak bisa ditukar menjadi saldo, dan saldo tidak bisa ditukar menjadi token.</li>
          </ul>
          <a href={TERMS_URL} className={`mt-4 inline-block text-[14px] ${LINK}`}>
            Baca ketentuan Paket Token lengkap
          </a>
        </div>
      </section>

      <section className="mt-16">
        <h2 className="text-[24px] font-semibold tracking-[-0.5px] text-[var(--ink)] sm:text-[28px]">Cara membeli</h2>
        <ol className="mt-6 divide-y divide-[var(--line)] rounded-xl border border-[var(--line)] bg-[var(--paper)]">
          {STEPS.map((s, i) => (
            <li key={s.title} className="flex gap-4 p-5">
              <span className="font-mono text-[13px] leading-[22px] text-[var(--muted)]" aria-hidden="true">{i + 1}.</span>
              <div className="min-w-0">
                <h3 className="text-[15px] font-semibold text-[var(--ink)]">{s.title}</h3>
                <p className="mt-1 text-[14px] leading-relaxed text-[var(--muted)]">{s.body}</p>
              </div>
            </li>
          ))}
        </ol>
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
          <span className="rounded-md bg-[var(--accent-bg)] px-2 py-0.5 text-[12px] font-medium text-[var(--green)]">Paling laris</span>
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
