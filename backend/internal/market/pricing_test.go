package market

import (
	"math"
	"testing"
)

func TestComputeRateAppliesMarkupAndDiscount(t *testing.T) {
	snap := []Model{{Slug: "a/b", MinAskIn: 1, MinAskOut: 5, MaxAskIn: 2, MaxAskOut: 10}}
	r, ok := ComputeRate("a/b", snap, 0, 0.1, 1.25)
	if !ok {
		t.Fatal("expected found")
	}
	if math.Abs(r.InputPerM-2) > 1e-9 || math.Abs(r.OutputPerM-10) > 1e-9 {
		t.Fatalf("unexpected rates %+v", r)
	}
	if math.Abs(r.CachedInputPerM-0.1) > 1e-9 || math.Abs(r.CacheWritePerM-1.25) > 1e-9 {
		t.Fatalf("unexpected cache rates %+v", r)
	}
}

func TestComputeRateMissingSlug(t *testing.T) {
	if _, ok := ComputeRate("x/y", nil, 10, 0.1, 1.25); ok {
		t.Fatal("expected not found")
	}
}

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