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
	buckets     int       // number of buckets; 0 for monthly plans
	since       time.Time // query lower bound; zero means start
	until       time.Time // exclusive query upper bound; zero for none
	lastMonth   time.Time // custom monthly plans: last month shown; zero ends at now
}

// MaxCustomDays caps a custom range so a request can't ask for an unbounded
// scan of daily buckets. Ranges longer than dailyLimitDays switch to months.
const (
	MaxCustomDays  = 3660
	dailyLimitDays = 92
)

// ParseDay reads a YYYY-MM-DD date as midnight in hosting.Zone.
func ParseDay(s string) (time.Time, error) {
	return time.ParseInLocation("2006-01-02", s, hosting.Zone)
}

// planCustom cuts the days from..to (both inclusive, midnight in
// hosting.Zone) into buckets: hours for a single day, days up to
// dailyLimitDays, months beyond that. The last day may not be after today.
func planCustom(from, to, now time.Time) (seriesPlan, error) {
	n := now.In(hosting.Zone)
	_, off := n.Zone()
	offset := int64(off)
	today := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, hosting.Zone)
	if to.Before(from) || to.After(today) {
		return seriesPlan{}, ErrBadRange
	}
	end := to.AddDate(0, 0, 1)
	days := 0
	for d := from; d.Before(end); d = d.AddDate(0, 0, 1) {
		days++
		if days > MaxCustomDays {
			return seriesPlan{}, ErrBadRange
		}
	}
	switch {
	case days == 1:
		hours := 24
		if from.Equal(today) {
			hours = n.Hour() + 1
		}
		return seriesPlan{granularity: "hour", slot: 3600, offset: offset, start: from, buckets: hours, until: end}, nil
	case days <= dailyLimitDays:
		return seriesPlan{granularity: "day", slot: 86400, offset: offset, start: from, buckets: days, until: end}, nil
	}
	return seriesPlan{granularity: "month", slot: 86400, offset: offset, start: monthStart(from), since: from, until: end, lastMonth: monthStart(to)}, nil
}

// querySince is the lower bound for the series query.
func (p seriesPlan) querySince() time.Time {
	if !p.since.IsZero() {
		return p.since
	}
	return p.start
}

func planSeries(rangeName string, now time.Time) (seriesPlan, error) {
	n := now.In(hosting.Zone)
	_, off := n.Zone()
	offset := int64(off)
	midnight := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, hosting.Zone)
	switch rangeName {
	case "today":
		return seriesPlan{granularity: "hour", slot: 3600, offset: offset, start: midnight, buckets: n.Hour() + 1}, nil
	case "7d":
		return seriesPlan{granularity: "day", slot: 86400, offset: offset, start: midnight.AddDate(0, 0, -6), buckets: 7}, nil
	case "30d":
		return seriesPlan{granularity: "day", slot: 86400, offset: offset, start: midnight.AddDate(0, 0, -29), buckets: 30}, nil
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
	if plan.granularity == "month" && !plan.lastMonth.IsZero() {
		for t := plan.start; !t.After(plan.lastMonth); t = t.AddDate(0, 1, 0) {
			starts = append(starts, t)
		}
	} else if plan.granularity == "month" {
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
