package gateway

import (
	"net/http"

	"github.com/mydisha/keirouter/backend/internal/dispatch"
	"github.com/mydisha/keirouter/backend/internal/market"
	"github.com/mydisha/keirouter/backend/internal/store"
)

// chainFallbackTarget is the chain's last-resort target. It carries the
// fallback's own price, never the chain price: the chain price follows the
// market steps and is far below what the fallback model costs.
func chainFallbackTarget(c store.Chain, floorExempt bool) dispatch.Target {
	t := dispatch.Target{
		Provider: c.FallbackProvider, Model: c.FallbackModel,
		StaticInputPerM: c.FallbackInputPerM, StaticOutputPerM: c.FallbackOutputPerM,
		ChainFallback: true, FallbackFloorExempt: floorExempt,
		// No markup unless the fallback sets one; the global markup never applies.
		StepMarkupSet: true,
	}
	if c.FallbackMarkupPercent != nil {
		t.StepMarkupPercent = *c.FallbackMarkupPercent
	}
	if c.FallbackCacheReadPerM != nil {
		t.StaticCacheReadPerM, t.StaticCacheReadSet = *c.FallbackCacheReadPerM, true
	}
	if c.FallbackCacheWritePerM != nil {
		t.StaticCacheWritePerM, t.StaticCacheWriteSet = *c.FallbackCacheWritePerM, true
	}
	return t
}

// fallbackModelsDevPrice is the models.dev list price of a fallback model.
func (s *Server) fallbackModelsDevPrice(model string) (market.Reference, bool) {
	if model == "" {
		return market.Reference{}, false
	}
	ref, ok := s.references.Lookup(model)
	return ref, ok && ref.Valid()
}

// priceChainFallback stamps the billable and cost rates on a fallback target.
// Without a stored price it uses the models.dev price of the fallback model;
// with neither it reports false and the fallback must not run. A cache read
// rate left empty bills the full input price, so a provider without a cache
// discount is never billed below cost.
func (s *Server) priceChainFallback(t dispatch.Target) (dispatch.Target, bool) {
	if !isStaticStep(t) {
		ref, ok := s.fallbackModelsDevPrice(t.Model)
		if !ok {
			return t, false
		}
		t.StaticInputPerM, t.StaticOutputPerM = ref.InputPerM, ref.OutputPerM
	}
	if !t.StaticCacheReadSet {
		t.StaticCacheReadPerM, t.StaticCacheReadSet = t.StaticInputPerM, true
	}
	if !t.StaticCacheWriteSet {
		t.StaticCacheWritePerM, t.StaticCacheWriteSet = t.StaticInputPerM*market.CacheWriteMult, true
	}
	t = priceStaticSteps([]dispatch.Target{t}, 0)[0]
	t.InputPerM, t.OutputPerM = t.MarketRateIn, t.MarketRateOut
	t.CacheReadPerM, t.CacheWritePerM = t.MarketCacheReadRate, t.MarketCacheWriteRate
	return t, true
}

// priceResolvedFallback prices the chain fallback at the tail of res, or
// drops it when it has no price. A route left empty is refused.
func (s *Server) priceResolvedFallback(res resolveResult, model string) (resolveResult, error) {
	n := len(res.Targets)
	if n == 0 || !res.Targets[n-1].ChainFallback {
		return res, nil
	}
	priced, ok := s.priceChainFallback(res.Targets[n-1])
	if ok {
		res.Targets[n-1] = priced
		return res, nil
	}
	res.Targets = res.Targets[:n-1]
	res.PlanOpts.PinnedTail = 0
	if len(res.Targets) == 0 {
		return resolveResult{}, errFallbackUnpriced(model)
	}
	return res, nil
}

// errFallbackUnpriced reports a route whose only target left is a fallback
// with no price set and none on models.dev.
func errFallbackUnpriced(model string) error {
	return badModelError{
		msg:              "model " + model + " is temporarily unavailable, try again later",
		status:           http.StatusServiceUnavailable,
		fallbackUnpriced: true,
	}
}
