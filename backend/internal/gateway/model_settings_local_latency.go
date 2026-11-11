package gateway

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/mydisha/keirouter/backend/internal/dispatch"
	"github.com/mydisha/keirouter/backend/internal/market"
	"github.com/mydisha/keirouter/backend/internal/store"
)

const (
	// localLatencyWindow is how far back measured performance looks, matching
	// the 24h window of the performance Surplus publishes.
	localLatencyWindow = 24 * time.Hour
	// localLatencyMinRequests keeps a handful of requests from passing for a
	// latency percentile.
	localLatencyMinRequests = 5
	// localSpeedMinSamples is the fewest streamed requests a speed is taken
	// from; only streams report a first-token time.
	localSpeedMinSamples = 3
	// localPerfMaxSamples caps how many recent requests one pair reads.
	localPerfMaxSamples = 5000
	localLatencyTTL     = 5 * time.Minute
)

// measuredPerf is the performance keirouter measured for a provider and
// model over the last 24h. Nil fields have too few requests behind them.
type measuredPerf struct {
	P50, P95     *float64
	TokensPerSec *float64
}

type localLatencyEntry struct {
	perf measuredPerf
	when time.Time
}

// localLatencyCache holds measured performance by "provider\x00model" so a
// page load reads the usage table at most once per pair per localLatencyTTL.
type localLatencyCache struct {
	mu      sync.Mutex
	entries map[string]localLatencyEntry
}

// measuredLatency is the median upstream latency keirouter measured for a
// provider and model over the last 24h.
func (s *Server) measuredLatency(ctx context.Context, provider, model string) *float64 {
	return s.measuredPerformance(ctx, provider, model).P50
}

// measuredPerformance reads the last 24h of a provider and model's successful
// requests: p50 and p95 upstream latency once localLatencyMinRequests
// succeeded, and the median generation speed of streamed requests.
func (s *Server) measuredPerformance(ctx context.Context, provider, model string) measuredPerf {
	if s.usage == nil {
		return measuredPerf{}
	}
	c := &s.localLatency
	k := provider + "\x00" + model
	now := time.Now()
	c.mu.Lock()
	if e, ok := c.entries[k]; ok && now.Sub(e.when) < localLatencyTTL {
		c.mu.Unlock()
		return e.perf
	}
	c.mu.Unlock()

	samples, err := s.usage.PerfSamples(ctx, adminTenant, provider, model, now.Add(-localLatencyWindow), localPerfMaxSamples)
	if err != nil {
		s.log.Debug("model settings: measured performance failed", "provider", provider, "model", model, "err", err)
		return measuredPerf{}
	}
	perf := perfFromSamples(samples)
	c.mu.Lock()
	if c.entries == nil {
		c.entries = map[string]localLatencyEntry{}
	}
	c.entries[k] = localLatencyEntry{perf: perf, when: now}
	c.mu.Unlock()
	return perf
}

// perfFromSamples computes the percentiles at the lower nearest rank, as
// LatencyPercentiles does. Speed is completion tokens over the time after the
// first token, so a slow prefill does not count against it.
func perfFromSamples(samples []store.PerfSample) measuredPerf {
	var out measuredPerf
	if len(samples) >= localLatencyMinRequests {
		lat := make([]float64, len(samples))
		for i, p := range samples {
			lat[i] = float64(p.UpstreamMS)
		}
		out.P50, out.P95 = percentile(lat, 0.50), percentile(lat, 0.95)
	}
	var speeds []float64
	for _, p := range samples {
		gen := p.UpstreamMS - p.TTFTMS
		if p.TTFTMS <= 0 || p.CompletionTokens <= 0 || gen <= 0 {
			continue
		}
		speeds = append(speeds, float64(p.CompletionTokens)*1000/float64(gen))
	}
	if len(speeds) >= localSpeedMinSamples {
		out.TokensPerSec = percentile(speeds, 0.50)
	}
	return out
}

// percentile sorts v in place and returns the value at the lower nearest
// rank for pct.
func percentile(v []float64, pct float64) *float64 {
	slices.Sort(v)
	x := v[int(float64(len(v)-1)*pct)]
	return &x
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
