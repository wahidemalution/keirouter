package gateway

import (
	"context"
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/dispatch"
	"github.com/mydisha/keirouter/backend/internal/market"
	"github.com/mydisha/keirouter/backend/internal/store"
)

func fptr(v float64) *float64 { return &v }

func near(t *testing.T, want, got float64, what string) {
	t.Helper()
	if math.Abs(want-got) > 1e-9 {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

// resolveStatic resolves a one-chain tenant with global markup 30.
func resolveStatic(t *testing.T, c store.Chain, cache *market.SnapshotCache) []dispatch.Target {
	t.Helper()
	c.ID, c.Name = "c-static", "static"
	res, err := resolveTargetsWith(context.Background(), &fakeChains{chains: []store.Chain{c}}, &fakeAliases{}, nil, cache, 30, "t1", "chain:static")
	require.NoError(t, err)
	return res.Targets
}

func staticStep(markup *float64) store.ChainStep {
	return store.ChainStep{Provider: "openai", Model: "gpt-4o", StaticInputPerM: 0.5, StaticOutputPerM: 2, MarkupPercent: markup}
}

func TestStaticStepRatesStepMarkup(t *testing.T) {
	got := resolveStatic(t, store.Chain{Steps: []store.ChainStep{staticStep(fptr(10))}}, nil)
	require.Len(t, got, 1)
	s := got[0]
	require.True(t, s.StaticPrice)
	require.Empty(t, s.MarketSlug)
	near(t, 0.55, s.MarketRateIn, "billable in")
	near(t, 2.2, s.MarketRateOut, "billable out")
	near(t, 0.055, s.MarketCacheReadRate, "billable cache read")
	near(t, 0.6875, s.MarketCacheWriteRate, "billable cache write")
	near(t, 2.2, s.MarketReasoningRate, "billable reasoning")
	near(t, 0.5, s.UpstreamRateIn, "cost in")
	near(t, 2, s.UpstreamRateOut, "cost out")
	near(t, 0.05, s.UpstreamCacheReadRate, "cost cache read")
	near(t, 0.625, s.UpstreamCacheWriteRate, "cost cache write")
}

func TestStaticStepMarkupPrecedence(t *testing.T) {
	cases := []struct {
		name         string
		step, chain  *float64
		wantBilledIn float64
	}{
		{"global", nil, nil, 0.65},
		{"chain explicit zero", nil, fptr(0), 0.5},
		{"step zero beats chain", fptr(0), fptr(20), 0.5},
		{"step beats chain", fptr(10), fptr(20), 0.55},
		{"chain beats global", nil, fptr(20), 0.6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveStatic(t, store.Chain{MarkupPercent: tc.chain, Steps: []store.ChainStep{staticStep(tc.step)}}, nil)
			near(t, tc.wantBilledIn, got[0].MarketRateIn, "billable in")
			near(t, 0.5, got[0].UpstreamRateIn, "cost in")
		})
	}
}

func TestStaticStepCacheOverride(t *testing.T) {
	st := staticStep(fptr(10))
	st.StaticCacheReadPerM = fptr(0.2)
	got := resolveStatic(t, store.Chain{Steps: []store.ChainStep{st}}, nil)
	near(t, 0.2, got[0].UpstreamCacheReadRate, "cost cache read")
	near(t, 0.22, got[0].MarketCacheReadRate, "billable cache read")
	near(t, 0.625, got[0].UpstreamCacheWriteRate, "cost cache write stays derived")
}

func TestStaticStepNotAdoptedBySurplusSlug(t *testing.T) {
	cache := market.NewSnapshotCache()
	cache.Replace([]market.Model{market.TrustedQuote{Found: true, BestIn: 1, BestOut: 2, CapIn: 3, CapOut: 4, MinDiscountPct: 50, Providers: []string{"zai"}}.Model("gpt-4o")})
	got := resolveStatic(t, store.Chain{
		MarketSlugs: []string{"surplus-trusted:gpt-4o"},
		Steps:       []store.ChainStep{staticStep(nil)},
	}, cache)
	require.Len(t, got, 1)
	require.True(t, got[0].StaticPrice)
	require.False(t, got[0].BillUpstreamCost)
	require.Empty(t, got[0].MarketSlug)
	near(t, 0.65, got[0].MarketRateIn, "billable in")
}

func TestStaticStepKeepsPositionWhenReorderedByMarket(t *testing.T) {
	cache := market.NewSnapshotCache()
	cache.Replace([]market.Model{
		{Slug: "dear", MinAskIn: 0.02, MinAskOut: 0.02, MaxAskIn: 0.02, MaxAskOut: 0.02},
		{Slug: "cheap", MinAskIn: 0.001, MinAskOut: 0.001, MaxAskIn: 0.001, MaxAskOut: 0.001},
	})
	s := staticStep(nil)
	s.Provider = "static"
	got := resolveStatic(t, store.Chain{ReorderByMarket: true, Steps: []store.ChainStep{
		{Provider: "dear", Model: "x", MarketSlug: "dear"},
		s,
		{Provider: "cheap", Model: "x", MarketSlug: "cheap"},
	}}, cache)
	require.Equal(t, []string{"cheap", "static", "dear"}, providers(got))
}

func TestStaticStepKeepsPositionUnderCostStrategy(t *testing.T) {
	s := staticStep(nil)
	s.Provider, s.Model = "static", "backup"
	got := resolveStatic(t, store.Chain{Strategy: "cost", Steps: []store.ChainStep{
		{Provider: "openai", Model: "gpt-4o"},
		s,
		{Provider: "deepseek", Model: "deepseek-chat"},
	}}, nil)
	require.Equal(t, []string{"deepseek", "static", "openai"}, providers(got))
}

func TestStaticStepDroppedByDiscountFloor(t *testing.T) {
	got := resolveStatic(t, store.Chain{Steps: []store.ChainStep{staticStep(nil)}}, nil)
	_, err := applyDiscountFloor(got, 10, "static")
	require.Error(t, err)
}
