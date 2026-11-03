// Market detail popup for one live-priced model on the public catalog.
// Styled with `.landing-root` tokens only, so it is portalled into that root
// (not <body>) to keep the theme variables. Focus handling follows the admin
// Modal in components/ui: close button first, Tab trapped, focus returned.

import { useEffect, useId, useRef, useState, type RefObject } from "react";
import { createPortal } from "react-dom";
import { useQuery } from "@tanstack/react-query";
import { X } from "lucide-react";
import { fetchPublicModelDetail, type PublicModelDetail } from "../lib/publicApi";
import { agoLabel, detailState, fmtLatency, fmtPct, fmtPricePair, fmtTps } from "../lib/modelDetail";
import { fmtCount, fmtRate } from "../lib/format";

const FOCUS = "focus:outline-none focus-visible:ring-2 focus-visible:ring-[var(--green)]";

const FOCUSABLE = [
  "button:not([disabled])",
  "[href]",
  "input:not([disabled])",
  "[tabindex]:not([tabindex='-1'])",
].join(",");

export function ModelDetailDialog({
  model,
  open,
  onClose,
  returnFocusRef,
}: {
  model: string;
  open: boolean;
  onClose: () => void;
  returnFocusRef: RefObject<HTMLElement | null>;
}) {
  const dialogRef = useRef<HTMLDivElement>(null);
  const closeRef = useRef<HTMLButtonElement>(null);
  const onCloseRef = useRef(onClose);
  const titleId = useId();
  const [now, setNow] = useState(() => Date.now());

  const query = useQuery({
    queryKey: ["public-model-detail", model],
    queryFn: () => fetchPublicModelDetail(model),
    enabled: open,
    staleTime: 30_000,
    retry: false,
  });

  useEffect(() => {
    onCloseRef.current = onClose;
  }, [onClose]);

  // A refetch after an error goes back to "pending" in TanStack v5; keep the
  // error block (and its focused retry button) up until data arrives.
  const showError = query.data === undefined && query.errorUpdateCount > 0;
  // The retry button unmounts when data arrives; keep focus inside the dialog.
  const hadError = useRef(false);
  useEffect(() => {
    if (hadError.current && !showError && dialogRef.current?.contains(document.activeElement) === false) {
      closeRef.current?.focus();
    }
    hadError.current = showError;
  }, [showError]);

  // Keeps "Diperbarui N detik lalu" honest while the popup stays open.
  useEffect(() => {
    if (!open) return;
    const id = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(id);
  }, [open]);

  useEffect(() => {
    if (!open) return;
    const dialog = dialogRef.current;
    const returnTo = returnFocusRef.current;
    const prevOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    closeRef.current?.focus();

    const focusables = () => Array.from(dialog?.querySelectorAll<HTMLElement>(FOCUSABLE) ?? []);
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        onCloseRef.current();
        return;
      }
      if (e.key !== "Tab" || !dialog) return;
      const items = focusables();
      if (items.length === 0) {
        e.preventDefault();
        return;
      }
      const first = items[0];
      const last = items[items.length - 1];
      const active = document.activeElement;
      if (e.shiftKey && (active === first || !dialog.contains(active))) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && (active === last || !dialog.contains(active))) {
        e.preventDefault();
        first.focus();
      }
    };
    const onFocusIn = (e: FocusEvent) => {
      if (dialog && e.target instanceof Node && !dialog.contains(e.target)) closeRef.current?.focus();
    };
    document.addEventListener("keydown", onKey);
    document.addEventListener("focusin", onFocusIn);
    return () => {
      document.removeEventListener("keydown", onKey);
      document.removeEventListener("focusin", onFocusIn);
      document.body.style.overflow = prevOverflow;
      if (returnTo?.isConnected) returnTo.focus();
    };
  }, [open, returnFocusRef]);

  if (!open) return null;

  const root = returnFocusRef.current?.closest(".landing-root") ?? document.body;
  const data = query.data;
  const updated = data ? agoLabel(data.as_of ?? data.performance?.as_of ?? null, now) : null;

  return createPortal(
    <div
      className="fixed inset-0 z-50 flex items-end justify-center bg-black/45 sm:items-center sm:p-4"
      onClick={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        className="flex max-h-[90dvh] w-full flex-col rounded-t-2xl border border-[var(--line)] bg-[var(--paper)] text-[var(--ink)] sm:max-w-3xl sm:rounded-xl lg:max-w-[52rem]"
      >
        <div className="flex items-start gap-3 border-b border-[var(--line)] px-4 py-3 sm:px-5">
          <div className="min-w-0 flex-1 pt-1.5">
            <h2 id={titleId} className="break-words text-[16px] font-semibold tracking-[-0.2px]">
              {model}
            </h2>
            <p className="mt-0.5 min-h-[18px] text-[12px] text-[var(--muted)]">{updated ?? ""}</p>
          </div>
          <button
            ref={closeRef}
            type="button"
            onClick={onClose}
            aria-label="Tutup"
            title="Tutup"
            className={`-mr-2 flex h-11 w-11 shrink-0 items-center justify-center rounded-lg text-[var(--muted)] transition-colors hover:bg-[var(--soft)] hover:text-[var(--ink)] ${FOCUS}`}
          >
            <X className="h-5 w-5" aria-hidden="true" />
          </button>
        </div>
        <div className="overflow-y-auto overscroll-contain px-4 pb-[calc(16px+env(safe-area-inset-bottom))] pt-4 sm:px-5 sm:pb-5">
          {query.data === null ? (
            <Notice>Detail belum tersedia untuk model ini.</Notice>
          ) : query.data !== undefined ? (
            <DetailBody data={query.data} />
          ) : !showError ? (
            <DetailSkeleton />
          ) : (
            <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-[var(--line)] bg-[var(--bg)] px-3 py-2" role="alert">
              <p className="text-[13px] text-[var(--ink)]">Detail model gagal dimuat.</p>
              <button
                type="button"
                onClick={() => {
                  if (!query.isFetching) void query.refetch();
                }}
                aria-disabled={query.isFetching}
                className={`min-h-[44px] rounded-lg border border-[var(--line)] bg-[var(--paper)] px-4 text-[13px] font-medium text-[var(--ink)] transition-colors hover:border-[var(--line-strong)] aria-disabled:opacity-60 ${FOCUS}`}
              >
                {query.isFetching ? "Memuat…" : "Coba lagi"}
              </button>
            </div>
          )}
        </div>
      </div>
    </div>,
    root,
  );
}

