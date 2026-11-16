package gateway

import (
	"context"
	"math"
	"testing"
	"time"

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

func orderCache() *market.SnapshotCache {
	cache := market.NewSnapshotCache()
	cache.Replace([]market.Model{
		{Slug: "dear", MinAskIn: 2, MinAskOut: 2, MaxAskIn: 2, MaxAskOut: 2},          // key 5.2
		{Slug: "cheap", MinAskIn: 0.1, MinAskOut: 0.1, MaxAskIn: 0.1, MaxAskOut: 0.1}, // key 0.26
		{Slug: "mid", MinAskIn: 1, MinAskOut: 1, MaxAskIn: 1, MaxAskOut: 1},           // key 2.6
	})
	return cache
}

func TestStaticStepSortedByCustomerPriceWhenReorderedByMarket(t *testing.T) {
	s := staticStep(nil) // (0.5+2) × 1.3 = 3.25
	s.Provider = "static"
	got := resolveStatic(t, store.Chain{ReorderByMarket: true, Steps: []store.ChainStep{
		{Provider: "dear", Model: "x", MarketSlug: "dear"},
		s,
		{Provider: "cheap", Model: "x", MarketSlug: "cheap"},
	}}, orderCache())
	require.Equal(t, []string{"cheap", "static", "dear"}, providers(got))
}

func TestStaticDiscountStepBeatsDearerMarketStep(t *testing.T) {
	s := staticStep(nil)
	s.Provider = "static"
	d := staticStep(nil)
	d.Provider, d.DiscountPercent = "disc", fptr(90) // (0.5+2) × 0.1 = 0.25
	got := resolveStatic(t, store.Chain{ReorderByMarket: true, Steps: []store.ChainStep{
		{Provider: "dear", Model: "x", MarketSlug: "dear"},
		s,
		{Provider: "cheap", Model: "x", MarketSlug: "cheap"},
		d,
	}}, orderCache())
	require.Equal(t, []string{"disc", "cheap", "static", "dear"}, providers(got))
}

func TestSluglessStepAdoptsChainSlugForOrdering(t *testing.T) {
	cache := market.NewSnapshotCache()
	cache.Replace([]market.Model{market.TrustedQuote{Found: true, BestIn: 0.1, BestOut: 0.1, CapIn: 0.2, CapOut: 0.2, MinDiscountPct: 50, Providers: []string{"zai"}}.Model("gpt-4o")}) // key 0.26
	s := staticStep(nil)
	s.Provider = "static"
	got := resolveStatic(t, store.Chain{
		ReorderByMarket: true,
		MarketSlugs:     []string{"surplus-trusted:gpt-4o"},
		Steps:           []store.ChainStep{s, {Provider: "surplus", Model: "gpt-4o"}},
	}, cache)
	require.Equal(t, []string{"surplus", "static"}, providers(got))
}

func TestStaticStepWithSlugSortsAsStatic(t *testing.T) {
	s := staticStep(nil)
	s.Provider, s.MarketSlug = "static", "cheap"
	got := resolveStatic(t, store.Chain{ReorderByMarket: true, Steps: []store.ChainStep{
		s,
		{Provider: "mid", Model: "x", MarketSlug: "mid"},
	}}, orderCache())
	require.Equal(t, []string{"mid", "static"}, providers(got))
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

func TestStaticStepIsNotDiscountCapable(t *testing.T) {
	c := store.Chain{
		MarketSlugs: []string{"surplus-trusted:gpt-4o"},
		Steps:       []store.ChainStep{staticStep(nil)},
	}
	require.Zero(t, chainMaxGuaranteedDiscount(c), "a static step cannot honour a discount floor")
	c.Steps = append(c.Steps, store.ChainStep{Provider: "openai", Model: "gpt-4o"})
	require.Equal(t, 100, chainMaxGuaranteedDiscount(c), "a slugless step on the trusted model still adopts the chain slug")
}

func TestStaticDiscountStepKeptByDiscountFloor(t *testing.T) {
	s := staticStep(nil)
	s.DiscountPercent = fptr(90)
	got := resolveStatic(t, store.Chain{Steps: []store.ChainStep{s}}, nil)
	kept, err := applyDiscountFloor(got, 80, "static")
	require.NoError(t, err)
	require.Len(t, kept, 1)
	require.Zero(t, kept[0].MinDiscountPct, "no X-Min-Discount for a static step")
	kept, err = applyDiscountFloor(got, 90, "static")
	require.NoError(t, err)
	require.Len(t, kept, 1, "exactly at the floor is kept")
	_, err = applyDiscountFloor(got, 91, "static")
	require.Error(t, err)
}

func TestStaticDiscountStepIsDiscountCapable(t *testing.T) {
	s := staticStep(nil)
	s.DiscountPercent = fptr(90)
	require.Equal(t, 90, chainMaxGuaranteedDiscount(store.Chain{Steps: []store.ChainStep{s}}))
	s.DiscountPercent = fptr(0)
	require.Zero(t, chainMaxGuaranteedDiscount(store.Chain{Steps: []store.ChainStep{s}}))
}

func TestChainMaxGuaranteedDiscount(t *testing.T) {
	disc := func(d float64) store.ChainStep {
		s := staticStep(nil)
		s.DiscountPercent = fptr(d)
		return s
	}
	plain := store.ChainStep{Provider: "openai", Model: "gpt-4o"}
	trusted := store.ChainStep{Provider: "surplus", Model: "glm", MarketSlug: "surplus-trusted:glm"}
	cases := []struct {
		name  string
		steps []store.ChainStep
		want  int
	}{
		{"none", []store.ChainStep{plain}, 0},
		{"static 90 and 50", []store.ChainStep{disc(50), disc(90), plain}, 90},
		{"static 10 and trusted", []store.ChainStep{disc(10), trusted}, 100},
		{"static zero", []store.ChainStep{disc(0)}, 0},
		{"fraction floors down", []store.ChainStep{disc(12.9)}, 12},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, chainMaxGuaranteedDiscount(store.Chain{Steps: tc.steps}))
		})
	}
}

