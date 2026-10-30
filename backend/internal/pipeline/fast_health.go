package pipeline

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mydisha/keirouter/backend/internal/core"
	"github.com/mydisha/keirouter/backend/internal/dispatch"
	"github.com/mydisha/keirouter/backend/internal/market"
)

// surplusProviderHeader names the Surplus provider that served a request.
const surplusProviderHeader = "X-Si-Provider-Family"

// watchFastAttempt prepares a surplus-fast attempt for FastHealth: the
// returned ctx captures which Surplus provider serves it, and report records
// the outcome against that provider. A failure Surplus names no provider for
// ("unknown", or no response at all) is not held against any of them: such a
// failure is fast and often passing, while setting every provider aside would
// shut the step for a whole cooldown. Other attempts get ctx back and a no-op.
func (p *Pipeline) watchFastAttempt(ctx context.Context, t dispatch.Target) (context.Context, func(ttft time.Duration, ok bool)) {
	model, fast := strings.CutPrefix(t.MarketSlug, market.SurplusFastPrefix)
	if p.fastHealth == nil || !fast || model == "" {
		return ctx, func(time.Duration, bool) {}
	}
	var mu sync.Mutex
	var served string
	ctx = core.WithResponseHeaderObserver(ctx, func(h http.Header) {
		if v := h.Get(surplusProviderHeader); v != "" && !strings.EqualFold(v, "unknown") {
			mu.Lock()
			served = v
			mu.Unlock()
		}
	})
	return ctx, func(ttft time.Duration, ok bool) {
		mu.Lock()
		provider := served
		mu.Unlock()
		if provider == "" {
			return
		}
		p.fastHealth.Observe(model, provider, ttft, ok)
		if !ok || ttft > market.FastSlowTTFT {
			p.log.Info("surplus-fast provider set aside", "model", model, "surplus_provider", provider,
				"ttft", ttft, "failed", !ok)
		} else {
			p.log.Debug("surplus-fast served", "model", model, "surplus_provider", provider, "ttft", ttft)
		}
	}
}
