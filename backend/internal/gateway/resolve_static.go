package gateway

import (
	"github.com/mydisha/keirouter/backend/internal/dispatch"
	"github.com/mydisha/keirouter/backend/internal/market"
)

// isStaticStep reports whether t was declared with its own fixed price.
func isStaticStep(t dispatch.Target) bool {
	return t.StaticInputPerM > 0 || t.StaticOutputPerM > 0
}

// priceStaticSteps stamps each static-price step with its cost (the provider
// price) and billable (price × markup) rates, in the same fields a market step
// uses, so the meter bills the step that served the request. The step markup
// wins over chainMarkup, which is the chain-effective markup. Cache rates the
// operator left empty derive from the input price with the market ratios.
func priceStaticSteps(steps []dispatch.Target, chainMarkup float64) []dispatch.Target {
	for i := range steps {
		t := &steps[i]
		if !isStaticStep(*t) {
			continue
		}
		markup := chainMarkup
		if t.StepMarkupSet {
			markup = t.StepMarkupPercent
		}
		mult := 1 + markup/100
		cacheRead := t.StaticInputPerM * market.CacheReadMult
		if t.StaticCacheReadSet {
			cacheRead = t.StaticCacheReadPerM
		}
		cacheWrite := t.StaticInputPerM * market.CacheWriteMult
		if t.StaticCacheWriteSet {
			cacheWrite = t.StaticCacheWritePerM
		}
		t.UpstreamRateIn, t.UpstreamRateOut = t.StaticInputPerM, t.StaticOutputPerM
		t.UpstreamCacheReadRate, t.UpstreamCacheWriteRate = cacheRead, cacheWrite
		t.UpstreamReasoningRate = t.StaticOutputPerM
		t.MarketRateIn, t.MarketRateOut = t.StaticInputPerM*mult, t.StaticOutputPerM*mult
		t.MarketCacheReadRate, t.MarketCacheWriteRate = cacheRead*mult, cacheWrite*mult
		t.MarketReasoningRate = t.StaticOutputPerM * mult
		t.StaticPrice = true
	}
	return steps
}

// pinStaticSteps applies a price-based ordering to the non-static steps only.
// Static steps keep their declared index, so a fixed-price backup stays where
// the operator put it; the sorted remainder fills the other slots in order.
func pinStaticSteps(steps []dispatch.Target, sortRest func([]dispatch.Target) []dispatch.Target) []dispatch.Target {
	var rest []dispatch.Target
	for _, t := range steps {
		if !isStaticStep(t) {
			rest = append(rest, t)
		}
	}
	if len(rest) == len(steps) {
		return sortRest(steps)
	}
	sorted := sortRest(rest)
	out := make([]dispatch.Target, 0, len(steps))
	next := 0
	for _, t := range steps {
		if isStaticStep(t) {
			out = append(out, t)
			continue
		}
		out = append(out, sorted[next])
		next++
	}
	return out
}
