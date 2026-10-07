# Soldout Detection for Market-Slug Chains — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Mark a routing chain as `sold_out` on the public landing page when it binds `market_slugs` but none of them resolve in the live market snapshot, and render a "Sold out" badge with the price hidden.

**Architecture:** The gateway's `publicModelRows` reads the live in-memory `market.SnapshotCache` to decide availability. A chain with `MarketSlugs` where no slug resolves is sold out; price stays 0 and the static catalog fallback is skipped. Chains without `MarketSlugs` keep the existing catalog/manual behavior. The frontend hides the price block and shows a badge for sold-out models.

**Tech Stack:** Go (gateway, market cache), React/TypeScript/Vite (landing).

## Global Constraints

- No new DB column or migration.
- Detection source is `Server.marketCache` (`*market.SnapshotCache`), which in production is always constructed in `app.Build`; tests using `New(Deps{...})` leave it nil and the code MUST tolerate nil.
- Sold out ⟺ `len(MarketSlugs) > 0` AND no slug resolves.
- Chains with no `market_slugs` are never sold out.
- A chain whose slugs resolve but whose rate is not yet synced keeps the existing chain-then-catalog price precedence (no sold-out flicker).
- Never stage or commit `backend/internal/gateway/gateway_e2e_test.go` (pre-existing dirty working-tree change).
- `docs/` is gitignored; commit plan/spec files with `git add -f`.

---

### Task 1: Backend — sold-out detection in `publicModelRows`

**Files:**
- Modify: `backend/internal/gateway/public.go`
- Test: `backend/internal/gateway/public_soldout_test.go` (create)

**Interfaces:**
- Consumes: `market.SnapshotCache.Rate(slug string) (Rate, bool)` (defined `backend/internal/market/cache.go`); `Server.marketCache *market.SnapshotCache` (`backend/internal/gateway/server.go:95`); `store.Chain.MarketSlugs []string`.
- Produces:
  - `func (s *Server) anyMarketSlugResolves(slugs []string) bool` — true if at least one slug yields a valid rate; false for empty slugs or nil cache.
  - `publicModelRow.SoldOut bool`.
  - `publicModels` payload gains `"sold_out": bool`.

- [ ] **Step 1: Write the failing tests**

Create `backend/internal/gateway/public_soldout_test.go`:

```go
package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/market"
	"github.com/mydisha/keirouter/backend/internal/store"
)

type publicModelJSON struct {
	ModelID    string  `json:"model_id"`
	InputPerM  float64 `json:"input_per_m"`
	OutputPerM float64 `json:"output_per_m"`
	SoldOut    bool    `json:"sold_out"`
}

func getPublicModels(t *testing.T, gw *Server) []publicModelJSON {
	t.Helper()
	rec := httptest.NewRecorder()
	gw.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/public/models", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var payload struct {
		Models []publicModelJSON `json:"models"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	return payload.Models
}

// All bound slugs absent from the snapshot -> sold out, prices stay 0 even
// though the first step (openai/gpt-4o) has a non-zero catalog price.
func TestPublicModelsSoldOutWhenNoMarketSlugResolves(t *testing.T) {
	db, gw := newPublicTestGatewayWithDB(t)
	require.NoError(t, db.Chains().Create(context.Background(), store.Chain{
		ID: "c-soldout", TenantID: store.DefaultTenantID, Name: "claude-fable-5.1",
		Strategy:    "priority",
		MarketSlugs: []string{"cc/claude-fable-5-1"},
		Steps:       []store.ChainStep{{Provider: "openai", Model: "gpt-4o", Position: 0}},
		CreatedAt:   time.Now(), UpdatedAt: time.Now(),
	}))
	gw.marketCache = market.NewSnapshotCache() // empty snapshot: nothing resolves

	models := getPublicModels(t, gw)
	require.Len(t, models, 1)
	require.True(t, models[0].SoldOut, "all slugs missing => sold out")
	require.Zero(t, models[0].InputPerM, "sold-out price must stay 0")
	require.Zero(t, models[0].OutputPerM)
}

// One bound slug resolves -> available, priced from the resolved slug.
func TestPublicModelsAvailableWhenOneMarketSlugResolves(t *testing.T) {
	db, gw := newPublicTestGatewayWithDB(t)
	require.NoError(t, db.Chains().Create(context.Background(), store.Chain{
		ID: "c-partial", TenantID: store.DefaultTenantID, Name: "partial-combo",
		Strategy:    "priority",
		MarketSlugs: []string{"cc/claude-fable-5-1", "ocg/deepseek-v4.1-flash"},
		Steps:       []store.ChainStep{{Provider: "custom-openai-inf", Model: "deepseek-v4.1-flash", Position: 0}},
		CreatedAt:   time.Now(), UpdatedAt: time.Now(),
	}))
	cache := market.NewSnapshotCache()
	cache.Replace([]market.Model{{Slug: "ocg/deepseek-v4.1-flash", MinAskIn: 0.042, MinAskOut: 0.168}})
	gw.marketCache = cache

	models := getPublicModels(t, gw)
	require.Len(t, models, 1)
	require.False(t, models[0].SoldOut, "one resolving slug => not sold out")
}

