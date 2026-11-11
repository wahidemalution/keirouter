package gateway

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/market"
	"github.com/mydisha/keirouter/backend/internal/store"
)

func ptrFloat(v float64) *float64 { return &v }

// fallbackPriceChain has one surplus step with no live quote, so a key with
// the fallback on is served by the fallback alone.
func fallbackPriceChain(name string, mutate func(*store.Chain)) store.Chain {
	now := time.Now().UTC()
	c := store.Chain{
		ID: name, TenantID: store.DefaultTenantID, Name: name, Strategy: "priority",
		InputPerM: 0.02, OutputPerM: 0.2, MarketSlugs: []string{"surplus-trusted:glm-5.3"},
		FallbackProvider: "openai", FallbackModel: "gpt-4o",
		Steps:     []store.ChainStep{{ID: name + "-s1", Provider: "custom-openai-surplus", Model: "glm-5.3", MarketSlug: "surplus-trusted:glm-5.3", Position: 0}},
		CreatedAt: now, UpdatedAt: now,
	}
	if mutate != nil {
		mutate(&c)
	}
	return c
}

func fallbackOnKey(model string) context.Context {
	return context.WithValue(context.Background(), apiKeyCtxKey,
		store.APIKey{ID: "k", ModelFinalFallback: map[string]int{model: store.FinalFallbackChosen}})
}

func resolvedFallback(t *testing.T, gw *Server, model string) resolveResultTarget {
	t.Helper()
	res, err := gw.resolveTargets(fallbackOnKey(model), store.DefaultTenantID, model)
	require.NoError(t, err)
	last := res.Targets[len(res.Targets)-1]
	require.True(t, last.ChainFallback)
	require.True(t, last.StaticPrice, "billed from its own rates, not the chain price")
	return resolveResultTarget{in: last.MarketRateIn, out: last.MarketRateOut, cacheRead: last.MarketCacheReadRate,
		costIn: last.UpstreamRateIn, costOut: last.UpstreamRateOut}
}

type resolveResultTarget struct{ in, out, cacheRead, costIn, costOut float64 }

func TestChainFallbackManualPriceWithMarkup(t *testing.T) {
	db, gw := newPublicTestGatewayWithDB(t)
	gw.marketCache = market.NewSnapshotCache()
	require.NoError(t, db.Chains().Create(context.Background(), fallbackPriceChain("manual", func(c *store.Chain) {
		c.FallbackInputPerM, c.FallbackOutputPerM = 2.5, 10
		c.FallbackCacheReadPerM = ptrFloat(1.25)
		c.FallbackMarkupPercent = ptrFloat(20)
	})))

	got := resolvedFallback(t, gw, "manual")
	require.InDelta(t, 3.0, got.in, 1e-9)
	require.InDelta(t, 12.0, got.out, 1e-9)
	require.InDelta(t, 1.5, got.cacheRead, 1e-9)
	require.Equal(t, 2.5, got.costIn)
	require.Equal(t, 10.0, got.costOut)
}

// Without a stored price the models.dev price bills the fallback, with no
// markup even though the global markup is 10%.
func TestChainFallbackModelsDevPriceNoGlobalMarkup(t *testing.T) {
	db, gw := newPublicTestGatewayWithDB(t)
	gw.marketCache = market.NewSnapshotCache()
	refs := market.NewReferenceCache()
	refs.Replace(map[string]market.Reference{"gpt-4o": {InputPerM: 2.5, OutputPerM: 10}})
	gw.references = refs
	require.NoError(t, db.Chains().Create(context.Background(), fallbackPriceChain("auto", nil)))

	got := resolvedFallback(t, gw, "auto")
	require.Equal(t, 2.5, got.in)
	require.Equal(t, 10.0, got.out)
	require.Equal(t, 2.5, got.cacheRead, "an empty cache read rate bills the full input price")
	require.Equal(t, got.in, got.costIn)
}

