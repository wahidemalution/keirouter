package finance

import (
	"time"

	"github.com/mydisha/keirouter/backend/internal/hosting"
	"github.com/mydisha/keirouter/backend/internal/store"
)

// SeriesPoint is one bucket of the finance chart. BilledUSD is what users
// paid, CostUSD the provider cost (exact plus estimated) and RevenueUSD the
// markup between them, matching the Overview fields of the same names.
type SeriesPoint struct {
	Start                          time.Time
	BilledUSD, CostUSD, RevenueUSD float64
	Requests                       int64
}

// Series is the chart for a range. Granularity is "hour", "day" or "month".
type Series struct {
	Granularity string
	Points      []SeriesPoint
}

// seriesPlan is how a range is cut into buckets, in hosting.Zone (the
// server's zone) like hosting finance.
type seriesPlan struct {
	granularity string
	slot        int64     // query bucket size in seconds
	offset      int64     // the zone's UTC offset now, aligning buckets to its hours and days
	start       time.Time // first bucket start; zero for all time
	buckets     int       // number of buckets; 0 for all time
}

func planSeries(rangeName string, now time.Time) (seriesPlan, error) {
	n := now.In(hosting.Zone)
	_, off := n.Zone()
	offset := int64(off)
	midnight := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, hosting.Zone)
	switch rangeName {
	case "today":
		return seriesPlan{"hour", 3600, offset, midnight, n.Hour() + 1}, nil
	case "7d":
		return seriesPlan{"day", 86400, offset, midnight.AddDate(0, 0, -6), 7}, nil
	case "30d":
		return seriesPlan{"day", 86400, offset, midnight.AddDate(0, 0, -29), 30}, nil
	case "all":
		return seriesPlan{granularity: "month", slot: 86400, offset: offset}, nil
	}
	return seriesPlan{}, ErrBadRange
}

func bucketStart(bucket int64, plan seriesPlan) time.Time {
	return time.Unix(bucket*plan.slot-plan.offset, 0).In(hosting.Zone)
}

func monthStart(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, hosting.Zone)
}

// BuildSeries turns query buckets into chart points, filling empty buckets
// with zero. All time is folded into calendar months from the first month
// with usage to the current one.
func BuildSeries(plan seriesPlan, pts []store.CostBasisPoint, m Markups, now time.Time) Series {
	type acc struct{ billed, cost, reqs int64 }
	sums := map[int64]*acc{} // keyed by bucket start, unix seconds
	var first time.Time
	for _, p := range pts {
		at := bucketStart(p.Bucket, plan)
		if plan.granularity == "month" {
			at = monthStart(at)
		}
		if first.IsZero() || at.Before(first) {
			first = at
		}
		e, s := CostNanos(p.CostBasisRow, m)
		a := sums[at.Unix()]
		if a == nil {
			a = &acc{}
			sums[at.Unix()] = a
		}
		a.billed += p.BilledNanos
		a.cost += e + s
		a.reqs += p.Requests
	}

	var starts []time.Time
	if plan.granularity == "month" {
		if !first.IsZero() {
			last := monthStart(now.In(hosting.Zone))
			for t := first; !t.After(last); t = t.AddDate(0, 1, 0) {
				starts = append(starts, t)
			}
		}
	} else {
		for i := 0; i < plan.buckets; i++ {
			starts = append(starts, plan.start.Add(time.Duration(int64(i)*plan.slot)*time.Second))
		}
	}

	out := Series{Granularity: plan.granularity, Points: make([]SeriesPoint, 0, len(starts))}
	for _, t := range starts {
		p := SeriesPoint{Start: t}
		if a := sums[t.Unix()]; a != nil {
			p.BilledUSD, p.CostUSD, p.RevenueUSD, p.Requests = usd(a.billed), usd(a.cost), usd(a.billed-a.cost), a.reqs
		}
		out.Points = append(out.Points, p)
	}
	return out
}
