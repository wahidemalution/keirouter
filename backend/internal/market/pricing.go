package market

import "math"

type Rate struct {
	InputPerM       float64
	OutputPerM      float64
	CachedInputPerM float64
	CacheWritePerM  float64
	DiscountPercent float64
}

// ComputeRate derives a model's rates from the single bound slug. It returns
// false when the slug is absent so the caller keeps the existing price.
func ComputeRate(slug string, models []Model, markupPercent, cacheReadMult, cacheWriteMult float64) (Rate, bool) {
	var m Model
	found := false
	for _, cand := range models {
		if cand.Slug == slug {
			m, found = cand, true
			break
		}
	}
	if !found {
		return Rate{}, false
	}
	mult := 1 + markupPercent/100
	effectiveInput := m.MinAskIn * mult
	effectiveOutput := m.MinAskOut * mult
	input, output, discount := effectiveInput, effectiveOutput, 0.0
	if m.MaxAskIn > 0 && effectiveInput > 0 && effectiveInput < m.MaxAskIn {
		factor := effectiveInput / m.MaxAskIn
		input = m.MaxAskIn
		output = effectiveOutput / factor
		discount = (1 - factor) * 100
	}
	return Rate{
		InputPerM:       input,
		OutputPerM:      output,
		CachedInputPerM: effectiveInput * cacheReadMult,
		CacheWritePerM:  effectiveInput * cacheWriteMult,
		DiscountPercent: discount,
	}, true
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