function DetailSkeleton() {
  return (
    <div aria-busy="true">
      <span className="sr-only" role="status">Memuat detail model</span>
      <div className="h-[104px] animate-pulse rounded-lg bg-[var(--soft)]" />
      <div className="mt-3 grid grid-cols-2 gap-3">
        <div className="h-[64px] animate-pulse rounded-lg bg-[var(--soft)]" />
        <div className="h-[64px] animate-pulse rounded-lg bg-[var(--soft)]" />
      </div>
      <div className="mt-5 h-[120px] animate-pulse rounded-lg bg-[var(--soft)]" />
    </div>
  );
}

function Notice({ children }: { children: string }) {
  return (
    <p className="rounded-lg border border-dashed border-[var(--line-strong)] px-3 py-4 text-center text-[13px] text-[var(--muted)]">
      {children}
    </p>
  );
}

function Stat({ label, value, note, wide }: { label: string; value: string; note?: string; wide?: boolean }) {
  return (
    <div className={`min-w-0 rounded-lg border border-[var(--line)] px-3 py-2.5 ${wide ? "col-span-2" : ""}`}>
      <p className="text-[12px] text-[var(--muted)]">{label}</p>
      <p className="mt-0.5 font-mono text-[18px] font-semibold tabular-nums">{value}</p>
      {note && <p className="text-[12px] text-[var(--muted)]">{note}</p>}
    </div>
  );
}

