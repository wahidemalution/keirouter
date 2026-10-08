package market

import (
	"math"
	"testing"
)

func TestComputeChainRateMinIndependent(t *testing.T) {
	models := []Model{
		{Slug: "a", MinAskIn: 1, MinAskOut: 5},
		{Slug: "b", MinAskIn: 2, MinAskOut: 3},
	}
	rate, ok := ComputeChainRate([]string{"a", "b"}, models, 0, 0.1, 1.25)
	if !ok {
		t.Fatal("expected match")
	}
	if rate.InputPerM != 1 {
		t.Fatalf("input = %v, want 1 (slug a)", rate.InputPerM)
	}
	if rate.OutputPerM != 3 {
		t.Fatalf("output = %v, want 3 (slug b)", rate.OutputPerM)
	}
	if rate.CachedInputPerM != 0.1 || rate.CacheWritePerM != 1.25 {
		t.Fatalf("cache = (%v,%v), want (0.1,1.25)", rate.CachedInputPerM, rate.CacheWritePerM)
	}
}

func TestComputeChainRateMarkup(t *testing.T) {
	models := []Model{{Slug: "a", MinAskIn: 1, MinAskOut: 2}}
	rate, ok := ComputeChainRate([]string{"a"}, models, 20, 0.1, 1.25)
	if !ok || rate.InputPerM != 1.2 || rate.OutputPerM != 2.4 {
		t.Fatalf("got %+v ok=%v, want in=1.2 out=2.4", rate, ok)
	}
}

func TestComputeChainRateMissingSlugs(t *testing.T) {
	models := []Model{{Slug: "a", MinAskIn: 1, MinAskOut: 1}}
	if _, ok := ComputeChainRate([]string{"x"}, models, 0, 0.1, 1.25); ok {
		t.Fatal("unknown slug must return false")
	}
	if _, ok := ComputeChainRate(nil, models, 0, 0.1, 1.25); ok {
		t.Fatal("empty slugs must return false")
	}
}

func TestComputeChainRateRejectsNonFinite(t *testing.T) {
	for _, tc := range []struct {
		name string
		m    Model
	}{
		{"nan input", Model{Slug: "a", MinAskIn: math.NaN(), MinAskOut: 1}},
		{"inf input", Model{Slug: "a", MinAskIn: math.Inf(1), MinAskOut: 1}},
		{"nan output", Model{Slug: "a", MinAskIn: 1, MinAskOut: math.NaN()}},
		{"inf output", Model{Slug: "a", MinAskIn: 1, MinAskOut: math.Inf(1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := ComputeChainRate([]string{"a"}, []Model{tc.m}, 0, 0.1, 1.25); ok {
				t.Fatal("non-finite ask must return false")
			}
		})
	}
}

func TestComputeChainRateSkipsZeroOrNegativeAsk(t *testing.T) {
	// Only slug has a zero ask: must not price the chain at 0.
	zero := []Model{{Slug: "a", MinAskIn: 0, MinAskOut: 5}}
	if _, ok := ComputeChainRate([]string{"a"}, zero, 0, 0.1, 1.25); ok {
		t.Fatal("zero MinAskIn must be skipped, leaving chain unpriced")
	}
	zeroOut := []Model{{Slug: "a", MinAskIn: 5, MinAskOut: 0}}
	if _, ok := ComputeChainRate([]string{"a"}, zeroOut, 0, 0.1, 1.25); ok {
		t.Fatal("zero MinAskOut must be skipped, leaving chain unpriced")
	}
	negative := []Model{{Slug: "a", MinAskIn: -1, MinAskOut: 5}}
	if _, ok := ComputeChainRate([]string{"a"}, negative, 0, 0.1, 1.25); ok {
		t.Fatal("negative ask must be skipped, leaving chain unpriced")
	}

	// A zeroed slug alongside a valid one is simply ignored.
	mixed := []Model{
		{Slug: "a", MinAskIn: 0, MinAskOut: 0},
		{Slug: "b", MinAskIn: 2, MinAskOut: 3},
	}
	rate, ok := ComputeChainRate([]string{"a", "b"}, mixed, 0, 0.1, 1.25)
	if !ok {
		t.Fatal("expected valid slug to price the chain")
	}
	if rate.InputPerM != 2 || rate.OutputPerM != 3 {
		t.Fatalf("price = (%v,%v), want (2,3)", rate.InputPerM, rate.OutputPerM)
	}
}

func TestComputeChainRateUsesPublishedCacheRates(t *testing.T) {
	models := []Model{{Slug: "surplus:m", MinAskIn: 0.12, MinAskOut: 0.48, CacheRead: 0.02, CacheWrite: 0.15}}
	rate, ok := ComputeChainRate([]string{"surplus:m"}, models, 0, 0.1, 1.25)
	if !ok {
		t.Fatal("expected match")
	}
	if rate.CachedInputPerM != 0.02 || rate.CacheWritePerM != 0.15 {
		t.Fatalf("cache = (%v,%v), want published (0.02,0.15)", rate.CachedInputPerM, rate.CacheWritePerM)
	}
}

func TestComputeChainRateDerivesCacheWhenUnpublished(t *testing.T) {
	models := []Model{{Slug: "a", MinAskIn: 1, MinAskOut: 2}}
	rate, ok := ComputeChainRate([]string{"a"}, models, 0, 0.1, 1.25)
	if !ok {
		t.Fatal("expected match")
	}
	if rate.CachedInputPerM != 0.1 || rate.CacheWritePerM != 1.25 {
		t.Fatalf("cache = (%v,%v), want derived (0.1,1.25)", rate.CachedInputPerM, rate.CacheWritePerM)
	}
}

func TestComputeChainRateMixedSurplusAndInferhub(t *testing.T) {
	models := []Model{
		{Slug: "cbcn/x", MinAskIn: 0.20, MinAskOut: 0.50},
		{Slug: "surplus:m", MinAskIn: 0.12, MinAskOut: 0.48, CacheRead: 0.02, CacheWrite: 0.15},
	}
	rate, ok := ComputeChainRate([]string{"cbcn/x", "surplus:m"}, models, 0, 0.1, 1.25)
	if !ok {
		t.Fatal("expected match")
	}
	if rate.InputPerM != 0.12 || rate.OutputPerM != 0.48 {
		t.Fatalf("rates = (%v,%v), want cheapest (0.12,0.48)", rate.InputPerM, rate.OutputPerM)
	}
}
