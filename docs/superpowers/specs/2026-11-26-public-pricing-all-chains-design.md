# Public pricing for every tokenizer.id model — design

Date: 2026-11-26
Status: approved in conversation, pending builder check

## Intent

Resellers who buy with PAYG keys need to set their own selling price before a
request runs. `usage.cost` (shipped on `feat/usage-cost`) tells them what one
request cost after the fact; this API tells anyone, without a key, what each
tokenizer.id model costs right now and the most it can cost.

Today `GET /v1/public/pricing` exists but reads Surplus only: chains priced by
any other upstream (Subrouter, a6api, inferhub, OpenRouter, sharellm, a fixed
chain price) are silently left out (`gateway/pricing.go` `surplusSlugs`). The
owner wants the endpoint to describe tokenizer.id's own models, whatever serves
them, with a per-provider breakdown and every price level under each provider,
like the portal Model Settings page.

It also has to remove the Surplus price confusion customers hit:
- the headline price is the cheapest offer, but a request is billed at the
  offer that served it, up to the failover ceiling;
- three different discount formulas exist for one offer (`math.Max` of in/out
  in `BestDiscountPct`, `math.Min` in the ceiling, blended in+out on
  `/v1/public/model-detail`);
- the best-ask feed's cache price may belong to another offer than its input
  price, so a "90% off" can be a cache price.

## Goals

- `GET /v1/public/pricing` lists every published chain, from any upstream.
- Each model shows its current price, a guaranteed maximum (`max_*`) and a
  `price_type` (`fixed` or `variable`).
- Each model lists its providers (routes) in routing order, each with its own
  best price and every price level (`prices[]`), grouped like the portal list.
- One discount formula everywhere in this response.
- Safe to expose publicly (see Security).

## Non-goals

- The per-key `GET /v1/pricing` keeps its current Surplus-only behaviour. It
  can adopt the new builder later.
- No change to how requests are routed or billed. This API describes billing;
  it does not change it.
- No price lock or guarantee beyond what billing already enforces.

## Anti-goals (a failure even if it works)

- Any internal provider ID, upstream account, Surplus seller/provider ID,
  upstream cost, margin or markup percentage in the response.
- A `max_*` lower than something a request on that model can actually be
  billed while `max_guaranteed` is `true`. Resellers price from it; an
  understated guaranteed max loses them money. When billing cannot bound a
  route, the response must say so (`max_guaranteed: false`) instead.
- A chain that is not published by an admin, disabled, hidden or in maintenance
  showing up, or being distinguishable from a nonexistent one.

## Response

Envelope unchanged (`currency`, `unit`, `as_of`, `models`), so existing
consumers keep working. Existing per-model fields stay; new ones are added.

```json
{
  "currency": "USD",
  "unit": "per_1m_tokens",
  "as_of": "2026-11-26T08:30:00Z",
  "models": [
    {
      "model": "claude-haiku-5.5",
      "available": true,
      "price_type": "variable",
      "input_per_m": 0.0862,
      "output_per_m": 0.431,
      "cached_input_per_m": 0.00862,
      "cache_write_per_m": 0.10775,
      "max_input_per_m": 0.1125,
      "max_output_per_m": 0.5625,
      "max_guaranteed": false,
      "min_request_cost": 0.00001,
      "official_input_per_m": 0.10,
      "official_output_per_m": 0.50,
      "discount_pct": 13,
      "avg_input_per_m": 0.09,
      "avg_output_per_m": 0.45,
      "avg_window_days": 7,
      "avg_sample_tokens": 1200000,
      "providers": [
        {
          "name": "Anthropic",
          "available": true,
          "input_per_m": 0.0862,
          "output_per_m": 0.431,
          "cached_input_per_m": 0.00862,
          "discount_pct": 13,
          "latency_p50_ms": 2300,
          "prices": [
            { "input_per_m": 0.0862, "output_per_m": 0.431,  "offers": 1, "discount_pct": 13, "tag": "cheapest" },
            { "input_per_m": 0.0975, "output_per_m": 0.4875, "offers": 5, "discount_pct": 2 },
            { "input_per_m": 0.18,   "output_per_m": 0.90,   "offers": 1, "discount_pct": 0,  "tag": "highest" }
          ]
        },
        {
          "name": "Openrouter",
          "available": true,
          "input_per_m": 0.1395,
          "output_per_m": 0.6975,
          "discount_pct": 0,
          "prices": [
            { "input_per_m": 0.1395, "output_per_m": 0.6975, "offers": 1, "discount_pct": 0 }
          ]
        }
      ]
    }
  ]
}
```

### Field rules

All prices are what the customer is billed, markup included, USD per 1M tokens.
The markup is the one billing applies: the chain's own markup when set, else
the global market markup (`market.EffectiveMarkup`); static steps use
`staticBillMult` (markup, or the step's discount setting). Per-key discount
floors are not applied, since the API has no key.
Unknown values are `null`, never `0`.

Model level:
- `input_per_m` / `output_per_m`: the price of the first available route in
  routing order (what a request gets when nothing fails over).
- `cached_input_per_m` / `cache_write_per_m`: the cache rates **billed** on that
  route (for Surplus steps: input × `market.CacheReadMult` / `CacheWriteMult`,
  the same rates billing uses), never the feed's independent best cache price.
- `max_input_per_m` / `max_output_per_m`: the highest rate any route of the
  chain can bill, across all steps and the chain fallback target when it is
  priced. Per route kind:
  - fixed/static step price: that price;
  - Surplus trusted/fast: the failover ceiling × markup (as `chainMaxRate`);
  - plain `surplus:` step: its stamped best-ask × markup (billed at the stamp);
  - cost-lookup (Subrouter, a6api): the ceiling static price the request is
    provisionally billed at × markup;
  - inferhub: the stamped maxAsk bid cap × markup;
  - a route with no known rate at all makes the model's `max_*` `null` (never
    a guess).
  Input and output maxima are taken independently (per-field max).
