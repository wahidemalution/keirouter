import { ListOrdered, Repeat2, type LucideIcon } from "lucide-react";
import type { Chain, ChainStep, ChainStepPayload, Provider } from "../../lib/api";
// Explicit extension: the node:test runner (npm test) resolves ESM literally.
import { ICONS } from "../../lib/icons.ts";

export const CHAIN_MODEL_KIND = "llm";

export type ChainStrategy = "priority" | "round_robin" | "latency" | "cost";

export const CACHE_WRITE_FACTOR = 1.25;
export const CACHE_READ_FACTOR = 0.1;

export type StepPriceSource = "market" | "static";

export interface DraftChainStep {
  id: string;
  provider: string;
  model: string;
  marketSlug: string;
  inputPerM: number;
  outputPerM: number;
  cacheWritePerM: number;
  cacheReadPerM: number;
  // Static price fields hold the raw input text; "" means unset (cache rates
  // derive from input, markup follows the chain).
  priceSource: StepPriceSource;
  staticInputPerM: string;
  staticOutputPerM: string;
  staticCacheReadPerM: string;
  staticCacheWritePerM: string;
  stepMarkup: string;
}

// deriveCacheRates recomputes cache write/read from the input rate, but keeps a
// field the operator edited by hand. A field is "auto" when it is unset (0) or
// still equals the formula result for the previous input; a manual override is
// left untouched.
export const deriveCacheRates = (
  input: number,
  prev: { cacheWritePerM: number; cacheReadPerM: number; prevInput?: number },
): { cacheWritePerM: number; cacheReadPerM: number } => {
  const oldInput = prev.prevInput ?? input;
  const autoWrite = prev.cacheWritePerM === 0 || prev.cacheWritePerM === +(oldInput * CACHE_WRITE_FACTOR).toFixed(8);
  const autoRead = prev.cacheReadPerM === 0 || prev.cacheReadPerM === +(oldInput * CACHE_READ_FACTOR).toFixed(8);
  return {
    cacheWritePerM: autoWrite ? +(input * CACHE_WRITE_FACTOR).toFixed(8) : prev.cacheWritePerM,
    cacheReadPerM: autoRead ? +(input * CACHE_READ_FACTOR).toFixed(8) : prev.cacheReadPerM,
  };
};

export const isRoundRobinStrategy = (strategy: string) =>
  strategy === "round_robin" || strategy === "round-robin";

export const normalizeChainStrategy = (strategy: string): ChainStrategy => {
  if (isRoundRobinStrategy(strategy)) return "round_robin";
  if (strategy === "latency" || strategy === "cost") return strategy;
  return "priority";
};

export const strategyLabel = (strategy: string) => {
  switch (normalizeChainStrategy(strategy)) {
    case "round_robin": return "Round robin";
    case "latency": return "Latency";
    case "cost": return "Cost";
    default: return "Priority";
  }
};

// strategyIcon gives each strategy one glyph, used by the editor picker and
// the strategy badge in the chain list.
export const strategyIcon = (strategy: string): LucideIcon => {
  switch (normalizeChainStrategy(strategy)) {
    case "round_robin": return Repeat2;
    case "latency": return ICONS.latency;
    case "cost": return ICONS.spend;
    default: return ListOrdered;
  }
};

// strategyDescription is the one-line explanation shown under the strategy
// picker. Keep it short: the route preview shows the actual order.
export const strategyDescription = (strategy: string) => {
  switch (normalizeChainStrategy(strategy)) {
    case "round_robin": return "Rotates the first model on each request.";
    case "latency": return "Fastest measured model first; unmeasured ones after.";
    case "cost": return "Cheapest priced model first; unpriced ones after.";
    default: return "Tries models in the order listed.";
  }
};

export const isLLMProvider = (provider: Provider) =>
  !provider.service_kinds?.length || provider.service_kinds.includes(CHAIN_MODEL_KIND);

export const providerIcon = (provider?: Provider, providerID?: string) =>
  provider?.icon || (providerID ? `/providers/${providerID}.png` : "");

const numText = (v?: number | null) => (v === undefined || v === null ? "" : String(v));

