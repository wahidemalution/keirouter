package market

import (
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
