# Per-Step Market Slug Pricing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Bind a market slug to each chain step so requests route cheapest-first by live market price and users are billed the winning step's slug price + markup, eliminating fallback losses.

**Architecture:** Add `market_slug` to `chain_steps` and `market_slug` to `store.ChainStep`. A `market.SnapshotCache` keeps the latest inferhub snapshot in memory, warmed by the existing `runMarketSync` loop. In the gateway, when a chain has `market_slugs` configured and steps carry slugs, `orderStepsByCost` is replaced by a market-aware ordering that reads live slug rates and stamps each `dispatch.Target` with its slug and billable (post-markup) rates. The pipeline already copies `attempt.Target` rates into `meter.Event`; the meter gains a `market_slug` pricing precedence that wins over the stale chain aggregate.

**Tech Stack:** Go 1.26 (chi router, database/sql + SQLite/Postgres, `go test`), React + TypeScript + Vite + TanStack Query (frontend), embedded SQL migrations.

## Global Constraints

- Go module path prefix: `github.com/mydisha/keirouter/backend`.
- Migrations are embedded via `//go:embed migrations/*.sql` and applied in filename order; each migration needs a `.sqlite.sql` and `.postgres.sql` variant. Next free number is `0045`.
- Markup is `market.Settings.MarkupPercent` (default 10), applied in the **gateway/pipeline**, never inside the meter. The meter must not import `internal/market`.
- `ReorderByMarket` (chain flag) defaults **OFF**; existing chains keep current behavior.
- Market cache is always warmed by `runMarketSync`; `AutoRefresh` gates only DB chain-rate writes.
- Never bill an unknown/absent price as zero or free: absent slug → fall back to existing chain/catalog behavior.
- Follow existing patterns in `backend/internal/store/repo_budgets.go`, `backend/internal/gateway/resolve.go`, `backend/internal/app/market_sync.go`.
- Verify with `cd backend && go test ./... && go vet ./...` and `cd frontend && npx tsc -b && npx vite build`.
- Do not commit unless the task step says to commit.

---

### Task 1: Schema + store — `market_slug` on chain steps

**Files:**
- Create: `backend/internal/store/migrations/0045_chain_step_market_slug.sqlite.sql`
- Create: `backend/internal/store/migrations/0045_chain_step_market_slug.postgres.sql`
- Modify: `backend/internal/store/models.go:148-155` (`ChainStep`)
- Modify: `backend/internal/store/repo_budgets.go:296-303,418-426,430-448` (create/update/select steps)
- Test: `backend/internal/store/repo_budgets_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `store.ChainStep.MarketSlug string` (empty = unbound). `ChainRepo` persists and returns it.

- [ ] **Step 1: Write the failing test**

Add to `backend/internal/store/repo_budgets_test.go`:

```go
func TestChainStepMarketSlugRoundTrip(t *testing.T) {
	db := newTestDB(t) // existing helper in this package's tests
	repo := db.Chains()
	ctx := context.Background()
	c := Chain{
		ID: "c1", TenantID: DefaultTenantID, Name: "slug-chain", Strategy: "fallback",
		Steps: []ChainStep{
			{ID: "s1", Position: 0, Provider: "commandcode", Model: "deepseek/deepseek-v4-pro", MarketSlug: "cmc/deepseek/deepseek-v4-pro"},
			{ID: "s2", Position: 1, Provider: "opencode-go", Model: "kimi-k2.6", MarketSlug: ""},
		},
	}
	if err := repo.Create(ctx, c); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := repo.Get(ctx, "c1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Steps) != 2 {
		t.Fatalf("steps = %d, want 2", len(got.Steps))
	}
	if got.Steps[0].MarketSlug != "cmc/deepseek/deepseek-v4-pro" {
		t.Fatalf("step0 slug = %q", got.Steps[0].MarketSlug)
	}
	if got.Steps[1].MarketSlug != "" {
		t.Fatalf("step1 slug = %q, want empty", got.Steps[1].MarketSlug)
	}

	// Update replaces steps; ensure slug is rewritten.
	c.Steps[1].MarketSlug = "ocg/kimi-k2.6"
	if err := repo.Update(ctx, c); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ = repo.Get(ctx, "c1")
	if got.Steps[1].MarketSlug != "ocg/kimi-k2.6" {
		t.Fatalf("after update step1 slug = %q", got.Steps[1].MarketSlug)
	}
}
```

Adjust `newTestDB`/helpers to match the existing test file's helper names (read the file; reuse whatever constructor the other tests use).

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./internal/store/ -run TestChainStepMarketSlugRoundTrip -v`
Expected: FAIL — compile error: `ChainStep` has no field `MarketSlug`.

- [ ] **Step 3: Add the migration files**

`backend/internal/store/migrations/0045_chain_step_market_slug.sqlite.sql`:

```sql
ALTER TABLE chain_steps ADD COLUMN market_slug TEXT NOT NULL DEFAULT '';
```

`backend/internal/store/migrations/0045_chain_step_market_slug.postgres.sql`:

```sql
ALTER TABLE chain_steps ADD COLUMN market_slug TEXT NOT NULL DEFAULT '';
```

- [ ] **Step 4: Add the struct field**

In `backend/internal/store/models.go`, `ChainStep`:

```go
type ChainStep struct {
	ID         string
	ChainID    string
	Position   int
	Provider   string
	Model      string
	MarketSlug string
	CreatedAt  time.Time
}
```

- [ ] **Step 5: Persist in create/update/select**

In `backend/internal/store/repo_budgets.go`, update all three step SQL sites:

