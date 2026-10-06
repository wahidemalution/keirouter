# Per-Step Market Slug Pricing — Design

Date: 2026-10-06
Status: draft (awaiting user review)

## Goal

Fix a structural loss: when a chain falls back to a step whose upstream costs
more than the chain's frozen aggregate price, the operator still bills the user
the old (lower) aggregate, so upstream cost exceeds charged revenue.

Introduce an explicit **market slug per chain step** so that:

1. At request time the chain orders its steps cheapest-first by the latest market
   snapshot.
2. The user is billed the **winning (actually served) step's slug price + markup**,
   not a stale chain aggregate.
3. If fallback lands on another slug, pricing follows the slug that actually served.

## Problem evidence (verified in the current code)

- `store.Chain.MarketSlugs []string` (`backend/internal/store/models.go:141`) is a
  chain-level slug list. `store.ChainStep` (`models.go:148`) has **no** slug field,
  so there is no relationship between a step (provider/model) and a market slug.
- `syncChainMarketPrices` (`backend/internal/app/market_sync.go:38`) fetches
  `market.Fetch`, computes **one** rate for the whole chain via
  `market.ComputeChainRate` (`backend/internal/market/pricing.go:15`) — cheapest
  input and cheapest output independently across all bound slugs — then multiplies
  by `1 + MarkupPercent` and writes it to the chain row.
- `TargetsFromChain` (`backend/internal/dispatch/dispatch.go:1029`) copies that
  single chain rate onto **every** fallback step.
- `pipeline.recordOutcomeWithTTFT` (`backend/internal/pipeline/pipeline.go:1672`)
  passes the target's rates into `meter.Event`; `meter.chainPrice`
  (`backend/internal/meter/pricing.go:265`) treats any nonzero rate as authoritative
  and `meter.Record` (`meter/meter.go:213`) uses it **instead of** the per-model
  catalog price.
- Consequence: a fallback to a more expensive slug is still billed at the chain
  aggregate. Market price movements are only reflected after the next DB sync.

## Scope

In scope:

- New `market_slug` field on each chain step (schema + repo + admin API + UI).
- Latest market snapshot held in memory, refreshed by the existing
  `runMarketSync` loop.
- Per-request cheapest-first ordering of chain steps using live per-slug rates.
- Winning-step pricing at record time, with provenance.
- Per-chain opt-out to keep current behavior.

Out of scope (explicitly, YAGNI):

- Reintroducing the dropped `model_market_bindings` per-model table
  (see `migrations/0041_drop_model_market_bindings.*.sql`). We build on the
  current per-chain model.
- Automatic slug inference from provider alias (rejected: `cbcn` and `cp` are not
  catalog providers, and `codebuddy` maps to two prefixes `cb`/`cbcn`).
- Changing the market API contract.

## Design

### 1. Data model

Add one column to `chain_steps`:

```
ALTER TABLE chain_steps ADD COLUMN market_slug TEXT NOT NULL DEFAULT '';
```

New migrations `0045_chain_step_market_slug.sqlite.sql` and
`0045_chain_step_market_slug.postgres.sql` (next free number after `0044`).
The `chain_steps` table is embedded via `//go:embed migrations/*.sql`
(`store/migrate.go:13`) and applied in filename order; `migrationApplies` gates
the sqlite/postgres variant.

`store.ChainStep` gains:

```go
MarketSlug string
```

`ChainRepo.Create`, `ChainRepo.Update`, `ChainRepo.steps`
(`backend/internal/store/repo_budgets.go:296,418,430`) must select/insert the new
column. `ChainRepo.UpdateRates` stays unchanged (it only writes the chain-level
rate columns).

Note: `migrations/0031_chain_step_pricing` already added unused per-step
`input_per_m`/`output_per_m`/`cache_*` columns. We deliberately do **not** use
them: the value we need is a *slug reference*, and rates must remain live. Leaving
those columns untouched preserves the current insert/select shape except for the
one new column.

### 2. Live market snapshot cache

`market` package gains an in-memory, concurrency-safe snapshot store:

```go
// market.SnapshotCache
func NewSnapshotCache() *SnapshotCache
func (c *SnapshotCache) Replace(models []Model)
func (c *SnapshotCache) Rate(slug string) (Rate, bool) // minAskIn/minAskOut, ok
func (c *SnapshotCache) Age() time.Duration
```

- `Rate` returns the raw market ask (no markup). Markup is applied at billing time.
- `runMarketSync` (`app/market_sync.go:61`) already fetches on interval; it will
  additionally call `cache.Replace(models)` on every successful fetch, even when
  `AutoRefresh` is off, by doing one fetch at startup and on each manual refresh.

Resolved: extend `runMarketSync` to always fetch on its 1s ticker subject to
`RefreshIntervalSeconds`, regardless of `AutoRefresh`, so the cache stays warm;
`AutoRefresh` continues to gate only the DB chain-rate writes. This keeps a single
fetch path (see Decisions).

### 3. Per-request cheapest-first ordering

New pure function in `dispatch`:

```go
// OrderStepsByMarketRate returns steps ordered cheapest-first by live market
// rate (input+output). Steps without a bound slug keep their original relative
// order and are placed after all priced steps.
func OrderStepsByMarketRate(steps []store.ChainStep, rateOf func(slug string) (market.Rate, bool)) []store.ChainStep
```

- Called from the chain→targets conversion, keeping `TargetsFromChain` as the
  single flatten point. The chain's stored step order is never modified in the DB.
