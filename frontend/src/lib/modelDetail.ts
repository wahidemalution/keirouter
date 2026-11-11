// Formatting and state helpers for the model detail popup. Pure, so they run
// under node --test without a DOM.

import type { PublicModelDetail } from "./publicApi.ts";
import { fmtRate } from "./format.ts";

const DASH = "–";

export function fmtPct(v: number | null): string {
  if (v == null) return DASH;
  return `${v.toLocaleString("id-ID", { maximumFractionDigits: 1 })}%`;
}

/** Input and output price per 1M token, e.g. "$0,0003 / $0,015". */
export function fmtPricePair(input: number | null, output: number | null): string {
  if (input == null || output == null) return DASH;
  return `${fmtRate(input)} / ${fmtRate(output)}`;
}

export function fmtLatency(ms: number | null): string {
  if (ms == null) return DASH;
  return `${(ms / 1000).toLocaleString("id-ID", { minimumFractionDigits: 2, maximumFractionDigits: 2 })} dtk`;
}

export function fmtTps(v: number | null): string {
  if (v == null) return DASH;
  return `${Math.round(v).toLocaleString("id-ID")} tok/dtk`;
}

export function agoLabel(asOf: string | null, now: number): string | null {
  if (!asOf) return null;
  const at = Date.parse(asOf);
  if (Number.isNaN(at)) return null;
  const secs = Math.max(0, Math.floor((now - at) / 1000));
  if (secs < 60) return `Diperbarui ${secs} detik lalu`;
  const mins = Math.floor(secs / 60);
  if (mins < 60) return `Diperbarui ${mins} menit lalu`;
  return `Diperbarui ${Math.floor(mins / 60)} jam lalu`;
}

export function detailState(d: PublicModelDetail): "unavailable" | "no-offers" | "ok" {
  // An empty book is a real answer, not a missing source.
  if (d.offer_count === 0) return "no-offers";
  if (d.best == null && d.performance == null) return "unavailable";
  return "ok";
}