Create (line ~296):
```go
sq := r.db.rebind(`INSERT INTO chain_steps (id, chain_id, position, provider, model, market_slug, created_at)
	VALUES (?, ?, ?, ?, ?, ?, ?)`)
for _, s := range c.Steps {
	if _, err := tx.ExecContext(ctx, sq, s.ID, c.ID, s.Position, s.Provider, s.Model,
		s.MarketSlug, formatTime(s.CreatedAt)); err != nil {
		return fmt.Errorf("store: create chain step: %w", err)
	}
}
```

Update (line ~418): same insert shape, with `formatTime(time.Now())`.

`steps()` select (line ~430):
```go
q := r.db.rebind(`SELECT id, chain_id, position, provider, model, market_slug, created_at FROM chain_steps WHERE chain_id = ? ORDER BY position ASC`)
```
and in the scan (read the exact `rows.Scan` below line 439) insert `&s.MarketSlug` between `&s.Model` and `&created`.

- [ ] **Step 6: Run test to verify it passes**

Run: `cd backend && go test ./internal/store/ -run TestChainStepMarketSlugRoundTrip -v`
Expected: PASS

- [ ] **Step 7: Run the store package tests**

Run: `cd backend && go test ./internal/store/`
Expected: PASS (existing chain tests unaffected; default `''` preserves behavior).

- [ ] **Step 8: Commit**

```bash
git add backend/internal/store/migrations/0045_chain_step_market_slug.sqlite.sql \
        backend/internal/store/migrations/0045_chain_step_market_slug.postgres.sql \
        backend/internal/store/models.go backend/internal/store/repo_budgets.go \
        backend/internal/store/repo_budgets_test.go
git commit -m "feat(store): add market_slug to chain steps"
```

---

### Task 2: `market.SnapshotCache` — in-memory live slug rates

**Files:**
- Create: `backend/internal/market/cache.go`
- Test: `backend/internal/market/cache_test.go`

**Interfaces:**
- Consumes: `market.Model` (`market.go:14`), `market.Rate`, `market.validRate` (`pricing.go:51`).
- Produces:
  - `func NewSnapshotCache() *SnapshotCache`
  - `func (c *SnapshotCache) Replace(models []Model)`
  - `func (c *SnapshotCache) Rate(slug string) (Rate, bool)` — raw market ask, no markup; `ok=false` when slug absent or rates invalid/zero.
  - `func (c *SnapshotCache) Age() time.Duration` — since last `Replace`; zero time yields a large age.

- [ ] **Step 1: Write the failing test**

`backend/internal/market/cache_test.go`:

```go
package market

import (
	"sync"
	"testing"
)

func TestSnapshotCacheRate(t *testing.T) {
	c := NewSnapshotCache()
	c.Replace([]Model{
		{Slug: "cmc/deepseek/deepseek-v4.1-flash", MinAskIn: 0.0225, MinAskOut: 0.09},
		{Slug: "zero/ask", MinAskIn: 0, MinAskOut: 0.5},
	})
	r, ok := c.Rate("cmc/deepseek/deepseek-v4.1-flash")
	if !ok || r.InputPerM != 0.0225 || r.OutputPerM != 0.09 {
		t.Fatalf("rate = %+v ok=%v", r, ok)
	}
	if _, ok := c.Rate("missing"); ok {
		t.Fatal("missing slug must be !ok")
	}
	if _, ok := c.Rate("zero/ask"); ok {
		t.Fatal("zero ask must be !ok")
	}
}

func TestSnapshotCacheReplaceIsAtomic(t *testing.T) {
	c := NewSnapshotCache()
	c.Replace([]Model{{Slug: "a/x", MinAskIn: 1, MinAskOut: 2}})
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); c.Replace([]Model{{Slug: "a/x", MinAskIn: 3, MinAskOut: 4}}) }()
		go func() { defer wg.Done(); _, _ = c.Rate("a/x") }()
	}
	wg.Wait()
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./internal/market/ -run TestSnapshotCache -v`
Expected: FAIL — `undefined: NewSnapshotCache`.

- [ ] **Step 3: Implement the cache**

`backend/internal/market/cache.go`:

```go
package market

import (
	"sync"
	"time"
)

// SnapshotCache holds the latest market snapshot in memory. Replace swaps the
// whole map atomically so readers never observe a partially built snapshot.
type SnapshotCache struct {
	mu      sync.RWMutex
	bySlug  map[string]Model
	fetched time.Time
}

func NewSnapshotCache() *SnapshotCache {
	return &SnapshotCache{bySlug: map[string]Model{}}
}

// Replace installs a new snapshot.
func (c *SnapshotCache) Replace(models []Model) {
	next := make(map[string]Model, len(models))
	for _, m := range models {
		next[m.Slug] = m
	}
	c.mu.Lock()
	c.bySlug = next
	c.fetched = time.Now()
	c.mu.Unlock()
}

// Rate returns the raw market ask for slug. ok is false when the slug is absent
// or its asks are not strictly positive finite values.
func (c *SnapshotCache) Rate(slug string) (Rate, bool) {
	c.mu.RLock()
	m, ok := c.bySlug[slug]
	c.mu.RUnlock()
	if !ok || m.MinAskIn <= 0 || m.MinAskOut <= 0 {
		return Rate{}, false
	}
	if !validRate(m.MinAskIn) || !validRate(m.MinAskOut) {
		return Rate{}, false
	}
	return Rate{InputPerM: m.MinAskIn, OutputPerM: m.MinAskOut}, true
}

// Age reports time since the last successful Replace. A never-populated cache
// returns a very large duration so callers can treat it as stale.
func (c *SnapshotCache) Age() time.Duration {
	c.mu.RLock()
	f := c.fetched
	c.mu.RUnlock()
	if f.IsZero() {
		return time.Duration(1<<62 - 1)
	}
	return time.Since(f)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd backend && go test ./internal/market/ -run TestSnapshotCache -v && cd backend && go test -race ./internal/market/ -run TestSnapshotCacheReplaceIsAtomic`
