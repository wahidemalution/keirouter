# Public Pricing for Every Chain Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `GET /v1/public/pricing` prices every admin-published chain from any upstream, with per-provider price levels, an honest maximum, and no internal data.

**Architecture:** A new builder in `backend/internal/gateway/public_price.go` prices each chain route by route, stamping every step exactly as request resolution does (`priceMarketSteps`, `priceStaticSteps`, `stampSurplusTrustedWith`, `stampCostLookup`, `priceChainFallback`), so displayed prices and billing share one code path. The public handler and the admin tab switch to it; `/v1/pricing` (per key) is untouched.

**Tech Stack:** Go (chi, testify, sqlite in-memory fixtures), React + TypeScript admin tab.

**Spec:** `docs/superpowers/specs/2026-11-26-public-pricing-all-chains-design.md`

## Global Constraints

- Go commands: `export PATH=/home/whd/sdk/go/bin:$PATH GOMAXPROCS=2 GOMEMLIMIT=1500MiB GOFLAGS=-p=1`, one heavy command at a time, no `-race`.
- All prices: customer price, markup included, USD per 1M tokens; unknown = `null`, never `0`.
- Envelope unchanged: `{"currency":"USD","unit":"per_1m_tokens","as_of":…,"models":[…]}`.
- Discount: `discountPercent` from `gateway/official_price.go` (floor), everywhere in the response.
- Never serialize: provider IDs, account IDs, market slugs, Surplus provider IDs, markup percent, upstream cost.
- `/v1/pricing` and its tests stay as they are.

## Review Focus

1. A chain whose only route is a cost-lookup step (Subrouter/a6api): `price_type` `variable`, `max_guaranteed` false, `min_request_cost` 0.000002 × bill multiplier.
2. A chain mixing a fixed step and a Surplus trusted step: `max_*` is the per-field max of both, `max_guaranteed` false.
3. A chain with no priced route at all (catalog-only step, no chain price): `max_*` null, `available` reflects quotes, no `0` prices.
4. Steps without `DisplayName` and without a Surplus label: names are `Provider 1`, `Provider 2`, never `custom-openai-…`.
5. Unpublished, disabled, hidden or maintenance chain named in `?model=`: identical answer to a nonexistent name.

## Rulings carried from planning

- Ruling: provider `name` = step `DisplayName`, else the label Surplus publishes for a `surplus-trusted@<provider>` step (`marketCache.ProviderLabel`, public data, matches the portal screenshot), else `Provider N`. Never the custom-provider display name or ID. Spec said DisplayName or generic only; production steps have no DisplayName, which would turn every Surplus route into "Provider N" against the owner's expected output. Cost if wrong: a Surplus label shows publicly (it is already public on Surplus).
- Ruling: CORS is a response header `Access-Control-Allow-Origin: *` on GET only. A plain GET without custom headers needs no preflight, so the global `go-chi/cors` middleware is left alone. Cost if wrong: a browser client sending custom headers is blocked by preflight.

---

### Task 1: Route pricing and the public model builder

**Files:**
- Create: `backend/internal/gateway/public_price.go`
- Test: `backend/internal/gateway/public_price_test.go`

