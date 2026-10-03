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