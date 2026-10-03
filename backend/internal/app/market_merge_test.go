package app

import (
	"testing"

	"github.com/mydisha/keirouter/backend/internal/market"
	"github.com/mydisha/keirouter/backend/internal/meter"
	"github.com/mydisha/keirouter/backend/internal/store"
)

func TestApplyMarketBindingsOverridesCatalog(t *testing.T) {
	out := map[string]meter.Price{"ag/claude": {InputPerM: 99, OutputPerM: 99}}
	bindings := []store.MarketBinding{{ProviderID: "ag", ModelID: "claude", MarketSlug: "ag/claude"}}
	snap := []market.Model{{Slug: "ag/claude", MinAskIn: 1, MinAskOut: 5}}
	applyMarketBindings(t.Context(), out, bindings, snap, market.DefaultSettings(), 0.1, 1.25)
	if out["ag/claude"].InputPerM != 1.1 {
		t.Fatalf("expected market override, got %+v", out["ag/claude"])
	}
}

func TestApplyMarketBindingsKeepsPriceWhenSlugMissing(t *testing.T) {
	out := map[string]meter.Price{"ag/claude": {InputPerM: 99}}
	bindings := []store.MarketBinding{{ProviderID: "ag", ModelID: "claude", MarketSlug: "ag/missing"}}
	applyMarketBindings(t.Context(), out, bindings, nil, market.DefaultSettings(), 0.1, 1.25)
	if out["ag/claude"].InputPerM != 99 {
		t.Fatalf("expected unchanged, got %+v", out["ag/claude"])
	}
}
