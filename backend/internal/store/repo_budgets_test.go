package store

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// incrementBudgetTx runs IncrementLimitOnTx in its own committed transaction.
func incrementBudgetTx(t *testing.T, db *DB, id string, delta int64) (int64, int64) {
	t.Helper()
	ctx := context.Background()
	tx, err := db.sql.BeginTx(ctx, nil)
	require.NoError(t, err)
	before, after, err := db.Budgets().IncrementLimitOnTx(ctx, tx, id, delta)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	return before, after
}

func TestBudgetRepo_IncrementLimitOnTx(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	b := Budget{
		ID: "b1", TenantID: DefaultTenantID, ScopeKind: ScopeAPIKey, ScopeID: "key1",
		LimitMicros: 1_000_000, Period: "total", AlertPct: 80, HardCutoff: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	require.NoError(t, db.Budgets().Create(ctx, b))

	before, after := incrementBudgetTx(t, db, "b1", 2_500_000)
	require.Equal(t, int64(1_000_000), before)
	require.Equal(t, int64(3_500_000), after)

	got, err := db.Budgets().Get(ctx, "b1")
	require.NoError(t, err)
	require.Equal(t, int64(3_500_000), got.LimitMicros)

	// Unknown id -> ErrNotFound, no row written.
	tx, err := db.sql.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, _, err = db.Budgets().IncrementLimitOnTx(ctx, tx, "missing", 1)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestBudgetRepo_ListByScope_NewestFirst(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// Two budgets for one scope is possible (no unique constraint); the first
	// row returned must be the newest so the top-up handler credits the same
	// row the UI displays.
	older := Budget{
		ID: "older", TenantID: DefaultTenantID, ScopeKind: ScopeAPIKey, ScopeID: "dup-key",
		LimitMicros: 1_000_000, Period: "total", AlertPct: 80, HardCutoff: true,
		CreatedAt: time.Now().Add(-time.Hour), UpdatedAt: time.Now().Add(-time.Hour),
	}
	newer := Budget{
		ID: "newer", TenantID: DefaultTenantID, ScopeKind: ScopeAPIKey, ScopeID: "dup-key",
		LimitMicros: 2_000_000, Period: "total", AlertPct: 80, HardCutoff: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	require.NoError(t, db.Budgets().Create(ctx, older))
	require.NoError(t, db.Budgets().Create(ctx, newer))

	got, err := db.Budgets().ListByScope(ctx, ScopeAPIKey, "dup-key")
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "newer", got[0].ID)
	require.Equal(t, "older", got[1].ID)
}

func TestChainMarketSlugsRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	now := time.Now().UTC()
	c := Chain{
		ID: "chain-market", TenantID: DefaultTenantID, Name: "chain-market", Strategy: "priority",
		InputPerM: 1, OutputPerM: 2, CacheWritePerM: 3, CacheReadPerM: 4,
		MarketSlugs: []string{"a/one", "b/two"},
		Steps:       []ChainStep{{ID: "s1", ChainID: "chain-market", Position: 0, Provider: "openai", Model: "gpt-4o", CreatedAt: now}},
		CreatedAt:   now, UpdatedAt: now,
	}
	require.NoError(t, db.Chains().Create(ctx, c))

	got, err := db.Chains().Get(ctx, "chain-market")
	require.NoError(t, err)
	require.Equal(t, []string{"a/one", "b/two"}, got.MarketSlugs)

	list, err := db.Chains().ListByTenant(ctx, DefaultTenantID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, []string{"a/one", "b/two"}, list[0].MarketSlugs)

	list[0].MarketSlugs = []string{"c/three"}
	require.NoError(t, db.Chains().Update(ctx, list[0]))
	after, err := db.Chains().Get(ctx, "chain-market")
	require.NoError(t, err)
	require.Equal(t, []string{"c/three"}, after.MarketSlugs)

	empty := c
	empty.ID = "chain-empty"
	empty.Name = "chain-empty"
	empty.MarketSlugs = nil
	empty.Steps = nil
	require.NoError(t, db.Chains().Create(ctx, empty))
	gotEmpty, err := db.Chains().Get(ctx, "chain-empty")
	require.NoError(t, err)
	require.Empty(t, gotEmpty.MarketSlugs)
}

func TestChainRepo_UpdateRatesOnlyTouchesPrices(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	now := time.Now().UTC()

	c := Chain{
		ID: "cr-1", TenantID: DefaultTenantID, Name: "keep-name", Strategy: "priority",
		FallbackProvider: "openai", FallbackModel: "gpt-4o",
		InputPerM: 1, OutputPerM: 2, CacheWritePerM: 3, CacheReadPerM: 4,
		MarketSlugs: []string{"a/one", "b/two"},
		Steps:       []ChainStep{{ID: "cs-1", ChainID: "cr-1", Position: 0, Provider: "openai", Model: "gpt-4o", CreatedAt: now}},
		CreatedAt:   now, UpdatedAt: now,
	}
	require.NoError(t, db.Chains().Create(ctx, c))

	require.NoError(t, db.Chains().UpdateRates(ctx, "cr-1", 10, 20, 30, 40))

	got, err := db.Chains().Get(ctx, "cr-1")
	require.NoError(t, err)
	require.Equal(t, 10.0, got.InputPerM)
	require.Equal(t, 20.0, got.OutputPerM)
	require.Equal(t, 30.0, got.CacheWritePerM)
	require.Equal(t, 40.0, got.CacheReadPerM)
	require.Equal(t, "keep-name", got.Name)
	require.Equal(t, "priority", got.Strategy)
	require.Equal(t, "openai", got.FallbackProvider)
	require.Equal(t, "gpt-4o", got.FallbackModel)
	require.Equal(t, []string{"a/one", "b/two"}, got.MarketSlugs)
	require.Len(t, got.Steps, 1)
	require.Equal(t, "cs-1", got.Steps[0].ID)
}

func TestChainStepMarketSlugRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	repo := db.Chains()

	c := Chain{
		ID: "c1", TenantID: DefaultTenantID, Name: "slug-chain", Strategy: "fallback",
		Steps: []ChainStep{
			{ID: "s1", Position: 0, Provider: "commandcode", Model: "deepseek/deepseek-v4-pro", MarketSlug: "cmc/deepseek/deepseek-v4-pro"},
			{ID: "s2", Position: 1, Provider: "opencode-go", Model: "kimi-k2.6", MarketSlug: ""},
		},
	}
	require.NoError(t, repo.Create(ctx, c))

	got, err := repo.Get(ctx, "c1")
	require.NoError(t, err)
	require.Len(t, got.Steps, 2)
	require.Equal(t, "cmc/deepseek/deepseek-v4-pro", got.Steps[0].MarketSlug)
	require.Equal(t, "", got.Steps[1].MarketSlug)

	c.Steps[1].MarketSlug = "ocg/kimi-k2.6"
	require.NoError(t, repo.Update(ctx, c))
	got, err = repo.Get(ctx, "c1")
	require.NoError(t, err)
	require.Equal(t, "ocg/kimi-k2.6", got.Steps[1].MarketSlug)
}

func TestChainReorderByMarketRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	repo := db.Chains()

	c := Chain{ID: "r1", TenantID: DefaultTenantID, Name: "reorder", Strategy: "fallback",
		ReorderByMarket: true,
		Steps:           []ChainStep{{ID: "s1", Position: 0, Provider: "opencode-go", Model: "kimi-k2.6"}}}
	require.NoError(t, repo.Create(ctx, c))
	got, err := repo.Get(ctx, "r1")
	require.NoError(t, err)
	require.True(t, got.ReorderByMarket)

	// Default stays false for a normal chain.
	c2 := Chain{ID: "r2", TenantID: DefaultTenantID, Name: "plain", Strategy: "fallback",
		Steps: []ChainStep{{ID: "s2", Position: 0, Provider: "openai", Model: "gpt-4o"}}}
	require.NoError(t, repo.Create(ctx, c2))
	got2, err := repo.Get(ctx, "r2")
	require.NoError(t, err)
	require.False(t, got2.ReorderByMarket)

	// Update persists the flag both ways.
	c.ReorderByMarket = false
	require.NoError(t, repo.Update(ctx, c))
	got, err = repo.Get(ctx, "r1")
	require.NoError(t, err)
	require.False(t, got.ReorderByMarket)
	c2.ReorderByMarket = true
	require.NoError(t, repo.Update(ctx, c2))
	got2, err = repo.Get(ctx, "r2")
	require.NoError(t, err)
	require.True(t, got2.ReorderByMarket)

	// ListByTenant surfaces the flag too.
	list, err := repo.ListByTenant(ctx, DefaultTenantID)
	require.NoError(t, err)
	byID := map[string]bool{}
	for _, ch := range list {
		byID[ch.ID] = ch.ReorderByMarket
	}
	require.True(t, byID["r2"])
	require.False(t, byID["r1"])

	// UpdateRates must leave the flag untouched.
	require.NoError(t, repo.UpdateRates(ctx, "r1", 1, 1, 1, 1))
	got, err = repo.Get(ctx, "r1")
	require.NoError(t, err)
	require.False(t, got.ReorderByMarket)
}

func TestBudgetRepo_IncrementLimitOnTx_Overflow(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	b := Budget{
		ID: "ovf", TenantID: DefaultTenantID, ScopeKind: ScopeAPIKey, ScopeID: "key1",
		LimitMicros: math.MaxInt64 - 5, Period: "total", AlertPct: 80, HardCutoff: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	require.NoError(t, db.Budgets().Create(ctx, b))

	// A delta that would exceed MaxInt64 must be rejected before any write.
	tx, err := db.sql.BeginTx(ctx, nil)
	require.NoError(t, err)
	before, after, err := db.Budgets().IncrementLimitOnTx(ctx, tx, "ovf", 10)
	require.ErrorIs(t, err, ErrLimitOverflow)
	require.Equal(t, int64(math.MaxInt64-5), before)
	require.Equal(t, int64(math.MaxInt64-5), after)
	require.NoError(t, tx.Rollback())

	// The stored row must be unchanged: no partially-written state.
	got, err := db.Budgets().Get(ctx, "ovf")
	require.NoError(t, err)
	require.Equal(t, int64(math.MaxInt64-5), got.LimitMicros)
}
