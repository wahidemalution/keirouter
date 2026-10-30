package gateway

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/mydisha/keirouter/backend/internal/store"
)

const (
	// realizedWindow is how far back the public average price looks.
	realizedWindow = 7 * 24 * time.Hour
	// realizedWindowDays is realizedWindow in days, as the API reports it.
	realizedWindowDays = 7
	// realizedMinTokens is the uncached input plus output a chain must have
	// been billed for in the window before its average is published. Below it,
	// a few requests would swing the number too far.
	realizedMinTokens = 1_000_000
	// realizedCacheTTL bounds how often the 7-day aggregate is recomputed.
	realizedCacheTTL = 5 * time.Minute
)

// realizedPrice is what customers actually paid on a chain over the window,
// in USD per 1M tokens, and the discount that works out to against the
// official price for the same tokens.
type realizedPrice struct {
	InputPerM  float64
	OutputPerM float64
	// DiscountPct is 0 when no official price is known or the average is not
	// below it.
	DiscountPct int
	Tokens      int64
}

// realizedFrom turns billed totals into average rates. It reports false when
// the chain has too little traffic, or no input or no output billed, to give
// a stable average. The discount weighs each token by what it cost, so it is
// the same number a customer gets by dividing their bill by the official cost.
func realizedFrom(b store.ChainBilledUsage, official *officialPrice) (realizedPrice, bool) {
	if b.InputTokens <= 0 || b.OutputTokens <= 0 || b.InputNanos <= 0 || b.OutputNanos <= 0 {
		return realizedPrice{}, false
	}
	tokens := b.InputTokens + b.OutputTokens
	if tokens < realizedMinTokens {
		return realizedPrice{}, false
	}
	// nanos / tokens is nano-USD per token; per 1M tokens in USD that is /1000.
	rp := realizedPrice{
		InputPerM:  float64(b.InputNanos) / float64(b.InputTokens) / 1000,
		OutputPerM: float64(b.OutputNanos) / float64(b.OutputTokens) / 1000,
		Tokens:     tokens,
	}
	if official != nil {
		list := (float64(b.InputTokens)*official.InputPerM + float64(b.OutputTokens)*official.OutputPerM) / 1e6
		paid := float64(b.InputNanos+b.OutputNanos) / 1e9
		if list > 0 && paid < list {
			rp.DiscountPct = int(math.Floor((1 - paid/list) * 100))
		}
	}
	return rp, true
}

// realizedCache holds the last 7-day billed aggregate per chain ID.
type realizedCache struct {
	mu      sync.Mutex
	builtAt time.Time
	byChain map[string]store.ChainBilledUsage
}

// chainBilled returns the billed totals per chain over realizedWindow,
// recomputed at most once per realizedCacheTTL.
func (s *Server) chainBilled(ctx context.Context) (map[string]store.ChainBilledUsage, error) {
	c := &s.realized
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byChain != nil && time.Since(c.builtAt) < realizedCacheTTL {
		return c.byChain, nil
	}
	if s.usage == nil {
		return map[string]store.ChainBilledUsage{}, nil
	}
	got, err := s.usage.ChainBilled(ctx, adminTenant, time.Now().UTC().Add(-realizedWindow))
	if err != nil {
		return nil, err
	}
	c.byChain, c.builtAt = got, time.Now()
	return got, nil
}

// chainMarketPriced reports whether the chain's price follows a market, so a
// single listed price is only today's best quote and not what every request
// pays.
func chainMarketPriced(c store.Chain) bool {
	if len(c.MarketSlugs) > 0 {
		return true
	}
	for _, st := range c.Steps {
		if st.MarketSlug != "" {
			return true
		}
	}
	return false
}

// scaleRate moves a cache rate by the same factor the input rate moved, so
// cache prices stay in the market's ratio to input.
func scaleRate(rate, from, to float64) float64 {
	if from <= 0 {
		return rate
	}
	return rate * to / from
}
