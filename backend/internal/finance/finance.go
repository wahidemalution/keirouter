// Package finance holds pure revenue and cost math for the
// admin finance dashboard. Nanos are 1e-9 USD, micros are 1e-6 USD.
package finance

import (
	"fmt"
	"math"
	"time"

	"github.com/mydisha/keirouter/backend/internal/store"
)

const (
	nanosPerUSD  = 1e9
	microsPerUSD = 1e6
)

// Markups carries the global markup percent and per-chain overrides.
type Markups struct {
	Global  float64
	ByChain map[string]float64
}

func (m Markups) forChain(id string) float64 {
	if v, ok := m.ByChain[id]; ok {
		return v
	}
	return m.Global
}

// CostNanos splits a row's upstream cost into the exact part and an estimate
// for requests whose upstream cost was not recorded.
func CostNanos(row store.CostBasisRow, m Markups) (exact, estimated int64) {
	exact = row.UpstreamNanos
	if row.UnknownBilledNanos > 0 {
		estimated = int64(math.Round(float64(row.UnknownBilledNanos) / (1 + m.forChain(row.ChainID)/100)))
	}
	return exact, estimated
}

func usd(nanos int64) float64 { return float64(nanos) / nanosPerUSD }

// Overview is the operator's money picture for a range. BilledUSD is what
// users were charged (upstream cost plus markup). RevenueUSD and ProfitUSD
// are both the markup earned on top of the upstream cost (billed - cost), and
// MarginPct is that markup as a percent of cost, so a request that costs $1
// upstream and is sold at a 50% margin shows revenue $0.50, cost $1, profit
// $0.50 and margin 50%.
type Overview struct {
	BilledUSD                                                        float64
	RevenueUSD, CostExactUSD, CostEstimatedUSD, ProfitUSD, MarginPct float64
	Requests                                                         int64
	CreditInIDR                                                      int64
	CreditInUSD                                                      float64
	PaidOrders                                                       int64
	OutstandingCreditUSD                                             float64
	InFlight                                                         int64
}

func BuildOverview(rows []store.CostBasisRow, m Markups, paid store.PaidOrderTotals, outstandingMicros int64, inflight map[string]int64) Overview {
	var billed, exact, est, reqs int64
	for _, r := range rows {
		e, s := CostNanos(r, m)
		billed += r.BilledNanos
		exact += e
		est += s
		reqs += r.Requests
	}
	markup := billed - exact - est
	o := Overview{
		BilledUSD:            usd(billed),
		RevenueUSD:           usd(markup),
		CostExactUSD:         usd(exact),
		CostEstimatedUSD:     usd(est),
		ProfitUSD:            usd(markup),
		Requests:             reqs,
		CreditInIDR:          paid.AmountIDR,
		CreditInUSD:          float64(paid.CreditMicros) / microsPerUSD,
		PaidOrders:           paid.Count,
		OutstandingCreditUSD: float64(outstandingMicros) / microsPerUSD,
	}
	if cost := exact + est; cost > 0 {
		o.MarginPct = float64(markup) / float64(cost) * 100
	}
	for _, n := range inflight {
		o.InFlight += n
	}
	return o
}

// RangeStart returns the inclusive lower bound for a named window.
func RangeStart(name string, now time.Time) (time.Time, error) {
	switch name {
	case "today":
		y, mo, d := now.Date()
		return time.Date(y, mo, d, 0, 0, 0, 0, now.Location()), nil
	case "7d":
		return now.Add(-7 * 24 * time.Hour), nil
	case "30d":
		return now.Add(-30 * 24 * time.Hour), nil
	case "all":
		return time.Time{}, nil
	}
	return time.Time{}, fmt.Errorf("unknown range %q", name)
}
