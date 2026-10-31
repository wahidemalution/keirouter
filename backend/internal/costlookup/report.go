package costlookup

import (
	"context"
	"time"

	"github.com/mydisha/keirouter/backend/internal/store"
)

// warnGapPct is the share of the provider's charge (percent) that may go
// unrecorded by keirouter before the report warns.
const warnGapPct = 1.0

// Report compares one UTC day of what the provider charged with what
// keirouter recorded as its upstream cost.
type Report struct {
	Day                  string                   `json:"day"`
	SubrouterUSD         float64                  `json:"subrouter_usd"`
	KeirouterUpstreamUSD float64                  `json:"keirouter_upstream_usd"`
	GapPct               float64                  `json:"gap_pct"`
	Pending              int                      `json:"pending"`
	Reconciled           int                      `json:"reconciled"`
	Unverified           int                      `json:"unverified"`
	OrphanCount          int                      `json:"orphan_count"`
	OrphanUSD            float64                  `json:"orphan_usd"`
	OverCeiling          []store.OverCeilingGroup `json:"over_ceiling"`
	Warn                 bool                     `json:"warn"`
}

// StatFunc returns the provider's total quota charged over [start, end]
// (unix seconds, inclusive).
type StatFunc func(ctx context.Context, start, end int64) (int64, error)

// BuildReport builds the report for the UTC day containing day. GapPct is
// the provider's charge keirouter did not record, as a percent of the
// charge; Warn is set above warnGapPct.
func BuildReport(ctx context.Context, provider string, day time.Time, stat StatFunc, usage *store.UsageRepo) (Report, error) {
	from := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	quota, err := stat(ctx, from.Unix(), to.Unix()-1)
	if err != nil {
		return Report{}, err
	}
	sum, err := usage.ReconcileDaySummary(ctx, provider, from, to)
	if err != nil {
		return Report{}, err
	}
	r := Report{
		Day:                  from.Format("2006-01-02"),
		SubrouterUSD:         float64(quota*QuotaNanos) / 1e9,
		KeirouterUpstreamUSD: float64(sum.UpstreamCostNanos) / 1e9,
		Pending:              sum.Pending,
		Reconciled:           sum.Reconciled,
		Unverified:           sum.Unverified,
		OrphanCount:          sum.OrphanCount,
		OrphanUSD:            float64(sum.OrphanQuota*QuotaNanos) / 1e9,
		OverCeiling:          sum.OverCeiling,
	}
	if quota > 0 {
		r.GapPct = float64(quota*QuotaNanos-sum.UpstreamCostNanos) / float64(quota*QuotaNanos) * 100
	}
	r.Warn = r.GapPct > warnGapPct
	return r, nil
}
