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
