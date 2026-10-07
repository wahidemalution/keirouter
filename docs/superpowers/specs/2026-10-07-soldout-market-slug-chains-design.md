# Soldout Detection for Market-Slug Chains — Design

## Problem

A chain can bind `market_slugs` that are absent from the live market feed
(e.g. `claude-fable-5.1` → `cc/claude-fable-5-1`, currently sold out). Today:

1. `ComputeChainRate` returns `ok=false` when no bound slug is present
   (`market/pricing.go:43`), so `syncChainMarketPricesWithModels` **skips**
   the chain and leaves `chains.input_per_m = 0` (`app/market_sync.go:49`).
2. `publicModelRows` sees an all-zero chain rate and **falls back to the
   static catalog price** via `connectors.ModelPriceByProviderModel`
   (`gateway/public.go:90-94`), which is always > 0.

Net effect: the landing page shows a price for a model that cannot be
served, so users read it as a bug instead of "sold out".

## Decisions (from brainstorming)

- **Partial availability is available.** A chain is sold out only when
  *none* of its bound `market_slugs` is present in the feed. If at least one
  slug resolves, the chain is available and priced from the available slugs
  (unchanged `ComputeChainRate` behavior).
- **Chains without `market_slugs` are never sold out.** They keep the
  existing catalog/manual price display.
- **Sold-out UI:** the card still renders, shows a **"Sold out"** badge, and
  **hides** the price block.
- **Detection source:** the live in-memory `market.SnapshotCache`
  (`Server.marketCache`), not the DB — because sync deliberately does not
  write when slugs vanish, so the DB cannot distinguish "sold out" from
  "never synced".

## Rule

For a chain `c`:

```
sold_out(c) := len(c.MarketSlugs) > 0
            && !anySlugResolves(c.MarketSlugs, marketCache)
```

where `anySlugResolves` returns true if any slug yields a valid `Rate`
(`SnapshotCache.Rate(slug)` returns `ok`). A chain with no `market_slugs`
is never sold out.

## Changes

### Backend — `internal/gateway/public.go`

- Add `SoldOut bool` to `publicModelRow`.
- In `publicModelRows`, after obtaining `chains`:
  - Collect the resolved-availability of each chain's `market_slugs` from
    `s.marketCache`. Add a helper that, given `[]string`, returns whether any
    slug resolves.
  - Pricing precedence becomes:
    1. `len(MarketSlugs) > 0` and chain rate > 0 → use chain rate (synced).
    2. `len(MarketSlugs) > 0` and no slug resolves → `SoldOut = true`,
       prices stay 0, **do not** consult the catalog.
    3. otherwise (no market slugs, or slugs resolve but rate not yet
       written) → existing behavior: chain rate if set, else catalog.
  - Rationale for case 3 keeping catalog: a chain with market slugs that
    resolve but whose rate is not yet synced should not flicker to sold out;
    the catalog fallback is the pre-existing, safe default there.
- Add `"sold_out": m.SoldOut` to the `publicModels` payload.

Note: `s.marketCache` may be nil in tests; the helper must treat a nil cache
as "no slugs resolve" only when `MarketSlugs` is non-empty, matching the
production wiring where the cache is always constructed in `app.Build`.

### Frontend — `pages/PublicLanding.tsx`

- Extend the public model type (`lib/api.ts`) with `sold_out: boolean`.
- In `ModelCard`: when `model.sold_out`, render a **"Sold out"** badge and
  hide the Input/Output/Cache price cells; otherwise render as today.

## Testing

- Backend unit tests in `gateway/public_handlers_test.go` (or a new
  `public_soldout_test.go`):
  - all bound slugs absent from the snapshot → `sold_out=true`, prices 0,
    catalog price ignored;
  - at least one bound slug present → `sold_out=false`, priced from the
    available slug;
  - chain with no `market_slugs` → `sold_out=false`, catalog price shown.
- Frontend: `tsc -b` and `vite build`.

## Out of scope

- No new DB column or migration.
- No change to metering or routing; sold-out chains remain routable (the
  gateway still attempts steps and fails upstream if truly unavailable).
- No auth/portal change.