- Ties break by original position (stable).
- If the cache is empty or no step carries a slug, the original order is returned.

Chain gains a flag to opt out:

```go
// store.Chain
ReorderByMarket bool // default false
```

Only chains explicitly enabled reorder; otherwise current order is preserved.
This is additive and non-breaking.

### 4. Step→target carries its slug and price

`dispatch.Target` gains:

```go
MarketSlug string
```

`TargetsFromChain` sets `MarketSlug: s.MarketSlug` (when reordering/ordering, the
ordered slice is used).

At record time the pipeline must resolve the winner's billable rate.

- `Target` carries the resolved `Rate` and `MarketSlug` at ordering time (from the
  same snapshot used to order), so the rate billed equals the rate that drove
  routing for that request. This avoids a second cache lookup that could see a
  newer snapshot mid-request.

Pipeline builds the meter event with a new optional slug block:

```go
// meter.Event additions
MarketSlug     string
MarketRateIn   float64
MarketRateOut  float64
```

Markup is applied in the pipeline (see Decisions): it reads
`market.Settings.MarkupPercent`, computes the final billable rates, and sets
`MarketRateIn`/`MarketRateOut` to those final values. The meter therefore stores
the billable rate directly and does not import the market package.

`meter.Record` precedence becomes:

1. If `MarketSlug != ""` and a positive market rate is present:
   `price = {InputPerM: MarketRateIn, OutputPerM: MarketRateOut,
   CachedInputPerM: MarketRateIn*cacheReadMult, CacheWritePerM: MarketRateIn*cacheWriteMult,
   Source: "market_slug"}`. Provenance: `PricingKey = MarketSlug`,
   `PricingMatchKind = "market_slug"`.
2. Else if chain-level rates present: current `chainPrice` behavior.
3. Else: per-model catalog `ResolvePrice` (unchanged).

### 5. Markup consistency

Markup is read from `market.Settings.MarkupPercent` (default 10,
`market/settings.go:20`) each time rates are applied, so a changed markup takes
effect on the next request without re-syncing the DB.

### 6. Admin API

`adminCreateChain` (`gateway/admin.go:1881`) and `adminUpdateChain`
(`admin.go:1982`) step payloads gain `market_slug`:

```go
type step struct {
    Provider   string `json:"provider"`
    Model      string `json:"model"`
    MarketSlug string `json:"market_slug"`
}
```

Validation: `market_slug` is optional; if set it must be non-empty after trim. No
existence check against the live snapshot (slugs may not be in the current
snapshot; unbound/missing stays safe by falling back to existing behavior).

`adminListChains`/get already serialize steps; add `market_slug` to the response
shape.

### 7. Frontend

Chain editor step rows gain a "Market slug" text input bound to `market_slug`,
plus a "Reorder by market price" toggle bound to the new chain flag. Locate the
chain editor component during implementation (search for `market_slugs` consumers
under `frontend/src`).

## Error handling

- Empty/missing market slug on a step: step keeps original order and is billed by
  existing rules (chain rate or catalog). Never zero, never free.
- Market cache empty/stale: ordering falls back to original order; billing falls
  back to existing rules. No behavior change if the feature is unconfigured.
- Non-finite/invalid snapshot values: reuse `market.validRate` semantics; skip
  invalid slugs.
- Markup < 0: already clamped by `LoadSettings`.

## Testing

- `OrderStepsByMarketRate`: cheapest-first, stable ties, unpriced steps last, empty
  cache → identity, single-step, all-unpriced.
- `market.SnapshotCache`: replace, lookup hit/miss, concurrent read during replace
  (`-race`).
- meter precedence: market-slug rate wins over chain rate; missing slug falls back
  to chain rate; provenance fields (`PricingKey`, `PricingMatchKind`, `Source`).
- store round-trip: create/get/update chain with per-step `market_slug`
  (sqlite + existing postgres integration test).
- handler: create/update accept and persist `market_slug`; an explicit `""` clears
  the binding, whitespace is trimmed, and no other value is rejected.
- Build: `go test ./...`, `go vet ./...`, `npx tsc -b && npx vite build`.

## Migration / compatibility

- New column defaults `''`; existing chains keep current behavior (no slug, no
  reorder).
- Historical `usage_records` stay immutable (they snapshot pricing already).
- Rollback: dropping the feature leaves the nullable column unused; no data loss.

## Decisions (resolved)

1. **Markup applied in the pipeline.** The pipeline reads
   `market.Settings.MarkupPercent`, computes `rate * (1 + markup/100)`, and passes
   the final billable rate to the meter. The meter stays unaware of market
   settings and does not import the market package (no dependency cycle).
2. **Cache always warmed.** `runMarketSync` warms `SnapshotCache` on its existing
   ticker regardless of `AutoRefresh`; `AutoRefresh` gates only the DB chain-rate
   writes. One fetch path for both.
3. **`ReorderByMarket` defaults to OFF (opt-in).** Chains that already declare
   `market_slugs` keep today's behavior until the operator enables the flag
   per chain.
4. **Rates carried on `Target`.** The rate used for routing is the same rate
   stamped for billing; no second cache lookup during record.

## Acceptance criteria

- A chain with per-step slugs reorders cheapest-first and bills the winning slug's
  `minAsk*(1+markup%)`.
- Fallback to a pricier slug charges that pricier slug, eliminating the loss.
- Chains without step slugs behave exactly as today.
- `go test ./...`, `go vet ./...`, and the frontend build all pass.
