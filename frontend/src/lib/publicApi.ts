// Typed client for the public landing API (/v1/public/*). Deliberately
// dependency-free and separate from lib/api.ts so the admin client never
// enters the public bundle. Responses are aggregate-only; see the Go handlers
// in backend/internal/gateway/public.go for the authoritative shape.

export interface PublicCapabilities {
  vision: boolean;
  pdf: boolean;
  audio_input: boolean;
  video_input: boolean;
  image_output: boolean;
  audio_output: boolean;
  search: boolean;
  tools: boolean;
  reasoning: boolean;
  structured_output: boolean;
  context_window: number;
  max_output: number;
}

export interface PublicUsage {
  users: number;
  requests: number;
  tokens: number;
}

export interface PublicModel {
  name: string;
  model_id: string;
  provider: string;
  provider_id: string;
  input_per_m: number;
  output_per_m: number;
  cached_per_m: number;
  cache_write_per_m: number;
  sold_out: boolean;
  /** Official list price per 1M tokens; null when no reference is known. */
  official_input_per_m?: number | null;
  official_output_per_m?: number | null;
  /** Whole-percent discount from the official price; null when none. For
   * live prices it is the 7-day billed average, null until there is enough
   * traffic. Not shown on the model cards. */
  discount_pct?: number | null;
  /** live: today's best market quote, a request may bill up to max_*_per_m.
   * fixed: a chain price that does not follow the market. Null when sold out. */
  price_basis?: "live" | "fixed" | null;
  /** Live quote's discount off the official price: the best case, which only
   * some requests get. Null when none. */
  discount_up_to_pct?: number | null;
  avg_input_per_m?: number | null;
  avg_output_per_m?: number | null;
  avg_window_days?: number | null;
  avg_sample_tokens?: number | null;
  max_input_per_m?: number | null;
  max_output_per_m?: number | null;
  /** Context window in tokens (models.dev); null when unknown. */
  context_tokens?: number | null;
  capabilities: PublicCapabilities;
  usage: PublicUsage;
}

export interface LandingNotification {
  id: string;
  tag?: string;
  title: string;
  body: string;
  href?: string;
}

export interface PublicOverview {
  total_requests: number;
  total_tokens: number;
  success: number;
  failed: number;
  model_count: number;
}

async function getJSON<T>(path: string): Promise<T> {
  const res = await fetch(path, { headers: { Accept: "application/json" } });
  if (!res.ok) throw new Error(`public api ${path}: ${res.status}`);
  return (await res.json()) as T;
}

export const fetchPublicOverview = () => getJSON<PublicOverview>("/v1/public/overview");

export const fetchPublicModels = () =>
  getJSON<{ models: PublicModel[] }>("/v1/public/models").then((d) => d.models);

export const fetchPublicNotifications = () =>
  getJSON<{ notifications: LandingNotification[] }>("/v1/public/notifications").then((d) => d.notifications);

export interface PublicBansos {
  exists: boolean;
  active: boolean;
  mode: "credit" | "unlimited";
  masked_display: string;
  allowed_models: string[];
  rpm: number;
  tpm: number;
  credit_remaining_usd: number | null;
  updated_at?: string;
}

export const fetchPublicBansos = () => getJSON<PublicBansos>("/v1/public/bansos");

export const fetchPublicBansosKey = () =>
  getJSON<{ key: string }>("/v1/public/bansos/key").then((d) => d.key);

export interface PublicHostingRegion {
  id: string;
  name: string;
  price_idr: number;
  /** Open slots; -1 = unlimited. */
  stock: number;
  sold_out: boolean;
}

export interface PublicHosting {
  /** Ordering is open (hosting and payments enabled). */
  enabled: boolean;
  period_days: number;
  terms_url: string;
  regions: PublicHostingRegion[];
}

export const fetchPublicHosting = () => getJSON<PublicHosting>("/v1/public/hosting");

export interface PublicModelDetailRoute {
  name: string;
  p50_latency_ms: number | null;
  p95_latency_ms: number | null;
  tokens_per_sec: number | null;
  success_pct: number | null;
  share_pct: number;
}

/** Market detail for one live-priced model. Prices are what a customer pays,
 * margin included. Null fields mean that source is unavailable right now. */
export interface PublicModelDetail {
  model: string;
  as_of: string | null;
  official_input_per_m: number | null;
  official_output_per_m: number | null;
  best: { input_per_m: number; output_per_m: number; discount_pct: number | null } | null;
  median_discount_pct: number | null;
  offer_count: number | null;
  requests_24h: number;
  performance: { as_of: string; routes: PublicModelDetailRoute[] } | null;
}

export const fetchPublicModelDetail = (model: string) =>
  getJSON<PublicModelDetail>("/v1/public/model-detail?model=" + encodeURIComponent(model));