// No market slugs -> never sold out; catalog price shown as today.
func TestPublicModelsNoMarketSlugsNeverSoldOut(t *testing.T) {
	db, gw := newPublicTestGatewayWithDB(t)
	require.NoError(t, db.Chains().Create(context.Background(), store.Chain{
		ID: "c-catalog", TenantID: store.DefaultTenantID, Name: "catalog-combo",
		Strategy:  "priority",
		Steps:     []store.ChainStep{{Provider: "openai", Model: "gpt-4o", Position: 0}},
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}))
	gw.marketCache = market.NewSnapshotCache()

	models := getPublicModels(t, gw)
	require.Len(t, models, 1)
	require.False(t, models[0].SoldOut, "no market slugs => never sold out")
	require.Greater(t, models[0].InputPerM, 0.0, "catalog price still shown")
}

// nil cache must not panic and must not mark a no-slug chain sold out.
func TestPublicModelsNilMarketCacheNoSlugs(t *testing.T) {
	db, gw := newPublicTestGatewayWithDB(t)
	require.NoError(t, db.Chains().Create(context.Background(), store.Chain{
		ID: "c-nil", TenantID: store.DefaultTenantID, Name: "nil-combo",
		Strategy:  "priority",
		Steps:     []store.ChainStep{{Provider: "openai", Model: "gpt-4o", Position: 0}},
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}))
	// gw.marketCache stays nil (New(Deps{...}) does not wire it).
	models := getPublicModels(t, gw)
	require.Len(t, models, 1)
	require.False(t, models[0].SoldOut)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd backend && go test ./internal/gateway/ -run 'TestPublicModelsSoldOut|TestPublicModelsAvailable|TestPublicModelsNoMarketSlugs|TestPublicModelsNilMarketCache' -v`
Expected: FAIL — `models[0].SoldOut` is false because the field does not exist yet / is always false (compile error or assertion failure).

- [ ] **Step 3: Implement the detection**

In `backend/internal/gateway/public.go`, add `SoldOut bool` to `publicModelRow` (after `CacheWrite float64`):

```go
	CacheWrite  float64
	SoldOut     bool
```

Add the helper near `publicModelRows`:

```go
// anyMarketSlugResolves reports whether at least one of the chain's bound
// market slugs currently resolves in the live snapshot. A nil cache (tests
// that do not wire one) resolves nothing.
func (s *Server) anyMarketSlugResolves(slugs []string) bool {
	if s.marketCache == nil {
		return false
	}
	for _, slug := range slugs {
		if _, ok := s.marketCache.Rate(slug); ok {
			return true
		}
	}
	return false
}
```

Replace the pricing block inside the `for _, c := range chains` loop (currently `public.go:88-94`) with:

```go
		first := c.Steps[0]
		// Chain-configured rates take precedence; all-zero means unset.
		inputPerM, outputPerM, cachedPerM, cacheWritePerM := c.InputPerM, c.OutputPerM, c.CacheReadPerM, c.CacheWritePerM
		soldOut := false
		if len(c.MarketSlugs) > 0 && !s.anyMarketSlugResolves(c.MarketSlugs) {
			// Every bound market slug is missing: the model is sold out.
			// Do not surface a catalog price the market cannot honour.
			soldOut = true
			inputPerM, outputPerM, cachedPerM, cacheWritePerM = 0, 0, 0, 0
		} else if inputPerM <= 0 && outputPerM <= 0 && cachedPerM <= 0 && cacheWritePerM <= 0 {
			price, _ := connectors.ModelPriceByProviderModel(first.Provider, first.Model)
			inputPerM, outputPerM, cachedPerM, cacheWritePerM = price.InputPerM, price.OutputPerM, price.CachedInputPerM, price.CacheWritePerM
		}
```

Then add `SoldOut: soldOut,` to the `publicModelRow{...}` literal.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd backend && go test ./internal/gateway/ -run 'TestPublicModels' -v`
Expected: PASS (all `TestPublicModels*`, including pre-existing ones).

- [ ] **Step 5: Add `sold_out` to the payload**

In `publicModels` (same file), add to the map literal:

```go
			"cache_write_per_m": m.CacheWrite,
			"sold_out":          m.SoldOut,
```

- [ ] **Step 6: Verify build and full gateway package**

Run: `cd backend && go build ./... && go vet ./internal/gateway/ && go test ./internal/gateway/ -count=1`
Expected: build clean, vet clean, all gateway tests PASS.

- [ ] **Step 7: Commit**

```bash
git add backend/internal/gateway/public.go backend/internal/gateway/public_soldout_test.go
git commit -m "feat(gateway): flag sold-out chains when no market slug resolves"
```

---

### Task 2: Frontend — "Sold out" badge and hidden price

**Files:**
- Modify: `frontend/src/lib/api.ts`
- Modify: `frontend/src/pages/PublicLanding.tsx`

**Interfaces:**
- Consumes: `GET /v1/public/models` now returns `"sold_out": boolean` per model.
- Produces: `PublicModel.sold_out: boolean`; `ModelCard` renders a Sold out badge and hides the price grid when true.

- [ ] **Step 1: Extend the public model type**

In `frontend/src/lib/api.ts`, find the `PublicModel` type (the one with `input_per_m`, `output_per_m`, `cached_per_m`, `cache_write_per_m` used by `fetchPublicModels`) and add:

```ts
  sold_out: boolean;
```

- [ ] **Step 2: Render the badge and hide prices**

In `frontend/src/pages/PublicLanding.tsx`, locate the `ModelCard` component. Wrap the price grid so that when `model.sold_out` is true the Input/Output/Cache cells are replaced by a "Sold out" badge, and otherwise the existing cells render unchanged. Concretely, inside `ModelCard` render:

```tsx
{model.sold_out ? (
  <div className="mt-3 inline-flex items-center gap-1 rounded-full border border-[var(--muted)] px-2.5 py-1 text-xs font-medium text-[var(--muted)]">
    Sold out
  </div>
) : (
  <div className="grid grid-cols-2 gap-2">
    {/* existing Input / Output / Cache Read / Cache Write cells unchanged */}
  </div>
)}
```

Use the existing badge/cell markup already present in `ModelCard` for the non-sold-out branch — do not restyle it.

- [ ] **Step 3: Typecheck and build**

Run: `cd frontend && npx tsc -b --noEmit && npx vite build`
Expected: typecheck clean, build succeeds.

- [ ] **Step 4: Run frontend tests**

Run: `cd frontend && npm test`
Expected: all existing tests PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/api.ts frontend/src/pages/PublicLanding.tsx
git commit -m "feat(ui): show Sold out badge and hide price for unavailable chains"
```

---

### Task 3: Live verification

**Files:** none (verification only).

- [ ] **Step 1: Rebuild and run**

Run: `cd /home/emalution/keirouter && KEIROUTER_PORT=20280 docker compose -f compose.localhost.yaml up -d --build`
Then wait for healthy and confirm: `curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:20280/healthz` → `200`.

- [ ] **Step 2: Confirm the sold-out model in the public API**

Run: `curl -s http://127.0.0.1:20280/v1/public/models | python3 -c "import json,sys; d=json.load(sys.stdin); [print(m['model_id'], 'sold_out=', m.get('sold_out'), 'in=', m['input_per_m']) for m in d['models'] if 'fable' in m['model_id'].lower() or m.get('sold_out')]"`
Expected: `claude-fable-5.1` (or any sold-out chain) shows `sold_out= True` and `in= 0.0`.

- [ ] **Step 3: Confirm a healthy chain is not sold out**

From the same output, a chain with a live slug (e.g. `deepseek-v4.1-flash`) shows `sold_out= False` and a non-zero price.

---

## Self-Review

**Spec coverage:**
- Rule (sold out ⟺ market_slugs present and none resolve) → Task 1 Step 3 `anyMarketSlugResolves` + branch.
- Chains without market_slugs never sold out → Task 1 test `TestPublicModelsNoMarketSlugsNeverSoldOut`.
- Detection from `Server.marketCache`, nil-tolerant → Task 1 helper + `TestPublicModelsNilMarketCacheNoSlugs`.
- Payload field → Task 1 Step 5.
- Landing badge + hidden price → Task 2.
- Testing (backend 3 cases + frontend build) → Tasks 1–2; live check → Task 3.
- Out of scope (no DB/migration, no metering change) → Global Constraints.

**Placeholder scan:** None; every code step shows actual code.

**Type consistency:** `anyMarketSlugResolves([]string) bool`, `publicModelRow.SoldOut`, payload key `sold_out`, and `PublicModel.sold_out` are consistent across tasks.
