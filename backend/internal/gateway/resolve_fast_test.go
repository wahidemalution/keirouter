package gateway

import (
	"testing"

	"github.com/mydisha/keirouter/backend/internal/dispatch"
	"github.com/mydisha/keirouter/backend/internal/market"
)

func TestStampSurplusFastBillsUpstreamCostWithProviderOrder(t *testing.T) {
	cache := market.NewSnapshotCache()
	cache.Replace([]market.Model{market.TrustedQuote{
		Found: true, BestIn: 0.0026, BestOut: 0.0156, CapIn: 0.008, CapOut: 0.048, MinDiscountPct: 96,
		Providers: []string{"bedrock", "google", "inferhub"}, ProviderOrder: []string{"google", "bedrock", "inferhub"},
	}.FastModel("m")})
	steps := []dispatch.Target{{Provider: "custom-openai-surplus", Model: "m"}}
	out := stampSurplusTrusted(steps, []string{"surplus-fast:m"}, cache, 20)
	if len(out) != 1 {
		t.Fatalf("steps = %d, want 1", len(out))
	}
	s := out[0]
	if s.MarketSlug != "surplus-fast:m" || !s.BillUpstreamCost || s.MinDiscountPct != 96 {
		t.Fatalf("step = %+v, want surplus-fast billed from upstream cost with ceiling 96", s)
	}
	if s.UpstreamCostMult < 1.1999 || s.UpstreamCostMult > 1.2001 {
		t.Fatalf("mult = %v, want 1.2", s.UpstreamCostMult)
	}
	if s.UpstreamProviders != "bedrock,google,inferhub" || s.UpstreamProviderOrder != "google,bedrock,inferhub" {
		t.Fatalf("providers = %q order = %q", s.UpstreamProviders, s.UpstreamProviderOrder)
	}
	if s.UpstreamRateIn != 0.008 || s.UpstreamRateOut != 0.048 {
		t.Fatalf("fallback = (%v,%v), want the band cap", s.UpstreamRateIn, s.UpstreamRateOut)
	}
	if s.FirstOutputTimeout != market.SurplusFastFirstOutputTimeout {
		t.Fatalf("first output timeout = %v, want %v", s.FirstOutputTimeout, market.SurplusFastFirstOutputTimeout)
	}
}

func TestStampSurplusTrustedKeepsDefaultFirstOutputTimeout(t *testing.T) {
	cache := market.NewSnapshotCache()
	cache.Replace([]market.Model{market.TrustedQuote{
		Found: true, BestIn: 0.0026, BestOut: 0.0156, Providers: []string{"bedrock"},
	}.Model("m")})
	steps := []dispatch.Target{{Provider: "custom-openai-surplus", Model: "m"}}
	out := stampSurplusTrusted(steps, []string{"surplus-trusted:m"}, cache, 0)
	if len(out) != 1 || out[0].FirstOutputTimeout != 0 {
		t.Fatalf("out = %+v, want one step with no first output timeout", out)
	}
}

func TestStampSurplusFastDropsStepWithoutQuote(t *testing.T) {
	steps := []dispatch.Target{{Provider: "custom-openai-surplus", Model: "m", MarketSlug: "surplus-fast:m"}}
	if out := stampSurplusTrusted(steps, nil, market.NewSnapshotCache(), 10); len(out) != 0 {
		t.Fatalf("out = %+v, want the unquoted step dropped", out)
	}
}

func TestDiscountFloorKeepsSurplusFastSteps(t *testing.T) {
	steps := []dispatch.Target{{MarketSlug: "surplus-fast:m", BillUpstreamCost: true, UpstreamCostMult: 1, MinDiscountPct: 10}}
	out, err := applyDiscountFloor(steps, 50, "m")
	if err != nil || len(out) != 1 || out[0].MinDiscountPct != 50 {
		t.Fatalf("out = %+v err = %v, want the step kept with floor 50", out, err)
	}
}

func fastQuoteCache() *market.SnapshotCache {
	cache := market.NewSnapshotCache()
	cache.Replace([]market.Model{market.TrustedQuote{
		Found: true, BestIn: 0.0026, BestOut: 0.0156, CapIn: 0.008, CapOut: 0.048, MinDiscountPct: 96,
		Providers: []string{"bedrock", "google", "inferhub"}, ProviderOrder: []string{"inferhub", "google", "bedrock"},
	}.FastModel("m")})
	return cache
}

func TestStampSurplusFastSkipsProvidersCoolingDown(t *testing.T) {
	cache := fastQuoteCache()
	cache.FastHealth().Observe("m", "inferhub", 0, false)
	steps := []dispatch.Target{{Provider: "custom-openai-surplus", Model: "m", MarketSlug: "surplus-fast:m"}}
	out := stampSurplusTrusted(steps, nil, cache, 0)
	if len(out) != 1 {
		t.Fatalf("steps = %d, want 1", len(out))
	}
	if out[0].UpstreamProviders != "bedrock,google" || out[0].UpstreamProviderOrder != "google,bedrock" {
		t.Fatalf("providers = %q order = %q, want inferhub left out", out[0].UpstreamProviders, out[0].UpstreamProviderOrder)
	}
}

func TestStampSurplusFastDropsStepWhenEveryProviderCoolsDown(t *testing.T) {
	cache := fastQuoteCache()
	for _, p := range []string{"bedrock", "google", "inferhub"} {
		cache.FastHealth().Observe("m", p, 0, false)
	}
	steps := []dispatch.Target{
		{Provider: "custom-openai-surplus", Model: "m", MarketSlug: "surplus-fast:m"},
		{Provider: "openai", Model: "gpt-4o"},
	}
	out := stampSurplusTrusted(steps, nil, cache, 0)
	if len(out) != 1 || out[0].Provider != "openai" {
		t.Fatalf("out = %+v, want only the next step", out)
	}
}