export const makeDraftStep = (step?: Partial<ChainStep> & { provider: string; model: string }): DraftChainStep => ({
  id: crypto.randomUUID(),
  provider: step?.provider ?? "",
  model: step?.model ?? "",
  marketSlug: step?.market_slug ?? "",
  inputPerM: 0,
  outputPerM: 0,
  cacheWritePerM: 0,
  cacheReadPerM: 0,
  priceSource: (step?.static_input_per_m ?? 0) > 0 || (step?.static_output_per_m ?? 0) > 0 ? "static" : "market",
  staticInputPerM: step?.static_input_per_m ? String(step.static_input_per_m) : "",
  staticOutputPerM: step?.static_output_per_m ? String(step.static_output_per_m) : "",
  staticCacheReadPerM: numText(step?.static_cache_read_per_m),
  staticCacheWritePerM: numText(step?.static_cache_write_per_m),
  stepMarkup: numText(step?.markup_percent),
});

export type StaticStepPreview =
  | { ok: true; markup: number; inputPerM: number; outputPerM: number; cacheReadPerM: number; cacheWritePerM: number }
  | { ok: false; error: string };

// parseOptional reads an optional numeric field: "" is undefined, anything
// else must be a finite number.
const parseOptional = (raw: string): number | undefined => (raw.trim() === "" ? undefined : Number(raw));

// staticStepPreview computes what the customer pays per 1M tokens on a
// static-price step. It mirrors the gateway's priceStaticSteps: the step
// markup wins over the inherited (chain or global) markup, and empty cache
// rates derive from the input price.
export const staticStepPreview = (step: DraftChainStep, inheritedMarkup: number): StaticStepPreview => {
  const input = parseOptional(step.staticInputPerM) ?? 0;
  const output = parseOptional(step.staticOutputPerM) ?? 0;
  const cacheRead = parseOptional(step.staticCacheReadPerM);
  const cacheWrite = parseOptional(step.staticCacheWritePerM);
  const stepMarkup = parseOptional(step.stepMarkup);
  const prices = [input, output, cacheRead ?? 0, cacheWrite ?? 0];
  if (prices.some((v) => !Number.isFinite(v) || v < 0)) return { ok: false, error: "Prices must be 0 or more." };
  if (!(input > 0 && output > 0)) return { ok: false, error: "Enter an input and output price above 0." };
  if (stepMarkup !== undefined && !(Number.isFinite(stepMarkup) && stepMarkup >= 0 && stepMarkup <= 1000)) {
    return { ok: false, error: "Markup must be between 0 and 1000." };
  }
  const markup = stepMarkup ?? inheritedMarkup;
  const mult = 1 + markup / 100;
  return {
    ok: true,
    markup,
    inputPerM: input * mult,
    outputPerM: output * mult,
    cacheReadPerM: (cacheRead ?? input * CACHE_READ_FACTOR) * mult,
    cacheWritePerM: (cacheWrite ?? input * CACHE_WRITE_FACTOR) * mult,
  };
};

// stepKey identifies a route step for duplicate checks. Two steps may share a
// provider/model when they are priced differently: different market slugs
// (surplus-fast then surplus-trusted) or a static price.
export const stepKey = (step: Pick<DraftChainStep, "provider" | "model" | "marketSlug"> & Partial<DraftChainStep>): string => {
  const base = `${step.provider}/${step.model}`;
  if (step.priceSource === "static") return `${base}|static:${(step.staticInputPerM ?? "").trim()}/${(step.staticOutputPerM ?? "").trim()}`;
  return `${base}|${(step.marketSlug ?? "").trim()}`;
};

// stepPayload builds the API step for the chosen price source only, so a
// value left over from the other source is never sent (the API rejects a
// step with both a slug and a static price).
export const stepPayload = (step: DraftChainStep): ChainStepPayload => {
  const base = { provider: step.provider, model: step.model };
  if (step.priceSource === "market") return { ...base, market_slug: step.marketSlug ?? "" };
  return {
    ...base,
    market_slug: "",
    static_input_per_m: parseOptional(step.staticInputPerM) ?? 0,
    static_output_per_m: parseOptional(step.staticOutputPerM) ?? 0,
    static_cache_read_per_m: parseOptional(step.staticCacheReadPerM) ?? null,
    static_cache_write_per_m: parseOptional(step.staticCacheWritePerM) ?? null,
    markup_percent: parseOptional(step.stepMarkup) ?? null,
  };
};

export const toDraftSteps = (chain?: Chain) =>
  chain?.steps.map((step) => makeDraftStep(step)) ?? [makeDraftStep()];

export const isValidChainName = (name: string) => /^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$/.test(name);
