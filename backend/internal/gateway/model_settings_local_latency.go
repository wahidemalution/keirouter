package gateway

import (
	"context"
	"sync"
	"time"

	"github.com/mydisha/keirouter/backend/internal/dispatch"
	"github.com/mydisha/keirouter/backend/internal/market"
	"github.com/mydisha/keirouter/backend/internal/store"
)

const (
	// localLatencyWindow is how far back the measured median looks, matching
	// the 24h window of the latency Surplus publishes.
	localLatencyWindow = 24 * time.Hour
	// localLatencyMinRequests keeps a handful of requests from passing for a
	// median.
	localLatencyMinRequests = 5
	localLatencyTTL         = 5 * time.Minute
)

type localLatencyEntry struct {
	p50  *float64
	when time.Time
}

// localLatencyCache holds measured medians by "provider\x00model" so a page
// load reads the usage table at most once per pair per localLatencyTTL.
type localLatencyCache struct {
	mu      sync.Mutex
	entries map[string]localLatencyEntry
}

// measuredLatency is the median upstream latency keirouter measured for a
// provider and model over the last 24h, for providers Surplus publishes no
// latency for. Nil when fewer than localLatencyMinRequests requests succeeded.
func (s *Server) measuredLatency(ctx context.Context, provider, model string) *float64 {
	if s.usage == nil {
		return nil
	}
	c := &s.localLatency
	k := provider + "\x00" + model
	now := time.Now()
	c.mu.Lock()
	if e, ok := c.entries[k]; ok && now.Sub(e.when) < localLatencyTTL {
		c.mu.Unlock()
		return e.p50
	}
	c.mu.Unlock()

	var out *float64
	p50, n, err := s.usage.MedianUpstreamLatency(ctx, adminTenant, provider, model, now.Add(-localLatencyWindow))
	if err != nil {
		s.log.Debug("model settings: measured latency failed", "provider", provider, "model", model, "err", err)
		return nil
	}
	if n >= localLatencyMinRequests && p50 > 0 {
		v := float64(p50)
		out = &v
	}
	c.mu.Lock()
	if c.entries == nil {
		c.entries = map[string]localLatencyEntry{}
	}
	c.entries[k] = localLatencyEntry{p50: out, when: now}
	c.mu.Unlock()
	return out
}

// fillMeasuredLatency gives each provider of row that Surplus publishes no
// latency for the median keirouter measured on the chain's step for it. A
// Surplus step without published data stays empty: its slug model is not
// the model keirouter records.
func (s *Server) fillMeasuredLatency(ctx context.Context, c store.Chain, row *modelSettingsRow) {
	for i := range row.Providers {
		p := &row.Providers[i]
		if p.LatencyP50MS != nil {
			continue
		}
		for _, t := range dispatch.TargetsFromChain(c) {
			if t.Provider != p.ID {
				continue
			}
			if _, _, ok := market.ParseProviderSlug(t.MarketSlug); ok {
				break
			}
			if v := s.measuredLatency(ctx, t.Provider, t.Model); v != nil {
				p.LatencyP50MS = v
				break
			}
		}
	}
}
