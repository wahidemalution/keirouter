import { ListOrdered, Repeat2, type LucideIcon } from "lucide-react";
import type { Chain, ChainFallbackPrice, ChainFallbackPricePayload, ChainStep, ChainStepPayload, Provider } from "../../lib/api";
// Explicit extension: the node:test runner (npm test) resolves ESM literally.
import { ICONS } from "../../lib/icons.ts";

export const CHAIN_MODEL_KIND = "llm";

export type ChainStrategy = "priority" | "round_robin" | "latency" | "cost";

export const CACHE_WRITE_FACTOR = 1.25;
export const CACHE_READ_FACTOR = 0.1;

export type StepPriceSource = "market" | "static" | "discount";

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
  // A discount step reuses the static fields as the models.dev list price.
  // discountPercent is what the customer gets off it; costMultiplier is the
  // operator's cost as a fraction of list ("" = cost equals customer price).
  discountPercent: string;
  costMultiplier: string;
  // displayName is the provider name the portal shows; "" keeps the default.
  displayName: string;
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

const draftPriceSource = (step?: Partial<ChainStep>): StepPriceSource => {
  if (!((step?.static_input_per_m ?? 0) > 0 || (step?.static_output_per_m ?? 0) > 0)) return "market";
  return step?.discount_percent === undefined || step?.discount_percent === null ? "static" : "discount";
};