**Interfaces:**
- Produces:
  - `type publicPriceLevel struct { InputPerM, OutputPerM float64; Offers int; DiscountPct *int; Tag string \`json:"tag,omitempty"\` }` (json: `input_per_m`, `output_per_m`, `offers`, `discount_pct`, `tag`).
  - `type publicProvider struct { Name string; Available bool; InputPerM, OutputPerM, CachedPerM *float64; DiscountPct *int; LatencyP50MS *float64; FinalFallback bool \`json:"final_fallback,omitempty"\`; Prices []publicPriceLevel }` (json: `name`, `available`, `input_per_m`, `output_per_m`, `cached_input_per_m`, `discount_pct`, `latency_p50_ms`, `final_fallback`, `prices`).
  - `type publicModelPrice struct` with json fields exactly: `model, available, price_type, input_per_m, output_per_m, cached_input_per_m, cache_write_per_m, max_input_per_m, max_output_per_m, max_guaranteed, min_request_cost, official_input_per_m, official_output_per_m, discount_pct, avg_input_per_m, avg_output_per_m, avg_window_days, avg_sample_tokens, providers`. No embedded structs.
  - `type routeBound struct { In, Out float64; Known, Guaranteed bool; MinChargeUSD float64 }`
  - `func (s *Server) stampRoute(c store.Chain, t dispatch.Target, markup float64) (dispatch.Target, bool)`: runs the same stamping as `stepCustomerQuote` plus `stampCostLookup`; false when the step drops out (sold out).
  - `func boundOf(t dispatch.Target) routeBound`: max rate and guarantee of one stamped route:
    - `IsUpstreamCostSlug(t.MarketSlug)` (trusted/fast): `MarketRateIn/Out`, Guaranteed false;
    - `t.BillUpstreamCost` (inferhub): `MarketRateIn/Out` (bid cap × mult), Guaranteed false;
    - `t.CostLookup`: `MarketRateIn/Out`, Guaranteed false;
    - other market or static stamp (`MarketRateIn>0 || MarketRateOut>0`): that, Guaranteed true;
    - chain price (`InputPerM>0 || OutputPerM>0`): that, Guaranteed true;
    - nothing: Known false.
    - `MinChargeUSD = t.MinChargeMicro / 1e6 × bill multiplier` (`UpstreamCostMult` for inferhub, `staticBillMult` for static steps, 1 otherwise).
  - `func (s *Server) publicChainPrice(c store.Chain, markup float64, latency map[string]map[string]*float64) publicModelPrice`
- Consumes: `stepCustomerQuote`, `discountPercent`, `chainOfficialPrice`, `chainFallbackTarget`, `priceChainFallback`, `applyRealized`-equivalent data (`realizedFrom`), `stepDisplayName`, `marketCache.ProviderLabel`.

- [ ] **Step 1: Write the failing tests** in `public_price_test.go`, using a fixture modelled on `pricingFixture` (`pricing_test.go:27`) with chains:
  - `fixed`: chain price 2/8, step `openai/gpt-4o` → `price_type` "fixed", `max_*` 2/8, `max_guaranteed` true, one provider, one level, `min_request_cost` null.
  - `glm-trusted` (markup 25, official 1/4, quote BestIn 0.2 BestOut 0.8 CapIn 0.4 CapOut 1.6) → `input_per_m` 0.25, `output_per_m` 1.0, `max_*` 0.5/2.0, `max_guaranteed` false, `price_type` "variable", `discount_pct` = `discountPercent(0.25, 1.0, official)`.
  - `sub`: one static step on a cost-lookup provider (pick the provider ID for which `connectors.CostLookupEnabled` is true in tests), static 1/4, chain markup 100 → `max_*` 2/8, `max_guaranteed` false, `price_type` "variable", `min_request_cost` 0.000004.
  - `mixed` (chain markup 0): static step 3/3 then the trusted glm step (cap 0.4/1.6) → `max_input_per_m` 3, `max_output_per_m` 3, `max_guaranteed` false, providers in declared order.
  - `bare`: catalog-only step, no chain price → `max_input_per_m` null, `max_output_per_m` null, no `0` anywhere in the provider prices.
  - fallback: `fixed` with `FallbackProvider/Model` + fallback price 10/30 → last provider has `final_fallback` true, headline still 2/8, `max_*` 10/30.
  - names: a step with `DisplayName` "Anthropic" shows "Anthropic"; a second unnamed non-Surplus step shows "Provider 2".
  - levels: a trusted quote with offers (0.2/0.8)×1, (0.3/1.2)×2 identical → `prices` has 2 levels, second `offers` 2, first `tag` "cheapest", last `tag` "highest".
- [ ] **Step 2: Run, expect FAIL** (`undefined: publicChainPrice`):
  `cd backend && go test ./internal/gateway/ -run 'TestPublicChainPrice' -count=1`
