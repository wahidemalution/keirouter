package gateway

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/core"
	"github.com/mydisha/keirouter/backend/internal/meter"
	"github.com/mydisha/keirouter/backend/internal/store"
)

func discountStep(discount float64, costMult *float64) store.ChainStep {
	return store.ChainStep{Provider: "openai", Model: "gpt-4o", StaticInputPerM: 0.28, StaticOutputPerM: 0.42,
		StaticCacheReadPerM: fptr(0.028), StaticCacheWritePerM: fptr(0.35), DiscountPercent: fptr(discount), CostMultiplier: costMult}
}

// The customer pays exactly list × (100 − discount)/100 on every rate, and
// neither the chain nor the global markup (30 in resolveStatic) applies.
func TestDiscountStepBillsListLessDiscount(t *testing.T) {
	got := resolveStatic(t, store.Chain{MarkupPercent: fptr(20), Steps: []store.ChainStep{discountStep(90, fptr(0.01))}}, nil)
	require.Len(t, got, 1)
	s := got[0]
	require.True(t, s.StaticPrice)
	near(t, 0.028, s.MarketRateIn, "billable in")
	near(t, 0.042, s.MarketRateOut, "billable out")
	near(t, 0.0028, s.MarketCacheReadRate, "billable cache read")
	near(t, 0.035, s.MarketCacheWriteRate, "billable cache write")
	near(t, 0.042, s.MarketReasoningRate, "billable reasoning")
	near(t, 0.0028, s.UpstreamRateIn, "cost in")
	near(t, 0.0042, s.UpstreamRateOut, "cost out")
	near(t, 0.00028, s.UpstreamCacheReadRate, "cost cache read")
	near(t, 0.0035, s.UpstreamCacheWriteRate, "cost cache write")
	near(t, 0.0042, s.UpstreamReasoningRate, "cost reasoning")
}

func TestDiscountStepFactorIsExact(t *testing.T) {
	for _, tc := range []struct{ discount, factor float64 }{
		{0, 1}, {10, 0.9}, {50, 0.5}, {90, 0.1}, {99, 0.01}, {99.5, 0.005}, {33.3, 0.667},
	} {
		got := resolveStatic(t, store.Chain{Steps: []store.ChainStep{{Provider: "openai", Model: "gpt-4o",
			StaticInputPerM: 1, StaticOutputPerM: 1, DiscountPercent: fptr(tc.discount)}}}, nil)
		require.InDelta(t, tc.factor, got[0].MarketRateIn, 1e-15, "discount %v", tc.discount)
	}
}

func TestDiscountStepCostDefaultsToCustomerPrice(t *testing.T) {
	got := resolveStatic(t, store.Chain{Steps: []store.ChainStep{discountStep(90, nil)}}, nil)
	s := got[0]
	near(t, s.MarketRateIn, s.UpstreamRateIn, "cost in")
	near(t, s.MarketRateOut, s.UpstreamRateOut, "cost out")
	near(t, s.MarketCacheReadRate, s.UpstreamCacheReadRate, "cost cache read")
	near(t, s.MarketCacheWriteRate, s.UpstreamCacheWriteRate, "cost cache write")
}

// Empty cache prices derive from the list input price before the discount.
func TestDiscountStepDerivedCache(t *testing.T) {
	got := resolveStatic(t, store.Chain{Steps: []store.ChainStep{{Provider: "openai", Model: "gpt-4o",
		StaticInputPerM: 2, StaticOutputPerM: 8, DiscountPercent: fptr(90)}}}, nil)
	near(t, 0.02, got[0].MarketCacheReadRate, "billable cache read = 2 × 0.1 × 0.1")
	near(t, 0.25, got[0].MarketCacheWriteRate, "billable cache write = 2 × 1.25 × 0.1")
}

// A real request mix on a 90% discount step bills exactly one tenth of what
// the same tokens cost at the models.dev list price.
func TestDiscountStepRequestBillsTenthOfList(t *testing.T) {
	s := resolveStatic(t, store.Chain{Steps: []store.ChainStep{discountStep(90, fptr(0.01))}}, nil)[0]
	u := core.Usage{PromptTokens: 1_000_000, CachedTokens: 400_000, CacheWriteTokens: 100_000, CompletionTokens: 200_000}
	ev := meter.Event{
		Provider: s.Provider, Model: s.Model, StaticPrice: s.StaticPrice,
		MarketRateIn: s.MarketRateIn, MarketRateOut: s.MarketRateOut,
		MarketCacheReadRate: s.MarketCacheReadRate, MarketCacheWriteRate: s.MarketCacheWriteRate,
		MarketReasoningRate: s.MarketReasoningRate, Usage: u,
	}
	// List: 500k×0.28 + 400k×0.028 + 100k×0.35 + 200k×0.42 per 1M = 0.2702 USD.
	got := (&meter.Meter{}).QuoteMicros(ev)
	require.Equal(t, int64(27_020), got, "0.2702 USD × 0.1")
}
