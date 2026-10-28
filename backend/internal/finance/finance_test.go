package finance

import (
	"math"
	"testing"
	"time"

	"github.com/mydisha/keirouter/backend/internal/store"
)

func near(a, b, eps float64) bool { return math.Abs(a-b) <= eps }

func row() store.CostBasisRow {
	return store.CostBasisRow{Provider: "p", ChainID: "c1", Requests: 1, BilledNanos: 18e9, UpstreamNanos: 10e9, UnknownBilledNanos: 3e9}
}

func TestCostNanos_ExactAndEstimated(t *testing.T) {
	e, est := CostNanos(row(), Markups{Global: 50})
	if e != 10e9 || est != 2e9 {
		t.Fatalf("got %d %d", e, est)
	}
}

func TestCostNanos_ChainOverride(t *testing.T) {
	e, est := CostNanos(row(), Markups{Global: 50, ByChain: map[string]float64{"c1": 0}})
	if e != 10e9 || est != 3e9 {
		t.Fatalf("got %d %d", e, est)
	}
}

func TestBuildOverview_ProfitExample(t *testing.T) {
	rows := []store.CostBasisRow{{Provider: "p", Requests: 2, BilledNanos: 15e9, UpstreamNanos: 10e9}}
	o := BuildOverview(rows, Markups{}, store.PaidOrderTotals{Count: 1, AmountIDR: 16000, CreditMicros: 1e6}, 2e6, map[string]int64{"p": 2, "q": 1})
	// Charged $15 on a $10 upstream cost: revenue and profit are the $5
	// markup, and margin is 5/10 = 50%.
	if !near(o.BilledUSD, 15, 1e-9) || !near(o.RevenueUSD, 5, 1e-9) || !near(o.ProfitUSD, 5, 1e-9) ||
		!near(o.MarginPct, 50, 1e-9) || o.Requests != 2 || o.InFlight != 3 {
		t.Fatalf("%+v", o)
	}
	if o.CreditInIDR != 16000 || o.CreditInUSD != 1 || o.PaidOrders != 1 || o.OutstandingCreditUSD != 2 {
		t.Fatalf("%+v", o)
	}
	if z := BuildOverview(nil, Markups{}, store.PaidOrderTotals{}, 0, nil); z.MarginPct != 0 {
		t.Fatal("margin")
	}
}

func TestRangeStart(t *testing.T) {
	loc := time.FixedZone("x", 7*3600)
	now := time.Date(2026, 10, 15, 13, 30, 0, 0, loc)
	got, err := RangeStart("today", now)
	if err != nil || !got.Equal(time.Date(2026, 10, 15, 0, 0, 0, 0, loc)) {
		t.Fatal(got, err)
	}
	if got, _ := RangeStart("all", now); !got.IsZero() {
		t.Fatal("all")
	}
	if got, _ := RangeStart("7d", now); !got.Equal(now.Add(-7 * 24 * time.Hour)) {
		t.Fatal("7d")
	}
	if _, err := RangeStart("bad", now); err == nil {
		t.Fatal("want error")
	}
}

func TestBuildOverview_MarkupExample(t *testing.T) {
	// The user's example: $1 upstream, 50% margin, user pays $1.50.
	rows := []store.CostBasisRow{{Provider: "p", Requests: 1, BilledNanos: 1.5e9, UpstreamNanos: 1e9}}
	o := BuildOverview(rows, Markups{Global: 50}, store.PaidOrderTotals{}, 0, nil)
	if !near(o.RevenueUSD, 0.5, 1e-9) || !near(o.ProfitUSD, 0.5, 1e-9) || !near(o.CostExactUSD, 1, 1e-9) ||
		!near(o.MarginPct, 50, 1e-9) || !near(o.BilledUSD, 1.5, 1e-9) {
		t.Fatalf("%+v", o)
	}
	// Unknown upstream cost is estimated by removing the markup: same result.
	est := []store.CostBasisRow{{Provider: "p", Requests: 1, BilledNanos: 1.5e9, UnknownBilledNanos: 1.5e9}}
	o = BuildOverview(est, Markups{Global: 50}, store.PaidOrderTotals{}, 0, nil)
	if !near(o.RevenueUSD, 0.5, 1e-9) || !near(o.CostEstimatedUSD, 1, 1e-9) || !near(o.MarginPct, 50, 1e-9) {
		t.Fatalf("%+v", o)
	}
}
