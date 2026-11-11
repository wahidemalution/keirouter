package market

import (
	"math"
	"reflect"
	"strings"
	"testing"
)

// Billing guard: pins ParseSurplusTrustedBook output on a mixed book. List is
// $1/M in, $4/M out for every offer that carries one.
const goldenBook = `{"offers":[
  {"provider_id":"alpha","price_input_per_1m":500000,"price_output_per_1m":2000000,"direct_input_per_1m":1000000,"direct_output_per_1m":4000000,"available":true,"healthy":true,"trusted":true},
  {"provider_id":"alpha","price_input_per_1m":500000,"price_output_per_1m":2000000,"direct_input_per_1m":1000000,"direct_output_per_1m":4000000,"available":true,"healthy":true,"trusted":true},
  {"provider_id":"beta","price_input_per_1m":600000,"price_output_per_1m":2400000,"direct_input_per_1m":1000000,"direct_output_per_1m":4000000,"available":true,"healthy":true,"trusted":true},
  {"provider_id":"theta","price_input_per_1m":700000,"price_output_per_1m":2800000,"direct_input_per_1m":1000000,"direct_output_per_1m":4000000,"available":true,"healthy":true,"trusted":true},
  {"provider_id":"iota","price_input_per_1m":800000,"price_output_per_1m":3200000,"direct_input_per_1m":1000000,"direct_output_per_1m":4000000,"available":true,"healthy":true,"trusted":true},
  {"provider_id":"inferhub","price_input_per_1m":100000,"price_output_per_1m":400000,"direct_input_per_1m":1000000,"direct_output_per_1m":4000000,"available":true,"healthy":true,"trusted":true},
  {"provider_id":"gamma","price_input_per_1m":300000,"price_output_per_1m":1200000,"direct_input_per_1m":1000000,"direct_output_per_1m":4000000,"available":true,"healthy":true,"trusted":false},
  {"provider_id":"kappa","price_input_per_1m":200000,"price_output_per_1m":800000,"direct_input_per_1m":1000000,"direct_output_per_1m":4000000,"available":false,"healthy":true,"trusted":true},
  {"provider_id":"delta","price_input_per_1m":400000,"price_output_per_1m":1600000,"direct_input_per_1m":1000000,"direct_output_per_1m":4000000,"available":true,"healthy":false,"trusted":true},
  {"provider_id":"eps","price_input_per_1m":950000,"price_output_per_1m":3800000,"direct_input_per_1m":1000000,"direct_output_per_1m":4000000,"available":true,"healthy":true,"trusted":true},
  {"provider_id":"zeta","price_input_per_1m":700000,"price_output_per_1m":2800000,"available":true,"healthy":true,"trusted":true}
]}`

func TestTrustedQuoteGolden(t *testing.T) {
	q, err := ParseSurplusTrustedBook(strings.NewReader(goldenBook), 3, 10, []string{"inferhub"})
	if err != nil {
		t.Fatal(err)
	}
	// Dropped: inferhub (blocked), gamma (untrusted), kappa (unavailable, else
	// the cheapest kept), delta (unhealthy), eps (5% off, under the 10% floor),
	// zeta (no list price, cannot prove the floor).
	// Kept: alpha x2 (50% off), beta (40%), theta (30%), iota (20%).
	// Distinct levels by price: alpha, beta, theta, iota. depth 3 stops at theta,
	// 30% off both rates: ceiling 30-2 = 28, cap = list x 0.72.
	// Floor cap uses the deepest kept offer (iota): list x 0.90.
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-9 }
	if !q.Found || q.Eligible != 5 {
		t.Fatalf("found=%v eligible=%d, want true/5", q.Found, q.Eligible)
	}
	if !near(q.BestIn, 0.5) || !near(q.BestOut, 2.0) {
		t.Fatalf("best = (%v,%v), want (0.5,2)", q.BestIn, q.BestOut)
	}
	if q.MinDiscountPct != 28 {
		t.Fatalf("min discount = %d, want 28", q.MinDiscountPct)
	}
	if !near(q.CapIn, 0.72) || !near(q.CapOut, 2.88) {
		t.Fatalf("cap = (%v,%v), want (0.72,2.88)", q.CapIn, q.CapOut)
	}
	if !near(q.FloorCapIn, 0.9) || !near(q.FloorCapOut, 3.6) {
		t.Fatalf("floor cap = (%v,%v), want (0.9,3.6)", q.FloorCapIn, q.FloorCapOut)
	}
	if want := []string{"alpha", "beta", "iota", "theta"}; !reflect.DeepEqual(q.Providers, want) {
		t.Fatalf("providers = %v, want %v", q.Providers, want)
	}
	if len(q.ProviderOrder) != 0 {
		t.Fatalf("provider order = %v, want empty", q.ProviderOrder)
	}
}