func TestApplyDiscountFloorMixedStaticAndSurplus(t *testing.T) {
	static := func(provider string, d float64) dispatch.Target {
		return dispatch.Target{Provider: provider, Model: "m", StaticInputPerM: 1, StaticOutputPerM: 2, DiscountSet: true, DiscountPercent: d}
	}
	in := []dispatch.Target{
		static("s90", 90), static("s50", 50), trustedStep("glm", 1.2, 0), {Provider: "plain", Model: "m"},
	}
	out, err := applyDiscountFloor(in, 80, "chain-x")
	require.NoError(t, err)
	require.Equal(t, []string{"s90", "surplus"}, providers(out))
}

func TestStaticDiscountBelowKeyFloorRunsAtSetPrice(t *testing.T) {
	db, gw := newPublicTestGatewayWithDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	require.NoError(t, db.Chains().Create(ctx, store.Chain{
		ID: "mix", TenantID: store.DefaultTenantID, Name: "mix", Strategy: "priority", InputPerM: 1, OutputPerM: 2,
		Steps: []store.ChainStep{
			{ID: "s1", Provider: "openai", Model: "gpt-4o", StaticInputPerM: 1, StaticOutputPerM: 2, DiscountPercent: fptr(10), Position: 0},
			{ID: "s2", Provider: "openai", Model: "gpt-4o-mini", Position: 1},
		},
		CreatedAt: now, UpdatedAt: now,
	}))
	gw.marketCache = market.NewSnapshotCache()
	keyed := context.WithValue(ctx, apiKeyCtxKey, store.APIKey{ID: "k", MinDiscountPct: 50})
	res, err := gw.resolveTargets(keyed, store.DefaultTenantID, "mix")
	require.NoError(t, err, "floor above the chain's best static discount does not apply")
	require.Len(t, res.Targets, 2)
	require.Equal(t, 10, res.MaxDiscountPct)
}