Expected: PASS (no data race).

- [ ] **Step 5: Commit**

```bash
git add backend/internal/market/cache.go backend/internal/market/cache_test.go
git commit -m "feat(market): add in-memory snapshot cache"
```

---

### Task 3: Warm the cache from the market sync loop

**Files:**
- Modify: `backend/internal/app/app.go:69` (add field), `app.go:387,445` (constructors)
- Modify: `backend/internal/app/market_sync.go:21-57,61-91`
- Test: `backend/internal/app/market_sync_test.go`

**Interfaces:**
- Consumes: `market.NewSnapshotCache` (Task 2), `market.Fetch`, `market.LoadSettings`.
- Produces: `App.marketCache *market.SnapshotCache`; a method `func (a *App) MarketCache() *market.SnapshotCache` returning it (used by the gateway in Task 5).

- [ ] **Step 1: Write the failing test**

Add to `backend/internal/app/market_sync_test.go` (follow the existing test's server/DB stub pattern in that file):

```go
func TestSyncChainMarketPricesPopulatesCache(t *testing.T) {
	// Build the App exactly as the existing sync test does, with a stub market
	// URL/server returning:
	//   {"models":[{"slug":"ocg/kimi-k2.6","minAskIn":1,"minAskOut":2},
	//              {"slug":"cmc/deepseek/deepseek-v4-pro","minAskIn":3,"minAskOut":4}]}
	// then call a.syncChainMarketPrices(ctx).
	//
	// Assert a.MarketCache().Rate("ocg/kimi-k2.6") returns (1,2).
	a := newMarketSyncTestApp(t) // reuse the helper used by existing tests; add if absent
	if _, err := a.syncChainMarketPrices(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	r, ok := a.MarketCache().Rate("ocg/kimi-k2.6")
	if !ok || r.InputPerM != 1 || r.OutputPerM != 2 {
		t.Fatalf("cache rate = %+v ok=%v", r, ok)
	}
}
```

If the existing test file has no reusable constructor, read it and factor the smallest shared helper (do not invent a framework).

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./internal/app/ -run TestSyncChainMarketPricesPopulatesCache -v`
Expected: FAIL — `a.MarketCache` undefined.

- [ ] **Step 3: Add the field and accessor**

In `backend/internal/app/app.go`, add to the `App` struct near `marketURL`:

```go
marketCache *market.SnapshotCache
```

In both constructor literals (`app.go:387`, `app.go:445`) add:

```go
marketCache: market.NewSnapshotCache(),
```

Add accessor (near the other `App` methods):

```go
// MarketCache exposes the live market snapshot for routing decisions.
func (a *App) MarketCache() *market.SnapshotCache { return a.marketCache }
```

- [ ] **Step 4: Populate the cache on fetch**

In `backend/internal/app/market_sync.go`, after `models, err := market.Fetch(...)` succeeds (line ~22-25), add:

```go
a.marketCache.Replace(models)
```

Then decouple cache warming from `AutoRefresh`: in `runMarketSync`, currently the ticker returns early when `!settings.AutoRefresh`. Restructure so the fetch+`Replace` happens on the interval regardless, while the DB chain-rate write stays gated by `AutoRefresh`:

```go
case <-ticker.C:
	settings := market.LoadSettings(ctx, a.db.Settings().Get)
	interval := time.Duration(settings.RefreshIntervalSeconds) * time.Second
	if time.Since(lastRun) < interval {
		continue
	}
	lastRun = time.Now()
	models, err := market.Fetch(ctx, a.marketURL)
	if err != nil {
		a.log.Debug("market fetch failed", "err", err)
		continue
	}
	a.marketCache.Replace(models)
	if !settings.AutoRefresh {
		continue
	}
	if n, err := a.syncChainMarketPricesWithModels(ctx, models, settings); err != nil {
		settings.LastFetchError = err.Error()
		_ = market.SaveSettings(ctx, a.db.Settings().Set, settings)
		a.log.Debug("market sync failed", "err", err)
	} else {
		settings.LastFetchedAt = time.Now().UTC().Format(time.RFC3339)
		settings.LastFetchError = ""
		settings.LastSyncedCount = n
		_ = market.SaveSettings(ctx, a.db.Settings().Set, settings)
	}
```

Refactor `syncChainMarketPrices` to keep its public shape (it fetches then delegates):

```go
func (a *App) syncChainMarketPrices(ctx context.Context) (int, error) {
	models, err := market.Fetch(ctx, a.marketURL)
	if err != nil {
		return 0, err
	}
	a.marketCache.Replace(models)
	settings := market.LoadSettings(ctx, a.db.Settings().Get)
	return a.syncChainMarketPricesWithModels(ctx, models, settings)
}
```

Move the existing per-chain loop (lines 21-56) into `syncChainMarketPricesWithModels(ctx, models, settings)` unchanged.

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd backend && go test ./internal/app/ -run "MarketSync|SyncChainMarket" -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add backend/internal/app/app.go backend/internal/app/market_sync.go backend/internal/app/market_sync_test.go
git commit -m "feat(app): warm market snapshot cache from sync loop"
```

---

### Task 4: `ReorderByMarket` flag on the chain

**Files:**
- Modify: `backend/internal/store/models.go:126-145` (`Chain`)
- Create: `backend/internal/store/migrations/0046_chain_reorder_by_market.sqlite.sql`
- Create: `backend/internal/store/migrations/0046_chain_reorder_by_market.postgres.sql`
- Modify: `backend/internal/store/repo_budgets.go:288-292,309-316,337-352,405-408`
- Test: `backend/internal/store/repo_budgets_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `store.Chain.ReorderByMarket bool` (default false).

- [ ] **Step 1: Write the failing test**

Add to `backend/internal/store/repo_budgets_test.go`:

```go
func TestChainReorderByMarketRoundTrip(t *testing.T) {
	db := newTestDB(t)
	repo := db.Chains()
	ctx := context.Background()
	c := Chain{ID: "r1", TenantID: DefaultTenantID, Name: "reorder", Strategy: "fallback",
		ReorderByMarket: true,
		Steps: []ChainStep{{ID: "s1", Position: 0, Provider: "opencode-go", Model: "kimi-k2.6"}}}
	if err := repo.Create(ctx, c); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, _ := repo.Get(ctx, "r1")
	if !got.ReorderByMarket {
		t.Fatal("ReorderByMarket not persisted")
	}
	// Default stays false for a normal chain.
	c2 := Chain{ID: "r2", TenantID: DefaultTenantID, Name: "plain", Strategy: "fallback",
		Steps: []ChainStep{{ID: "s2", Position: 0, Provider: "openai", Model: "gpt-4o"}}}
	_ = repo.Create(ctx, c2)
	got2, _ := repo.Get(ctx, "r2")
	if got2.ReorderByMarket {
		t.Fatal("default should be false")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./internal/store/ -run TestChainReorderByMarketRoundTrip -v`
Expected: FAIL — no field `ReorderByMarket`.

- [ ] **Step 3: Migrations**

`0046_chain_reorder_by_market.sqlite.sql`:
```sql
ALTER TABLE chains ADD COLUMN reorder_by_market INTEGER NOT NULL DEFAULT 0;
```
`0046_chain_reorder_by_market.postgres.sql`:
```sql
ALTER TABLE chains ADD COLUMN reorder_by_market BOOLEAN NOT NULL DEFAULT FALSE;
```

- [ ] **Step 4: Struct field**

In `store/models.go` `Chain`, after `MarketSlugs`:
```go
	// ReorderByMarket orders steps cheapest-first by live market slug rate at
	// request time. Off by default; requires per-step market slugs to be useful.
	ReorderByMarket bool
```

- [ ] **Step 5: Persist in repo**

In `repo_budgets.go`, add `reorder_by_market` to the chain INSERT column list and value, the two SELECT column lists and their `rows.Scan`/`QueryRow.Scan`, and the UPDATE SET list. SQLite stores an int; scan into a local `var reorder int` then `c.ReorderByMarket = reorder != 0` (mirror how the repo already handles any boolean columns — check `repo_*` for an existing bool-scan pattern and reuse it). Postgres flattens bool↔int via the driver; if the repo already scans booleans directly elsewhere, use that same approach for consistency.

- [ ] **Step 6: Run tests to verify they pass**

Run: `cd backend && go test ./internal/store/`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add backend/internal/store/models.go backend/internal/store/repo_budgets.go backend/internal/store/repo_budgets_test.go \
        backend/internal/store/migrations/0046_chain_reorder_by_market.sqlite.sql \
        backend/internal/store/migrations/0046_chain_reorder_by_market.postgres.sql
git commit -m "feat(store): add ReorderByMarket chain flag"
```

---

### Task 5: Market-aware ordering in the gateway + slug/rate on Target

**Files:**
- Modify: `backend/internal/dispatch/dispatch.go:82-93` (`Target`), `dispatch.go:1027-1039` (`TargetsFromChain`)
- Modify: `backend/internal/gateway/resolve.go:104-141` (`chainResult`), add helper
- Modify: `backend/internal/gateway/server.go` (add `marketCache` field + setter)
- Modify: `backend/internal/gateway/*.go` where `Server` is constructed (find the constructor that sets `syncMarketPrices`)
- Test: `backend/internal/gateway/resolve_test.go` (or new `resolve_market_test.go`)

**Interfaces:**
- Consumes: `market.SnapshotCache.Rate` (Task 2), `market.Settings.MarkupPercent`, `store.Chain.ReorderByMarket` + `ChainStep.MarketSlug` (Tasks 1,4).
- Produces:
  - `dispatch.Target.MarketSlug string`
  - `dispatch.Target.MarketRateIn float64`, `MarketRateOut float64` — billable (post-markup) rates; zero when unbound.
  - `func (s *Server) orderStepsByMarket(steps []dispatch.Target, slugOf map[string]string) []dispatch.Target` (or equivalent; see step 4).

- [ ] **Step 1: Write the failing test**

Create `backend/internal/gateway/resolve_market_test.go`:

```go
package gateway

import (
	"testing"

	"github.com/mydisha/keirouter/backend/internal/dispatch"
	"github.com/mydisha/keirouter/backend/internal/market"
)

func TestOrderStepsByMarketCheapestFirstAndStampsRates(t *testing.T) {
	cache := market.NewSnapshotCache()
	cache.Replace([]market.Model{
		{Slug: "cmc/x", MinAskIn: 0.03, MinAskOut: 0.10}, // sum 0.13
		{Slug: "ocg/x", MinAskIn: 0.02, MinAskOut: 0.05}, // sum 0.07 cheapest
	})
	steps := []dispatch.Target{
		{Provider: "commandcode", Model: "x", MarketSlug: "cmc/x"},
		{Provider: "opencode-go", Model: "x", MarketSlug: "ocg/x"},
		{Provider: "plain", Model: "y"}, // no slug
	}
	out := orderStepsByMarket(steps, cache, 10 /*markupPercent*/)
	if out[0].Provider != "opencode-go" {
		t.Fatalf("first = %s, want opencode-go (cheapest)", out[0].Provider)
	}
	if out[1].Provider != "commandcode" {
		t.Fatalf("second = %s, want commandcode", out[1].Provider)
	}
	if out[2].Provider != "plain" {
		t.Fatalf("last = %s, want slugless step last", out[2].Provider)
	}
	// Markup 10% on ocg/x: in 0.022, out 0.055
	if got := out[0].MarketRateIn; got < 0.0219 || got > 0.0221 {
		t.Fatalf("ocg rate in = %v, want ~0.022", got)
	}
	if got := out[0].MarketRateOut; got < 0.0549 || got > 0.0551 {
		t.Fatalf("ocg rate out = %v, want ~0.055", got)
	}
	if out[2].MarketRateIn != 0 {
		t.Fatalf("slugless step must carry no market rate")
	}
}

func TestOrderStepsByMarketIdentityWhenNoSlugs(t *testing.T) {
	steps := []dispatch.Target{{Provider: "a", Model: "1"}, {Provider: "b", Model: "2"}}
	out := orderStepsByMarket(steps, market.NewSnapshotCache(), 10)
	if out[0].Provider != "a" || out[1].Provider != "b" {
		t.Fatalf("order changed with empty cache: %+v", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./internal/gateway/ -run TestOrderStepsByMarket -v`
Expected: FAIL — `undefined: orderStepsByMarket`.

- [ ] **Step 3: Extend `dispatch.Target` and `TargetsFromChain`**

In `dispatch/dispatch.go` `Target`:

```go
	// MarketSlug is the inferhub slug bound to this step (empty = unbound).
	MarketSlug string
	// MarketRateIn/Out are the billable (post-markup) rates for MarketSlug.
	// Zero when unbound; the meter falls back to the chain/catalog price.
	MarketRateIn  float64
	MarketRateOut float64
```

In `TargetsFromChain`, propagate the slug:

```go
func TargetsFromChain(chain store.Chain) []Target {
	out := make([]Target, 0, len(chain.Steps))
	for _, s := range chain.Steps {
		out = append(out, Target{
			Provider: s.Provider, Model: s.Model, MarketSlug: s.MarketSlug,
			InputPerM: chain.InputPerM, OutputPerM: chain.OutputPerM,
			CacheWritePerM: chain.CacheWritePerM, CacheReadPerM: chain.CacheReadPerM,
		})
	}
	return out
}
```

Note: keep stamping the chain-level rates as today, so unbound steps still bill the aggregate.

- [ ] **Step 4: Implement the ordering helper in the gateway**

Add to `backend/internal/gateway/resolve.go`:

```go
// orderStepsByMarket orders steps cheapest-first by the live market rate of each
// step's bound slug and stamps the billable (post-markup) rates onto each slugged
// target. Steps without a resolvable slug keep their relative order and sink
// behind priced steps. When no step has a resolvable slug the input order is
// returned unchanged.
func orderStepsByMarket(steps []dispatch.Target, cache *market.SnapshotCache, markupPercent float64) []dispatch.Target {
	out := append([]dispatch.Target(nil), steps...)
	if cache == nil {
		return out
	}
	mult := 1 + markupPercent/100
	type key struct{ sum float64; ok bool }
	keys := make([]key, len(out))
	for i := range out {
		if out[i].MarketSlug == "" {
			continue
		}
		r, ok := cache.Rate(out[i].MarketSlug)
		if !ok {
			continue
		}
		out[i].MarketRateIn = r.InputPerM * mult
		out[i].MarketRateOut = r.OutputPerM * mult
		keys[i] = key{sum: r.InputPerM + r.OutputPerM, ok: true}
	}
	sort.SliceStable(out, func(i, j int) bool {
		ki, kj := keys[i], keys[j]
		if ki.ok != kj.ok {
			return ki.ok // priced before unpriced
		}
		if !ki.ok {
			return false // both unpriced: preserve order
		}
		return ki.sum < kj.sum
	})
	return out
}
```

Add `"github.com/mydisha/keirouter/backend/internal/market"` to resolve.go imports.

- [ ] **Step 5: Wire the cache + markup into `chainResult`**

`chainResult` needs the cache and markup. Add a `marketCache *market.SnapshotCache` field to `Server` (server.go) and set it alongside the existing `syncMarketPrices` wiring (find where `srv.syncMarketPrices = ...` is assigned; that same place should set `srv.marketCache = app.MarketCache()`).

Change `resolveTargets`/`chainResult` signatures to accept a `*market.SnapshotCache` and `markupPercent float64`. Simplest: make `chains`/`marketCache`/markup available via the `Server` by converting `chainResult` into a method, OR thread the extra args from `resolveTargets`. Prefer minimal churn: add a `marketOrderer` parameter struct to `resolveTargets` and pass it through. Concretely, change `chainResult`:

```go
func chainResult(ctx context.Context, chains ChainSource, latency LatencyReader, cache *market.SnapshotCache, tenantID, name string) (resolveResult, error) {
```
and inside, replace:
```go
case chainStrategyCost:
	steps = orderStepsByCost(steps)
```
with:
```go
case chainStrategyCost:
	steps = orderStepsByCost(steps)
```
but add, before the strategy switch, when `c.ReorderByMarket` is true:
```go
if c.ReorderByMarket && cache != nil {
	settings := market.LoadSettings(ctx, settingsGetter) // see note
	steps = orderStepsByMarket(steps, cache, settings.MarkupPercent)
}
```

Note on settings access: `chainResult` has no settings getter. Minimal approach: pass `markupPercent float64` from the caller (the `Server` handlers already have `s.settings`/`market.LoadSettings`). Compute markup once in the handler layer and pass down. Update the 4 call sites of `resolveTargets` (`gateway/handlers.go:200`, `gateway/gemini.go:68`, `gateway/media.go:26`, and any test) to pass the cache and markup. Read each call site and thread the two values; do not change unrelated logic.

- [ ] **Step 6: Run tests to verify they pass**

Run: `cd backend && go test ./internal/gateway/ -run TestOrderStepsByMarket -v && cd backend && go test ./internal/gateway/ ./internal/dispatch/`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add backend/internal/dispatch/dispatch.go backend/internal/gateway/resolve.go backend/internal/gateway/resolve_market_test.go \
        backend/internal/gateway/server.go backend/internal/gateway/handlers.go backend/internal/gateway/gemini.go backend/internal/gateway/media.go
git commit -m "feat(gateway): order chain steps by live market rate and stamp slug rates"
```

---

### Task 6: Meter bills the winning step's market slug

**Files:**
- Modify: `backend/internal/meter/meter.go:111-147` (`Event`), `meter.go:207-217` (`Record` precedence)
- Modify: `backend/internal/meter/pricing.go` (add `marketPrice` helper near `chainPrice:265`)
- Modify: `backend/internal/pipeline/pipeline.go:1666-1688` (populate new event fields)
- Test: `backend/internal/meter/meter_test.go`

**Interfaces:**
- Consumes: `dispatch.Target.MarketSlug/MarketRateIn/MarketRateOut` (Task 5) via the pipeline.
- Produces: `meter.Event.MarketSlug`, `MarketRateIn`, `MarketRateOut`; billing precedence: market slug → chain → catalog.

- [ ] **Step 1: Write the failing test**

Add to `backend/internal/meter/meter_test.go`:

```go
func TestRecordPrefersMarketSlugOverChainRate(t *testing.T) {
	// Chain aggregate is cheap (1/1); winning step's market slug is pricier.
	m := New(nil, nil, map[string]Price{})
	ev := Event{
		Provider: "commandcode", Model: "deepseek/deepseek-v4-pro",
		InputPerM: 1, OutputPerM: 1, // chain aggregate
		MarketSlug: "cmc/deepseek/deepseek-v4-pro",
		MarketRateIn: 0.15, MarketRateOut: 0.45, // already post-markup
		Usage: core.Usage{PromptTokens: 1_000_000, CompletionTokens: 1_000_000},
	}
	bd := m.costForEvent(ev) // extract a testable helper from Record, see step 3
	if bd.Pricing.Source != "market_slug" {
		t.Fatalf("source = %q, want market_slug", bd.Pricing.Source)
	}
	if bd.Pricing.Key != "cmc/deepseek/deepseek-v4-pro" {
		t.Fatalf("key = %q", bd.Pricing.Key)
	}
	// cost = 0.15 + 0.45 = 0.60 USD = 600_000_000 nanos
	if bd.CostNanos != 600_000_000 {
		t.Fatalf("CostNanos = %d, want 600000000", bd.CostNanos)
	}
}

func TestRecordFallsBackToChainWhenNoSlug(t *testing.T) {
	m := New(nil, nil, map[string]Price{})
	ev := Event{Provider: "openai", Model: "gpt-4o", InputPerM: 2, OutputPerM: 4,
		Usage: core.Usage{PromptTokens: 1_000_000, CompletionTokens: 1_000_000}}
	bd := m.costForEvent(ev)
	if bd.Pricing.Source != "chain" {
		t.Fatalf("source = %q, want chain", bd.Pricing.Source)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./internal/meter/ -run "TestRecordPrefersMarketSlug|TestRecordFallsBackToChain" -v`
Expected: FAIL — `undefined: Event.MarketSlug` / `costForEvent`.

- [ ] **Step 3: Add event fields + extract `costForEvent`**

In `meter/meter.go` `Event`, after the chain rate fields:
```go
	// Winning step's bound market slug and its billable (post-markup) rates.
	// When set, these take precedence over the chain-level rates above.
	MarketSlug    string
	MarketRateIn  float64
	MarketRateOut float64
```

In `meter/pricing.go`, add near `chainPrice`:

```go
// marketPrice returns the billable price for a winning step's market slug. The
// rates are already markup-adjusted by the caller; all-zero rates mean unset.
func marketPrice(ev Event) (Price, bool) {
	if ev.MarketSlug == "" || (ev.MarketRateIn <= 0 && ev.MarketRateOut <= 0) {
		return Price{}, false
	}
	return Price{
		InputPerM: ev.MarketRateIn, OutputPerM: ev.MarketRateOut,
		CachedInputPerM: ev.MarketRateIn, CacheWritePerM: ev.MarketRateIn,
		ReasoningPerM: ev.MarketRateOut, Source: "market_slug",
	}, true
}
```

Refactor `Record`'s pricing branch (meter.go:207-217) into `costForEvent`:

```go
// costForEvent resolves the terminal cost for an event, preferring the winning
// step's market slug, then the chain aggregate, then the catalog.
func (m *Meter) costForEvent(ev Event) CostBreakdown {
	u := clampUsage(ev.Usage)
	savedTokens := 0
	if ev.SlimStats != nil {
		savedTokens += ev.SlimStats.TokensSaved
	}
	if ev.HeadroomStats != nil {
		savedTokens += ev.HeadroomStats.TokensSaved
	}
	if u.PromptTokens+u.CompletionTokens == 0 && !ev.CacheHit {
		return CostBreakdown{Pricing: PricingMatch{Status: "none", MatchKind: "none"}}
	}
	if price, ok := marketPrice(ev); ok {
		return calculateCostFromPrice(pricingMatch(ev.MarketSlug, price, "market_slug", false), u, ev.CacheHit, savedTokens)
	}
	if price, ok := chainPrice(ev); ok {
		return calculateCostFromPrice(pricingMatch("chain", price, "chain", false), u, ev.CacheHit, savedTokens)
	}
	return m.CalculateCost(ev.Provider, ev.Model, u, ev.CacheHit, savedTokens)
}
```

Then `Record` replaces the inline cost block with `cost := m.costForEvent(ev)` (keep `u := clampUsage(ev.Usage)` and the `savedTokens` usage elsewhere in Record intact — verify the rest of Record compiles; `savedTokens` is also used for the record fields, so compute it once in Record and have `costForEvent` recompute or accept it; to avoid double logic, `costForEvent` recomputes internally and Record keeps its own for the non-cost fields — acceptable as it mirrors today's structure).

- [ ] **Step 4: Populate the fields in the pipeline**

In `pipeline/pipeline.go` `recordOutcomeWithTTFT` event literal, after `CacheReadPerM`:
```go
		MarketSlug:     attempt.Target.MarketSlug,
		MarketRateIn:   attempt.Target.MarketRateIn,
		MarketRateOut:  attempt.Target.MarketRateOut,
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd backend && go test ./internal/meter/ ./internal/pipeline/`
Expected: PASS

- [ ] **Step 6: Full backend verification**

Run: `cd backend && go test ./... && go vet ./...`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add backend/internal/meter/meter.go backend/internal/meter/pricing.go backend/internal/meter/meter_test.go backend/internal/pipeline/pipeline.go
git commit -m "feat(meter): bill winning step market slug ahead of chain rate"
```

---

### Task 7: Admin API — accept and return `market_slug` + `reorder_by_market`

**Files:**
- Modify: `backend/internal/gateway/admin.go:1875-1953` (create), `admin.go:1964-2040` (update), and the list/get serializer near `admin.go:1858`
- Test: `backend/internal/gateway/admin_chain_pricing_test.go` (add cases)

**Interfaces:**
- Consumes: `store.ChainStep.MarketSlug`, `store.Chain.ReorderByMarket`.
- Produces: request/response JSON fields `market_slug` (per step) and `reorder_by_market` (per chain).

- [ ] **Step 1: Write the failing test**

Add to `admin_chain_pricing_test.go` (reuse `newChainPricingTestServer`):

```go
func TestAdminChainPersistsStepMarketSlugAndReorder(t *testing.T) {
	s := newChainPricingTestServer(t)
	// POST create with a step carrying market_slug and reorder_by_market=true.
	// (Use the same client/request helper pattern as sibling tests in this file.)
	body := map[string]any{
		"name": "slug-chain", "strategy": "fallback", "reorder_by_market": true,
		"steps": []map[string]any{
			{"provider": "commandcode", "model": "deepseek/deepseek-v4-pro", "market_slug": "cmc/deepseek/deepseek-v4-pro"},
		},
	}
	// ... perform POST /v1/admin/chains, expect 201 ...
	// then GET the chain and assert:
	//   chain.Steps[0].MarketSlug == "cmc/deepseek/deepseek-v4-pro"
	//   chain.ReorderByMarket == true
}
```

Fill the request/assert boilerplate by copying the exact pattern used by `TestAdminChainPricing_ExportImportRoundTrip` in the same file.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./internal/gateway/ -run TestAdminChainPersistsStepMarketSlugAndReorder -v`
Expected: FAIL — response `market_slug`/`reorder_by_market` empty.

- [ ] **Step 3: Extend create handler**

In `adminCreateChain` (`admin.go:1882`), add to the anonymous step struct:
```go
			MarketSlug string `json:"market_slug"`
```
add chain-level:
```go
		ReorderByMarket bool `json:"reorder_by_market"`
```
and when building `store.Chain`, set `ReorderByMarket: body.ReorderByMarket` and each `ChainStep{... MarketSlug: strings.TrimSpace(st.MarketSlug)}`.

- [ ] **Step 4: Extend update handler**

In `adminUpdateChain` (`admin.go:1972`), add:
```go
		ReorderByMarket *bool `json:"reorder_by_market"`
```
and to the step struct `MarketSlug string \`json:"market_slug"\``. Apply: `if body.ReorderByMarket != nil { existing.ReorderByMarket = *body.ReorderByMarket }` and set `MarketSlug` on rebuilt steps. An explicit empty `""` clears the slug; trim whitespace. No other validation.

- [ ] **Step 5: Extend the serializer**

At the chain serialization site (`admin.go:1858` region), add `"reorder_by_market": c.ReorderByMarket` and per-step `"market_slug": s.MarketSlug`. Read the exact serializer shape and mirror it.

- [ ] **Step 6: Run tests to verify they pass**

Run: `cd backend && go test ./internal/gateway/ -run TestAdminChain`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add backend/internal/gateway/admin.go backend/internal/gateway/admin_chain_pricing_test.go
git commit -m "feat(gateway): accept market_slug per step and reorder_by_market"
```

---

### Task 8: Frontend — per-step market slug input and reorder toggle

**Files:**
- Modify: `frontend/src/pages/ChainEditor.tsx`
- Modify: `frontend/src/lib/api.ts` (chain types)
- Test: build only (no frontend unit test harness in repo; verify via typecheck + build)

**Interfaces:**
- Consumes: admin API `market_slug` / `reorder_by_market` (Task 7).
- Produces: editor UI that sends those fields.

- [ ] **Step 1: Extend the chain types**

In `frontend/src/lib/api.ts`, find `ChainStep`/`Chain` types and add `market_slug: string` to the step type and `reorder_by_market: boolean` to the chain type. Match the existing naming style (snake_case JSON is already used).

- [ ] **Step 2: Add step slug state + input**

In `ChainEditor.tsx`, the `DraftChainStep` type (used by `makeDraftStep`) and the step row UI: add a `market_slug` text input per step (default `""`), bound to update the step draft. Place it beneath the `ChainModelPicker` in the step card. Keep the existing grid layout; add a second row for the slug field. Include a helper caption: "Inferhub market slug for this step (optional)".

- [ ] **Step 3: Add the reorder toggle**

Add `const [reorderByMarket, setReorderByMarket] = useState(false)`, initialize from `existing.reorder_by_market ?? false` in the edit-load effect (near line 68), and add a checkbox/switch in the "Model route" card header area: "Order by market price". Caption: "Tries the cheapest bound slug first. Requires steps to have market slugs."

- [ ] **Step 4: Include fields in the save payload**

In the save mutation payload (line ~91), add:
```ts
reorder_by_market: reorderByMarket,
```
and per step:
```ts
steps: completeSteps.map((step) => ({ provider: step.provider, model: step.model, market_slug: step.marketSlug ?? "" })),
```
Also include `market_slug` in the `duplicate` key logic only if duplicate detection is based on provider/model (do not change duplicate semantics — two steps may share provider/model with different slugs only if that is currently allowed; keep existing behavior and do not add slug to the dedupe key).

- [ ] **Step 5: Typecheck and build**

Run: `cd frontend && npx tsc -b && npx vite build`
Expected: PASS, no type errors.

- [ ] **Step 6: Commit**

```bash
git add frontend/src/pages/ChainEditor.tsx frontend/src/lib/api.ts
git commit -m "feat(ui): per-step market slug and reorder-by-market toggle"
```

---

### Task 9: End-to-end verification + docs note

**Files:**
- Modify: `CHANGELOG.md` (add an entry under the unreleased section, matching existing style)
- Test: full suite

- [ ] **Step 1: Run the complete backend suite**

Run: `cd backend && go test ./... && go vet ./...`
Expected: PASS.

- [ ] **Step 2: Run the frontend build**

Run: `cd frontend && npx tsc -b && npx vite build`
Expected: PASS.

- [ ] **Step 3: Manual round-trip smoke test**

Start the backend locally, create a chain via the admin UI with two steps bound to two different slugs (one cheap, one expensive), enable "Order by market price", send a request through `chain:<name>`, and confirm in the usage table that the billed `pricing_key` is the cheapest step's slug and `pricing_source` is `market_slug`. Record the observed values in the PR description.

- [ ] **Step 4: Update CHANGELOG**

Add a line under the current unreleased/next section describing: per-step market slug pricing, cheapest-first ordering, and winning-slug billing.

- [ ] **Step 5: Commit**

```bash
git add CHANGELOG.md
git commit -m "docs: changelog for per-step market slug pricing"
```

---

## Self-Review

**Spec coverage:**
- Schema per-step slug → Task 1.
- In-memory snapshot cache → Task 2; warming → Task 3.
- Cheapest-first ordering (opt-in flag) → Tasks 4, 5.
- Winning-slug billing + provenance → Task 6.
- Markup applied in gateway/pipeline, not meter → Tasks 5 (applies markup) and 6 (meter stores final rate).
- Admin API → Task 7; UI → Task 8.
- Error handling (absent slug → existing behavior) → Tasks 5 (`orderStepsByMarket` keeps order), 6 (`costForEvent` falls back to chain then catalog).
- Compatibility (no slug/no flag = current behavior) → enforced by defaults in Tasks 1, 4.

**Placeholder scan:** Test bodies for Tasks 3, 7, 8 reference existing repo test helpers; the implementer must read those files to reuse the exact helper names. No "TBD"/"implement later" markers; code blocks are concrete except the noted "reuse existing helper" boilerplate, which is intentional to avoid duplicating a test harness the implementer can see.

**Type consistency:** `MarketSlug`/`MarketRateIn`/`MarketRateOut` names are identical across `dispatch.Target`, `meter.Event`, and the pipeline mapping. `ReorderByMarket` is identical across `store.Chain`, repo, API JSON (`reorder_by_market`), and UI. `orderStepsByMarket` signature is consistent between definition (Task 5 step 4) and tests.

**Known risk:** Task 5 must thread `marketCache` + markup through `resolveTargets` to 4 call sites; the exact call-site edits depend on the current handler signatures. The implementer must read each call site. If threading proves invasive, an acceptable alternative is a package-level `Server` method — but keep the ordering logic pure (`orderStepsByMarket`) and tested independently, which this plan already does.