// With no stored price and none on models.dev the fallback never runs.
func TestChainFallbackWithoutPriceDoesNotRun(t *testing.T) {
	db, gw := newPublicTestGatewayWithDB(t)
	gw.marketCache = market.NewSnapshotCache()
	ctx := context.Background()
	require.NoError(t, db.Chains().Create(ctx, fallbackPriceChain("unpriced", nil)))

	_, err := gw.resolveTargets(fallbackOnKey("unpriced"), store.DefaultTenantID, "unpriced")
	var bad badModelError
	require.ErrorAs(t, err, &bad)
	require.True(t, bad.fallbackUnpriced)
	require.Equal(t, http.StatusServiceUnavailable, bad.HTTPStatus())

	// A chain with a working step keeps routing; only the fallback is dropped.
	now := time.Now().UTC()
	require.NoError(t, db.Chains().Create(ctx, store.Chain{
		ID: "plain", TenantID: store.DefaultTenantID, Name: "plain", Strategy: "priority", InputPerM: 1, OutputPerM: 2,
		FallbackProvider: "openai", FallbackModel: "gpt-4o",
		Steps:     []store.ChainStep{{ID: "p1", Provider: "anthropic", Model: "claude-sonnet-4", Position: 0}},
		CreatedAt: now, UpdatedAt: now,
	}))
	res, err := gw.resolveTargets(fallbackOnKey("plain"), store.DefaultTenantID, "plain")
	require.NoError(t, err)
	require.Len(t, res.Targets, 1)
	require.False(t, res.Targets[0].ChainFallback)
	require.Zero(t, res.PlanOpts.PinnedTail)
}

func TestChainFallbackPriceInputValidation(t *testing.T) {
	var c store.Chain
	require.NotEmpty(t, chainFallbackPriceInput{InputPerM: 1}.applyTo(&c), "input without output")
	require.NotEmpty(t, chainFallbackPriceInput{InputPerM: 1, OutputPerM: 2, MarkupPercent: ptrFloat(1001)}.applyTo(&c))
	require.NotEmpty(t, chainFallbackPriceInput{InputPerM: 1, OutputPerM: 2, CacheReadPerM: ptrFloat(-1)}.applyTo(&c))
	require.Empty(t, chainFallbackPriceInput{}.applyTo(&c), "both empty: models.dev price")
	require.Empty(t, chainFallbackPriceInput{InputPerM: 1, OutputPerM: 2, MarkupPercent: ptrFloat(0)}.applyTo(&c))
	require.Equal(t, 1.0, c.FallbackInputPerM)
	require.Equal(t, 0.0, *c.FallbackMarkupPercent)
}

func TestChainFallbackPriceStoreRoundTrip(t *testing.T) {
	db, _ := newPublicTestGatewayWithDB(t)
	ctx := context.Background()
	c := fallbackPriceChain("rt", func(c *store.Chain) {
		c.FallbackInputPerM, c.FallbackOutputPerM = 2.5, 10
		c.FallbackCacheWritePerM = ptrFloat(3.125)
		c.FallbackMarkupPercent = ptrFloat(15)
	})
	require.NoError(t, db.Chains().Create(ctx, c))
	got, err := db.Chains().Get(ctx, "rt")
	require.NoError(t, err)
	require.Equal(t, 2.5, got.FallbackInputPerM)
	require.Equal(t, 10.0, got.FallbackOutputPerM)
	require.Nil(t, got.FallbackCacheReadPerM)
	require.Equal(t, 3.125, *got.FallbackCacheWritePerM)
	require.Equal(t, 15.0, *got.FallbackMarkupPercent)

	got.FallbackMarkupPercent = nil
	require.NoError(t, db.Chains().Update(ctx, got))
	list, err := db.Chains().ListByTenant(ctx, store.DefaultTenantID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Nil(t, list[0].FallbackMarkupPercent)
	require.Equal(t, 2.5, list[0].FallbackInputPerM)
}