- [ ] **Step 3: Implement** `public_price.go` per Interfaces. Group steps by `t.Provider` as `modelSettingsRow` does; a provider's headline is its first step with a quote; `prices` from `stepQuote.offers` merged by identical (in, out) pairs, or one level from the provider headline when no offers. Model `max_*` = per-field max of every route's `boundOf` including the priced fallback; any route with `Known` false makes both `null`. `max_guaranteed` = every route Guaranteed. `min_request_cost` = max `MinChargeUSD`, null when 0. `price_type` "fixed" iff every route Guaranteed and not `CostLookup`. Headline and cache rates from the first available non-fallback route (`stepQuote.cacheRead/cacheWrite`).
- [ ] **Step 4: Run, expect PASS** (same command).
- [ ] **Step 5: Commit** `feat(pricing): price every chain route for the public API`.

### Task 2: Serve it publicly and in the admin tab

**Files:**
- Modify: `backend/internal/gateway/public_pricing.go`
- Modify: `backend/internal/gateway/server.go` (struct field for the new 2s cache, if not kept inside public_price.go)
- Test: `backend/internal/gateway/public_pricing_test.go`

**Interfaces:**
- Consumes: `publicChainPrice` (Task 1).
- Produces: `func (s *Server) allChainPrices(ctx context.Context) (publicPriceList, error)` — one-entry cache, TTL `pricingCacheTTL`, `publicPriceList{byName map[string]publicModelPrice; idToName map[string]string; unlisted map[string]bool; asOf time.Time}`; includes every chain with steps, `unlisted` for `!c.Listed()`; disabled/maintenance chains priced `available:false` and treated as unlisted.

- [ ] **Step 1: Write failing tests** (update `TestPublicPricingOnlyPublishedChains`):
  - admin GET lists all fixture chains (fixed included); PUT accepts the `fixed` chain ID; PUT of an unknown ID → 400 `unknown chain: <id>`.
  - public GET after publishing `fixed` and `glm-trusted`: 2 models, `providers` present, header `Cache-Control: public, max-age=5`, `Access-Control-Allow-Origin: *`.
  - `?model=hidden-chain` (published but `!Listed()`) and `?model=nope` give the same status and body.
  - leak test: body contains none of `custom-openai`, `surplus-trusted:`, `"markup`, step IDs `s1`/`s2`/`s3`, the chain IDs.
  - `TestPublicPricingLimitsPerIP` keeps passing unchanged.
- [ ] **Step 2: Run, expect FAIL:** `go test ./internal/gateway/ -run 'TestPublicPricing' -count=1`
- [ ] **Step 3: Implement:** `publicPricing` uses `allChainPrices`; a `writePublicPriceList` mirroring `writePriceList`'s selection/404 rules but with the new type and the two headers; `adminGetPublicPricing`/`adminUpdatePublicPricing` use `allChainPrices` (admin model rows: `chain_id`, `published`, plus the public fields). Update comments that say "Surplus".
- [ ] **Step 4: Run, expect PASS**, then the whole gateway package: `go test ./internal/gateway/ -count=1` — only the 4 known pre-existing failures (`TestE2E_Budget_RefusesPromptLargerThanBalance`, `TestPortalModelSettingsList`, `TestPortalModelSettingsFallbackRuns`, `TestResolveTargetsFallbackTargetCarriesChainPrice`) may fail.
- [ ] **Step 5: Commit** `feat(pricing): public pricing covers every published chain`.

### Task 3: Admin tab and docs

**Files:**
- Modify: `frontend/src/lib/api.ts` (`PublicPricingModel`: add `price_type`, `max_guaranteed`)
- Modify: `frontend/src/pages/settings/PublicPricingTab.tsx` (copy says any chain; show "fixed"/"variable" and "max not guaranteed" next to the max)
- Modify: `skills/keirouter-chat/SKILL.md` or the public pricing doc section in `README.md`: document the new fields, `max_guaranteed`, `min_request_cost`, discount meaning change.

- [ ] **Step 1:** Update the type and tab; run `cd frontend && npx tsc --noEmit -p .` (alone, nothing else running). Expected: no errors.
- [ ] **Step 2:** Run the tab's existing tests if any (`npx vitest run src/pages/settings` if present). Expected: pass.
- [ ] **Step 3:** Docs edit (follow antislop-copywriting).
- [ ] **Step 4: Commit** `feat(admin): publish any chain on public pricing; document fields`.
