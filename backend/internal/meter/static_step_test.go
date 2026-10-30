package meter

import (
	"testing"

	"github.com/mydisha/keirouter/backend/internal/core"
)

// staticStepEvent mimics a static-price step: provider price 0.5/2 per 1M,
// billed with a 20% markup.
func staticStepEvent(u core.Usage) Event {
	return Event{
		StaticPrice: true, Provider: "openai", Model: "gpt-4o",
		// Chain rates are present too, as for any chain step; the static rates win.
		InputPerM: 9, OutputPerM: 9,
		MarketRateIn: 0.6, MarketRateOut: 2.4, MarketCacheReadRate: 0.06, MarketCacheWriteRate: 0.75,
		UpstreamRateIn: 0.5, UpstreamRateOut: 2, UpstreamCacheReadRate: 0.05, UpstreamCacheWriteRate: 0.625,
		Usage: u,
	}
}

func TestStaticStepBillsMarkedUpRateAndCostsRawRate(t *testing.T) {
	ev := staticStepEvent(core.Usage{PromptTokens: 1_000_000, CompletionTokens: 1_000_000})
	m := &Meter{}
	user := m.costForEvent(ev)
	up := upstreamCostForEvent(ev)
	if user.CostMicros != 3_000_000 {
		t.Fatalf("user = %d micros, want 0.6+2.4 USD = 3_000_000", user.CostMicros)
	}
	if up.CostMicros != 2_500_000 {
		t.Fatalf("upstream = %d micros, want 0.5+2 USD = 2_500_000", up.CostMicros)
	}
	if user.Pricing.Source != "static_step" {
		t.Fatalf("pricing source = %q, want static_step", user.Pricing.Source)
	}
}

func TestStaticStepCachedInputUsesCacheReadRate(t *testing.T) {
	ev := staticStepEvent(core.Usage{PromptTokens: 1_000_000, CachedTokens: 1_000_000})
	m := &Meter{}
	if got := m.costForEvent(ev).CostMicros; got != 60_000 {
		t.Fatalf("user = %d micros, want 1M cached × 0.06 = 60_000", got)
	}
	if got := upstreamCostForEvent(ev).CostMicros; got != 50_000 {
		t.Fatalf("upstream = %d micros, want 1M cached × 0.05 = 50_000", got)
	}
}
