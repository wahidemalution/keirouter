package gateway

import (
	"testing"

	"github.com/mydisha/keirouter/backend/internal/dispatch"
	"github.com/mydisha/keirouter/backend/internal/market"
)

func TestOrderStepsByMarketCheapestFirstAndStampsRates(t *testing.T) {
	cache := market.NewSnapshotCache()
	cache.Replace([]market.Model{
		{Slug: "p1", MinAskIn: 0.02, MinAskOut: 0.01}, // sum 0.03
		{Slug: "p2", MinAskIn: 0.005, MinAskOut: 0.005}, // sum 0.01 cheapest
		{Slug: "p3", MinAskIn: 0.01, MinAskOut: 0.01}, // sum 0.02
	})
	// Declared non-ascending: p1(3), U(unpriced, middle), p2(1), p3(2).
	// Requires >=2 moves and must sink U last.
	steps := []dispatch.Target{
		{Provider: "p1", Model: "x", MarketSlug: "p1"},
		{Provider: "plain", Model: "y"}, // no slug, interleaved middle
		{Provider: "p2", Model: "x", MarketSlug: "p2"},
		{Provider: "p3", Model: "x", MarketSlug: "p3"},
	}
	out := orderStepsByMarket(steps, cache, 10 /*markupPercent*/)
	want := []string{"p2", "p3", "p1", "plain"}
	for i, w := range want {
		if out[i].Provider != w {
			t.Fatalf("out[%d] = %s, want %s (cheapest-first, slugless last); full=%v",
				i, out[i].Provider, w, providers(out))
		}
	}
	// Markup 10% on p2: in 0.0055, out 0.0055.
	if got := out[0].MarketRateIn; got < 0.00549 || got > 0.00551 {
		t.Fatalf("p2 rate in = %v, want ~0.0055", got)
	}
	if got := out[0].MarketRateOut; got < 0.00549 || got > 0.00551 {
		t.Fatalf("p2 rate out = %v, want ~0.0055", got)
	}
	if out[3].MarketRateIn != 0 || out[3].MarketRateOut != 0 {
		t.Fatalf("slugless step must carry no market rate")
	}
}

// TestOrderStepsByMarketReproduction pins the exact defect reproduction from
// review C1: declared [p1(3), U, p2(1), p3(2)] must yield [p2, p3, p1, U].
func TestOrderStepsByMarketReproduction(t *testing.T) {
	cache := market.NewSnapshotCache()
	cache.Replace([]market.Model{
		{Slug: "p1", MinAskIn: 0.02, MinAskOut: 0.01}, // sum 0.03
		{Slug: "p2", MinAskIn: 0.005, MinAskOut: 0.005}, // sum 0.01
		{Slug: "p3", MinAskIn: 0.01, MinAskOut: 0.01}, // sum 0.02
	})
	steps := []dispatch.Target{
		{Provider: "p1", Model: "x", MarketSlug: "p1"},
		{Provider: "U", Model: "y"},
		{Provider: "p2", Model: "x", MarketSlug: "p2"},
		{Provider: "p3", Model: "x", MarketSlug: "p3"},
	}
	out := orderStepsByMarket(steps, cache, 0)
	want := []string{"p2", "p3", "p1", "U"}
	for i, w := range want {
		if out[i].Provider != w {
			t.Fatalf("out[%d] = %s, want %s; full=%v", i, out[i].Provider, w, providers(out))
		}
	}
}

func providers(ts []dispatch.Target) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Provider
	}
	return out
}

func TestOrderStepsByMarketIdentityWhenNoSlugs(t *testing.T) {
	steps := []dispatch.Target{{Provider: "a", Model: "1"}, {Provider: "b", Model: "2"}}
	out := orderStepsByMarket(steps, market.NewSnapshotCache(), 10)
	if out[0].Provider != "a" || out[1].Provider != "b" {
		t.Fatalf("order changed with empty cache: %+v", out)
	}
}
