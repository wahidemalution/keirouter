import type { PortalModelProvider } from "./api.ts";

/** One distinct price on a provider, with how many offers share it. */
export interface OfferLevel {
  input: number;
  output: number;
  discountPct: number | null;
  count: number;
}

// offerLevels merges offers with the same price, keeping the cheapest-first
// order the server sends.
export function offerLevels(p: PortalModelProvider): OfferLevel[] {
  const levels: OfferLevel[] = [];
  for (const o of p.offers ?? []) {
    const last = levels[levels.length - 1];
    if (last && last.input === o.input_per_m && last.output === o.output_per_m) {
      last.count++;
      continue;
    }
    levels.push({ input: o.input_per_m, output: o.output_per_m, discountPct: o.discount_pct, count: 1 });
  }
  return levels;
}

// discountText is "31% off official", or null when there is nothing to say.
export function discountText(pct: number | null | undefined): string | null {
  if (pct == null) return null;
  return pct > 0 ? `${pct}% off official` : "No discount vs official";
}

// latencyText is "850 ms" or "2.3 s".
export function latencyText(ms: number | null | undefined): string | null {
  if (ms == null || !(ms > 0)) return null;
  if (ms < 1000) return `${Math.round(ms)} ms`;
  const s = ms / 1000;
  return `${s < 10 ? s.toFixed(1) : Math.round(s)} s`;
}
