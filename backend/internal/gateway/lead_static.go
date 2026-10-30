package gateway

import (
	"context"

	"github.com/mydisha/keirouter/backend/internal/dispatch"
	"github.com/mydisha/keirouter/backend/internal/market"
	"github.com/mydisha/keirouter/backend/internal/store"
)

// leadStaticTarget returns the chain's first step, priced, when that step has
// its own fixed price and the strategy always tries it first. Such a chain is
// sold at that fixed price: the market steps behind it are only backups, and
// a request that falls through to one is billed at that step's own rate.
// Round-robin and latency ordering can put another step first, so they never
// lead with a static step.
func (s *Server) leadStaticTarget(ctx context.Context, c store.Chain) (dispatch.Target, bool) {
	switch normalizeStrategyToken(c.Strategy) {
	case string(dispatch.StrategyRoundRobin), "round_robin", "roundrobin", chainStrategyLatency:
		return dispatch.Target{}, false
	}
	steps := dispatch.TargetsFromChain(c)
	if len(steps) == 0 || !isStaticStep(steps[0]) {
		return dispatch.Target{}, false
	}
	settings := market.DefaultSettings()
	if s.settings != nil {
		settings = market.LoadSettings(ctx, s.settings.Get)
	}
	markup := market.EffectiveMarkup(c.MarkupPercent, settings.MarkupPercent)
	return priceStaticSteps(steps[:1], markup)[0], true
}
