package market

import "math"

type Rate struct {
	InputPerM       float64
	OutputPerM      float64
	CachedInputPerM float64
	CacheWritePerM  float64
}

// ComputeChainRate derives a chain's rates from its bound slugs, taking the
// cheapest input and the cheapest output independently. It returns false when
// none of the slugs are present so the caller keeps the existing price.
func ComputeChainRate(slugs []string, models []Model, markupPercent, cacheReadMult, cacheWriteMult float64) (Rate, bool) {
	bySlug := make(map[string]Model, len(models))
	for _, m := range models {
		bySlug[m.Slug] = m
	}
	mult := 1 + markupPercent/100
	minIn, minOut := math.Inf(1), math.Inf(1)
	found := false
	for _, slug := range slugs {
		m, ok := bySlug[slug]
		if !ok {
			continue
		}
		found = true
		minIn = math.Min(minIn, m.MinAskIn)
		minOut = math.Min(minOut, m.MinAskOut)
	}
	if !found {
		return Rate{}, false
	}
	effectiveInput := minIn * mult
	effectiveOutput := minOut * mult
	return Rate{
		InputPerM:       effectiveInput,
		OutputPerM:      effectiveOutput,
		CachedInputPerM: effectiveInput * cacheReadMult,
		CacheWritePerM:  effectiveInput * cacheWriteMult,
	}, true
}