function DetailBody({ data }: { data: PublicModelDetail }) {
  const state = detailState(data);
  const best = data.best;
  const routes = data.performance?.routes ?? [];
  const showMedian = state === "ok" && best != null;

  return (
    <>
      {state === "unavailable" ? (
        <Notice>Data pasar sedang tidak tersedia</Notice>
      ) : state === "no-offers" ? (
        <Notice>Belum ada penawaran trusted saat ini</Notice>
      ) : best == null ? (
        <Notice>Data pasar sedang tidak tersedia</Notice>
      ) : (
        <div className="rounded-lg bg-[var(--tag-bg)] px-4 py-3 text-[var(--tag-ink)]">
          <p className="text-[12px] font-medium">Diskon terbaik dari harga resmi</p>
          <p className="font-mono text-[40px] font-bold leading-tight tabular-nums">{fmtPct(best.discount_pct)}</p>
          <p className="font-mono text-[12px] tabular-nums">
            Input {fmtRate(best.input_per_m)} · Output {fmtRate(best.output_per_m)} per 1M token
          </p>
        </div>
      )}
      <div className="mt-3 grid grid-cols-2 gap-3">
        {showMedian && (
          <Stat
            label="Diskon median"
            value={fmtPct(data.median_discount_pct)}
            note={data.offer_count != null ? `dari ${fmtCount(data.offer_count)} penawaran` : undefined}
          />
        )}
        <Stat label="Request 24 jam terakhir" value={fmtCount(data.requests_24h)} wide={!showMedian} />
      </div>
      {routes.length > 0 && <ProviderTable routes={routes} />}
    </>
  );
}

// The provider column stays pinned while the numbers scroll on narrow screens.
const PINNED = "sticky left-0 z-[1] bg-[var(--paper)]";

function ProviderTable({ routes }: { routes: NonNullable<PublicModelDetail["performance"]>["routes"] }) {
  const titleId = useId();
  const noteId = useId();
  return (
    <section className="mt-5">
      <h3 id={titleId} className="text-[13px] font-semibold">Harga & performa per provider</h3>
      <p id={noteId} className="mt-0.5 text-[12px] text-[var(--muted)]">
        Harga per 1M token dari penawaran termurah tiap provider. Performa dihitung dari 24 jam terakhir.
      </p>
      <div
        className={`mt-2 overflow-x-auto rounded-lg border border-[var(--line)] ${FOCUS}`}
        tabIndex={0}
        role="region"
        aria-labelledby={titleId}
        aria-describedby={noteId}
      >
        <table className="w-full min-w-[560px] whitespace-nowrap text-left text-[12.5px]">
          <thead className="text-[12px] text-[var(--muted)]">
            <tr>
              <th scope="col" className={`px-3 py-2 font-medium ${PINNED}`}>Provider</th>
              <th scope="col" className="px-3 py-2 text-right font-medium">Input / Output</th>
              <th scope="col" className="px-3 py-2 text-right font-medium">Diskon</th>
              <th scope="col" className="px-3 py-2 text-right font-medium">Latensi p50</th>
              <th scope="col" className="px-3 py-2 text-right font-medium">Latensi p95</th>
              <th scope="col" className="px-3 py-2 text-right font-medium">Kecepatan</th>
            </tr>
          </thead>
          <tbody className="font-mono tabular-nums">
            {routes.map((r, i) => (
              <tr key={`${r.name}-${i}`} className="border-t border-[var(--line)]">
                <th scope="row" className={`px-3 py-2 font-sans font-medium ${PINNED}`}>{r.name}</th>
                <td className="px-3 py-2 text-right">{fmtPricePair(r.input_per_m, r.output_per_m)}</td>
                <td className="px-3 py-2 text-right">{fmtPct(r.discount_pct)}</td>
                <td className="px-3 py-2 text-right">{fmtLatency(r.p50_latency_ms)}</td>
                <td className="px-3 py-2 text-right">{fmtLatency(r.p95_latency_ms)}</td>
                <td className="px-3 py-2 text-right">{fmtTps(r.tokens_per_sec)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}
