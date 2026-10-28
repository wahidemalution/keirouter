package finance

import (
	"math"
	"testing"
	"time"

	"github.com/mydisha/keirouter/backend/internal/hosting"
	"github.com/mydisha/keirouter/backend/internal/store"
)

// wib pins the finance zone so results do not depend on the test host.
var wib = time.FixedZone("WIB", 7*3600)

func init() { hosting.Zone = wib }

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

func TestBuildSeries_DailyFillsGapsAndEstimates(t *testing.T) {
	now := time.Date(2026, 10, 28, 10, 0, 0, 0, wib)
	plan, err := planSeries("7d", now)
	if err != nil {
		t.Fatal(err)
	}
	day := func(d int) int64 { return (time.Date(2026, 10, d, 0, 0, 0, 0, wib).Unix() + 7*3600) / 86400 }
	pts := []store.CostBasisPoint{
		{Bucket: day(22), CostBasisRow: store.CostBasisRow{ChainID: "c1", Requests: 2, BilledNanos: 15e8, UpstreamNanos: 1e9}},
		{Bucket: day(28), CostBasisRow: store.CostBasisRow{ChainID: "c1", Requests: 1, BilledNanos: 3e9, UnknownBilledNanos: 3e9}},
		{Bucket: day(10), CostBasisRow: store.CostBasisRow{ChainID: "c1", Requests: 9, BilledNanos: 9e9}}, // outside the window
	}
	s := BuildSeries(plan, pts, Markups{Global: 50}, now)
	if s.Granularity != "day" || len(s.Points) != 7 {
		t.Fatalf("got %s with %d points", s.Granularity, len(s.Points))
	}
	if !s.Points[0].Start.Equal(time.Date(2026, 10, 22, 0, 0, 0, 0, wib)) {
		t.Fatalf("first bucket starts %v", s.Points[0].Start)
	}
	p0, p6 := s.Points[0], s.Points[6]
	if !near(p0.BilledUSD, 1.5, 1e-9) || !near(p0.CostUSD, 1, 1e-9) || !near(p0.RevenueUSD, 0.5, 1e-9) || p0.Requests != 2 {
		t.Fatalf("day 22: %+v", p0)
	}
	if s.Points[3].Requests != 0 || s.Points[3].BilledUSD != 0 {
		t.Fatalf("gap day not zero: %+v", s.Points[3])
	}
	// Unknown upstream cost is estimated as billed / (1 + 50%).
	if !near(p6.CostUSD, 2, 1e-9) || !near(p6.RevenueUSD, 1, 1e-9) {
		t.Fatalf("day 28: %+v", p6)
	}
}

func TestBuildSeries_AllTimeFoldsMonths(t *testing.T) {
	now := time.Date(2026, 10, 28, 10, 0, 0, 0, wib)
	plan, _ := planSeries("all", now)
	day := func(m time.Month, d int) int64 {
		return (time.Date(2026, m, d, 0, 0, 0, 0, wib).Unix() + 7*3600) / 86400
	}
	pts := []store.CostBasisPoint{
		{Bucket: day(8, 3), CostBasisRow: store.CostBasisRow{Requests: 1, BilledNanos: 2e9, UpstreamNanos: 1e9}},
		{Bucket: day(8, 30), CostBasisRow: store.CostBasisRow{Requests: 1, BilledNanos: 2e9, UpstreamNanos: 1e9}},
	}
	s := BuildSeries(plan, pts, Markups{}, now)
	if s.Granularity != "month" || len(s.Points) != 3 {
		t.Fatalf("got %s with %d points", s.Granularity, len(s.Points))
	}
	if s.Points[0].Requests != 2 || !near(s.Points[0].RevenueUSD, 2, 1e-9) || s.Points[2].Requests != 0 {
		t.Fatalf("months: %+v", s.Points)
	}
	if empty := BuildSeries(plan, nil, Markups{}, now); len(empty.Points) != 0 {
		t.Fatalf("all time with no usage should have no points, got %d", len(empty.Points))
	}
}

func TestPlanSeries_TodayIsHourly(t *testing.T) {
	plan, err := planSeries("today", time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC)) // 10:00 WIB
	if err != nil || plan.granularity != "hour" || plan.buckets != 11 {
		t.Fatalf("plan %+v err %v", plan, err)
	}
	if _, err := planSeries("x", time.Now()); err == nil {
		t.Fatal("expected an error for an unknown range")
	}
}