export const makeDraftStep = (step?: Partial<ChainStep> & { provider: string; model: string }): DraftChainStep => ({
  id: crypto.randomUUID(),
  provider: step?.provider ?? "",
  model: step?.model ?? "",
  marketSlug: step?.market_slug ?? "",
  inputPerM: 0,
  outputPerM: 0,
  cacheWritePerM: 0,
  cacheReadPerM: 0,
  priceSource: draftPriceSource(step),
  staticInputPerM: step?.static_input_per_m ? String(step.static_input_per_m) : "",
  staticOutputPerM: step?.static_output_per_m ? String(step.static_output_per_m) : "",
  staticCacheReadPerM: numText(step?.static_cache_read_per_m),
  staticCacheWritePerM: numText(step?.static_cache_write_per_m),
  stepMarkup: numText(step?.markup_percent),
  discountPercent: numText(step?.discount_percent),
  costMultiplier: numText(step?.cost_multiplier),
  displayName: step?.display_name ?? "",
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
  // The gateway bills a zero cache rate at the input rate, so 0 is refused
  // rather than previewed as free. Empty derives from the input price.
  if (cacheRead === 0 || cacheWrite === 0) return { ok: false, error: "Cache prices must be above 0, or leave them empty." };
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

export interface StepRates {
  inputPerM: number;
  outputPerM: number;
  cacheReadPerM: number;
  cacheWritePerM: number;
}

export type DiscountStepPreview =
  | { ok: true; discount: number; customer: StepRates; cost: StepRates; loss: boolean }
  | { ok: false; error: string };

// discountStepPreview computes a discount step's customer price and operator
// cost per 1M tokens. It mirrors the gateway's priceStaticSteps exactly: the
// customer pays list × (100 − discount) / 100 with no markup, the cost is
// list × multiplier (customer price when empty), and empty cache prices derive
// from the list input.
export const discountStepPreview = (step: DraftChainStep): DiscountStepPreview => {
  const input = parseOptional(step.staticInputPerM) ?? 0;
  const output = parseOptional(step.staticOutputPerM) ?? 0;
  const cacheRead = parseOptional(step.staticCacheReadPerM);
  const cacheWrite = parseOptional(step.staticCacheWritePerM);
  const discount = parseOptional(step.discountPercent);
  const multiplier = parseOptional(step.costMultiplier);
  if ([input, output, cacheRead ?? 0, cacheWrite ?? 0].some((v) => !Number.isFinite(v) || v < 0)) return { ok: false, error: "Prices must be 0 or more." };
  if (!(input > 0 && output > 0)) return { ok: false, error: "Enter an input and output price above 0." };
  if (cacheRead === 0 || cacheWrite === 0) return { ok: false, error: "Cache prices must be above 0, or leave them empty." };
  if (discount === undefined || !(Number.isFinite(discount) && discount >= 0 && discount < 100)) {
    return { ok: false, error: "Enter a discount from 0 to below 100." };
  }
  if (multiplier !== undefined && !(Number.isFinite(multiplier) && multiplier > 0 && multiplier <= 100)) {
    return { ok: false, error: "Cost multiplier must be above 0 and at most 100." };
  }
  // (100 − d) / 100 keeps 90 at exactly 0.1, as the gateway computes it.
  const bill = (100 - discount) / 100;
  const cost = multiplier ?? bill;
  const list: StepRates = {
    inputPerM: input,
    outputPerM: output,
    cacheReadPerM: cacheRead ?? input * CACHE_READ_FACTOR,
    cacheWritePerM: cacheWrite ?? input * CACHE_WRITE_FACTOR,
  };
  const scale = (f: number): StepRates => ({
    inputPerM: list.inputPerM * f,
    outputPerM: list.outputPerM * f,
    cacheReadPerM: list.cacheReadPerM * f,
    cacheWritePerM: list.cacheWritePerM * f,
  });
  return { ok: true, discount, customer: scale(bill), cost: scale(cost), loss: cost > bill };
};

// stepKey identifies a route step for duplicate checks. It mirrors the
// dispatcher's attempt key (provider, model, market slug): two steps may share
// a provider/model only when their market slugs differ (surplus-fast then
// surplus-trusted). A static step sends no slug, so it collides with a
// slugless or another static step on the same provider/model.
export const stepKey = (step: Pick<DraftChainStep, "provider" | "model" | "marketSlug"> & Partial<DraftChainStep>): string => {
  const slug = step.priceSource === "static" || step.priceSource === "discount" ? "" : (step.marketSlug ?? "").trim();
  return `${step.provider}/${step.model}|${slug}`;
};

// stepPayload builds the API step for the chosen price source only, so a
// value left over from the other source is never sent (the API rejects a
// step with both a slug and a static price).
export const stepPayload = (step: DraftChainStep): ChainStepPayload => {
  const base = { provider: step.provider, model: step.model, display_name: step.displayName.trim() };
  if (step.priceSource === "market") return { ...base, market_slug: step.marketSlug ?? "" };
  const prices = {
    ...base,
    market_slug: "",
    static_input_per_m: parseOptional(step.staticInputPerM) ?? 0,
    static_output_per_m: parseOptional(step.staticOutputPerM) ?? 0,
    static_cache_read_per_m: parseOptional(step.staticCacheReadPerM) ?? null,
    static_cache_write_per_m: parseOptional(step.staticCacheWritePerM) ?? null,
  };
  if (step.priceSource === "static") return { ...prices, markup_percent: parseOptional(step.stepMarkup) ?? null };
  // Markup never applies to a discount step, so a leftover one is not sent.
  return {
    ...prices,
    markup_percent: null,
    discount_percent: parseOptional(step.discountPercent) ?? null,
    cost_multiplier: parseOptional(step.costMultiplier) ?? null,
  };
};

export const toDraftSteps = (chain?: Chain) =>
  chain?.steps.map((step) => makeDraftStep(step)) ?? [makeDraftStep()];

export const isValidChainName = (name: string) => /^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$/.test(name);

// surplusProviderOf returns the Surplus provider a keirouter provider is
// pinned to, by the naming convention alias "surplus-<provider>" on a Surplus
// base URL; "" for any other provider, the unpinned "surplus" one included.
export const surplusProviderOf = (provider?: Pick<Provider, "alias" | "base_url">): string => {
  if (!provider?.base_url?.includes("surplusintelligence.ai")) return "";
  const alias = provider.alias.trim().toLowerCase();
  return alias.startsWith("surplus-") ? alias.slice("surplus-".length) : "";
};

// SurplusLabels maps a Surplus provider id (bedrock) to the name Surplus
// shows for it (Amazon Bedrock), as read from a model's order book.
export type SurplusLabels = Record<string, string>;

const squash = (s: string) => s.toLowerCase().replace(/[^a-z0-9]/g, "");

// resolveSurplusProvider finds the Surplus provider id and name a pinned
// keirouter provider stands for. The alias suffix is tried as an id first,
// then the alias and display name are compared with each Surplus id and name
// (surplus-amazon-bedrock and "Surplus Amazon Bedrock" both match bedrock =
// Amazon Bedrock). Without labels, or with no single match, the alias suffix
// is kept as the id and the name is "".
export const resolveSurplusProvider = (
  provider: Pick<Provider, "alias" | "base_url"> & Partial<Pick<Provider, "display_name">> | undefined,
  labels?: SurplusLabels,
): { id: string; name: string } => {
  const alias = surplusProviderOf(provider);
  if (!alias || !labels) return { id: alias, name: "" };
  if (labels[alias]) return { id: alias, name: labels[alias] };
  const wants = [squash(alias), squash((provider?.display_name ?? "").replace(/^\s*surplus\b/i, ""))].filter(Boolean);
  const ids = Object.keys(labels);
  const unique = (found: string[]) => (found.length === 1 ? { id: found[0], name: labels[found[0]] } : null);
  const exact = unique(ids.filter((id) => wants.includes(squash(id)) || wants.includes(squash(labels[id]))));
  if (exact) return exact;
  // Partial match, e.g. amazon-bedrock holds bedrock. Short ids are skipped so
  // "0g" or "ai" never match by accident.
  const partial = unique(ids.filter((id) => {
    const sid = squash(id);
    const sname = squash(labels[id]);
    return wants.some((w) => (sid.length >= 4 && w.includes(sid)) || (w.length >= 4 && sname.includes(w)));
  }));
  return partial ?? { id: alias, name: "" };
};

// autoMarketSlug is the slug a step on a pinned Surplus provider routes by:
// surplus-trusted@<provider>:<model>; "" when it has none. With labels the
// provider part is the id Surplus uses, not the alias suffix.
export const autoMarketSlug = (
  provider: Pick<Provider, "alias" | "base_url"> & Partial<Pick<Provider, "display_name">> | undefined,
  model: string,
  labels?: SurplusLabels,
): string => {
  const surplus = resolveSurplusProvider(provider, labels).id;
  return surplus && model.trim() ? `surplus-trusted@${surplus}:${model.trim()}` : "";
};

// FallbackPriceDraft is the editor state of a chain's final fallback price.
export type FallbackPriceDraft = { inputPerM: string; outputPerM: string; cacheReadPerM: string; cacheWritePerM: string; markup: string };

export const EMPTY_FALLBACK_PRICE: FallbackPriceDraft = { inputPerM: "", outputPerM: "", cacheReadPerM: "", cacheWritePerM: "", markup: "" };

const draftNumber = (v: number | null | undefined) => (v === null || v === undefined || v === 0 ? "" : String(v));

export const fallbackPriceDraft = (price: ChainFallbackPrice | null | undefined): FallbackPriceDraft =>
  price
    ? {
        inputPerM: draftNumber(price.input_per_m),
        outputPerM: draftNumber(price.output_per_m),
        cacheReadPerM: draftNumber(price.cache_read_per_m),
        cacheWritePerM: draftNumber(price.cache_write_per_m),
        markup: price.markup_percent === null || price.markup_percent === undefined ? "" : String(price.markup_percent),
      }
    : EMPTY_FALLBACK_PRICE;

export type FallbackPricePreview =
  | { ok: true; source: "manual" | "models.dev"; markup: number; inputPerM: number; outputPerM: number; cacheReadPerM: number; cacheWritePerM: number }
  | { ok: true; source: "none" }
  | { ok: false; error: string };

// fallbackPricePreview mirrors the gateway's priceChainFallback: the stored
// price, else the models.dev price, plus only the fallback's own markup. An
// empty cache read rate bills the full input price.
export const fallbackPricePreview = (draft: FallbackPriceDraft, auto: { input_per_m: number; output_per_m: number } | null): FallbackPricePreview => {
  const input = parseOptional(draft.inputPerM);
  const output = parseOptional(draft.outputPerM);
  const cacheRead = parseOptional(draft.cacheReadPerM);
  const cacheWrite = parseOptional(draft.cacheWritePerM);
  const markup = parseOptional(draft.markup) ?? 0;
  if ([input ?? 0, output ?? 0, cacheRead ?? 0, cacheWrite ?? 0].some((v) => !Number.isFinite(v) || v < 0)) return { ok: false, error: "Prices must be 0 or more." };
  if (!(Number.isFinite(markup) && markup >= 0 && markup <= 1000)) return { ok: false, error: "Markup must be between 0 and 1000." };
  if (cacheRead === 0 || cacheWrite === 0) return { ok: false, error: "Cache prices must be above 0, or leave them empty." };
  const manual = (input ?? 0) > 0 || (output ?? 0) > 0;
  if (manual && !((input ?? 0) > 0 && (output ?? 0) > 0)) return { ok: false, error: "Enter both an input and output price, or leave both empty." };
  const base = manual ? { input: input as number, output: output as number } : auto ? { input: auto.input_per_m, output: auto.output_per_m } : null;
  if (!base) return { ok: true, source: "none" };
  const mult = 1 + markup / 100;
  return {
    ok: true,
    source: manual ? "manual" : "models.dev",
    markup,
    inputPerM: base.input * mult,
    outputPerM: base.output * mult,
    cacheReadPerM: (cacheRead ?? base.input) * mult,
    cacheWritePerM: (cacheWrite ?? base.input * CACHE_WRITE_FACTOR) * mult,
  };
};

// fallbackPricePayload is the API body for the fallback price.
export const fallbackPricePayload = (draft: FallbackPriceDraft): ChainFallbackPricePayload => ({
  input_per_m: parseOptional(draft.inputPerM) ?? 0,
  output_per_m: parseOptional(draft.outputPerM) ?? 0,
  cache_read_per_m: parseOptional(draft.cacheReadPerM) ?? null,
  cache_write_per_m: parseOptional(draft.cacheWritePerM) ?? null,
  markup_percent: parseOptional(draft.markup) ?? null,
});
