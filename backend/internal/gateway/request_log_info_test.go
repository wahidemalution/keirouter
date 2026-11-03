package gateway

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/market"
	"github.com/mydisha/keirouter/backend/internal/store"
)

func TestRequestDiscountPct(t *testing.T) {
	official := officialPrice{InputPerM: 1, OutputPerM: 4}
	// 1M in + 1M out at list = $5; billed $1 → 80% off.
	rec := store.RecentRecord{PromptTokens: 1_000_000, CompletionTokens: 1_000_000, CostNanos: 1_000_000_000}
	require.Equal(t, 80, *requestDiscountPct(rec, official))

	// Cached input lists at the cache-read rate: 1M cached = $1 × CacheReadMult.
	cached := store.RecentRecord{PromptTokens: 1_000_000, CachedTokens: 1_000_000, CostNanos: int64(market.CacheReadMult * 0.5 * 1e9)}
	require.Equal(t, 50, *requestDiscountPct(cached, official))

	pricier := store.RecentRecord{PromptTokens: 1_000_000, CostNanos: 2_000_000_000}
	require.Equal(t, 0, *requestDiscountPct(pricier, official), "never below zero")

	require.Nil(t, requestDiscountPct(store.RecentRecord{PromptTokens: 10}, official), "nothing billed")
}

func TestRequestLogProviderName(t *testing.T) {
	cache := market.NewSnapshotCache()
	cache.MergeProviderLabels(map[string]string{"deepseek": "DeepSeek"})
	s := &Server{marketCache: cache}
	info := requestLogInfo{s: s, names: map[string]string{"custom-openai-surplus": "Surplus Main"}, official: map[string]*officialPrice{},
		chains: map[string]store.Chain{"c1": {ID: "c1", Steps: []store.ChainStep{
			{Provider: "custom-openai-surplus-deepseek", MarketSlug: "surplus-trusted@deepseek:m"},
		}}}}
	require.Equal(t, "DeepSeek", info.providerName(store.RecentRecord{ChainID: "c1", Provider: "custom-openai-surplus-deepseek"}))
	require.Equal(t, "Surplus Main", info.providerName(store.RecentRecord{ChainID: "c1", Provider: "custom-openai-surplus"}))
}
