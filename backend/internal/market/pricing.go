package market

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
		CachedInputPerM: input * cacheReadMult,
		CacheWritePerM:  input * cacheWriteMult,
		DiscountPercent: discount,
	}, true
}