- `max_guaranteed` (owner's decision, option 1): `true` only when every route
  of the chain bills at a rate fixed before the request (a static step price
  that is never re-billed, or a plain `surplus:` stamp). Any route billed from
  an upstream-reported or reconciled cost (Surplus trusted/fast, inferhub,
  OpenRouter `usage.cost`, Subrouter, a6api) makes it `false`: those bills are
  not clipped to a ceiling (`costlookup/settle.go` records `over_ceiling`;
  `upstreamBilledCost` does not cap), so `max_*` is then the expected ceiling,
  not a promise.
- `min_request_cost`: the highest known per-request minimum charge across the
  chain's routes (configured `MinChargeMicro` × markup, in USD), `null` when
  no route has one. A small request is billed at least this, whatever the
  per-token rates say.
- `price_type`: `fixed` only when every route bills a static price that is
  never adjusted afterwards. Cost-lookup steps (Subrouter, a6api) are
  `variable` even though they carry a static price, because the sweeper
  re-bills them from the upstream log. *(Writer's call, from the builder
  check.)*
- `official_*`, `avg_*`, `avg_window_days`, `avg_sample_tokens`: unchanged
  (official reference price; 7-day realized average).
- `discount_pct`: the one formula below applied to `input_per_m`/`output_per_m`
  against the official price. **Changed meaning:** it used to be the 7-day
  realized discount. The realized figure stays available through `avg_*`.

Provider (route) level, one entry per upstream provider of the chain, in
routing order, grouped as `modelSettingsRow` groups steps today:
- `name`: the step's admin-set `DisplayName`; when empty, a generic
  `Provider N` (N = 1-based position among providers). Never the provider ID
  or the catalog name of an internal upstream.
- `available`, `input_per_m`, `output_per_m`, `cached_input_per_m`,
  `discount_pct`, `latency_p50_ms`: as the portal computes them
  (`stepCustomerQuote`), with discount by the one formula.
- `prices[]`: every offer level, cheapest first, offers with an identical
  (in, out) pair merged with `offers` = count. Non-Surplus routes have one
  level. `tag`: `cheapest` on the first, `highest` on the last when there is
  more than one level, absent otherwise.

### One discount formula

`discount_pct = floor((1 - (in + out) / (official_in + official_out)) * 100)`,
clamped at 0, `null` without an official price. Same as `discountPercent` in
`gateway/official_price.go:95` (floor, so a discount never reads larger than
it is). Used for model, provider and price levels.

### Routing order and the chain fallback

*(Writer's calls, from the builder check.)*
- "Routing order" is the chain's declared step order. Round-robin, latency and
  cost strategies reorder per request; the public API does not replay that.
  The headline is the first available declared route; `max_*` covers every
  route regardless of order, so the guarantee does not depend on ordering.
- The chain fallback target is per key (`FinalFallbackOn`) and can be off. It
  is listed last in `providers[]` with `"final_fallback": true`, named by the
  same `DisplayName`/`Provider N` rule. It never sets the headline price or
  `available`, but it is included in `max_*` and `price_type`, because some
  keys can be billed through it.

## Security

Kept from today:
- Off by default; admin publishes chains by ID (`public_pricing` setting).
- Per-IP limit 30/min (`publicPricingRateLimiter`); client IP from the
  right-most `X-Forwarded-For` only behind a trusted private proxy.
- One-entry 2s build cache, `?model=` capped at 50, unknown and unpublished
  names treated alike.
- Unlisted (hidden/maintenance) and disabled chains never shown.

Added:
- Response built from dedicated structs with an explicit field list; no
  embedding of internal structs, so a new internal field can never leak.
- A test asserts the serialized response contains none of: chain step provider
  IDs, account IDs, market slugs, Surplus provider IDs, markup percentage.
- `Cache-Control: public, max-age=5` (was `no-store`) so a CDN absorbs polling;
  prices are at most ~5s staler, inside the market refresh interval.
- CORS: `Access-Control-Allow-Origin: *`, `GET`/`OPTIONS` only, no
  credentials, so reseller sites can call it from the browser without exposing
  any session.
- Errors stay generic (`pricing unavailable`).

## Admin

The Public Pricing tab (`frontend/src/pages/settings/PublicPricingTab.tsx`,
`adminGetPublicPricing`) lists only Surplus-priced chains today. It must list
every chain so an admin can publish any of them; preview uses the new builder.
Already published chain IDs keep working.

## Approaches considered

1. **Extend `/v1/public/pricing` (chosen).** Reuses its security, admin
   toggle and URL; existing consumers keep working. Cost: `discount_pct` changes
   meaning.
2. New `/v1/public/prices` beside the old one. Two price endpoints that can
   disagree, twice the public surface.
3. Admin-entered static selling prices. Exact but not realtime; manual upkeep
   whenever Surplus moves.

## Builder's calls

- Where the new builder lives (suggested: alongside `stepCustomerQuote` so the
  portal and the public API price routes the same way) and how the portal's
  grouping code is shared rather than copied.
- Test structure, provided every route kind in the `max_*` rules has a test,
  and the leak test above exists.
- Rounding of displayed prices: none beyond float formatting; prices are the
  computed values.
