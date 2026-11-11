package gateway

import (
	"context"
	"math"

	"github.com/mydisha/keirouter/backend/internal/market"
	"github.com/mydisha/keirouter/backend/internal/store"
)

// requestLogInfo resolves, for the request log, the provider name a customer
// sees and each chain's official price. It loads chains and provider names
// once per page.
type requestLogInfo struct {
	s        *Server
	chains   map[string]store.Chain
	names    map[string]string
	official map[string]*officialPrice
}

func (s *Server) newRequestLogInfo(ctx context.Context, tenantID string) requestLogInfo {
	info := requestLogInfo{s: s, chains: map[string]store.Chain{}, names: s.providerNames(ctx), official: map[string]*officialPrice{}}
	chains, err := s.chains.ListByTenant(ctx, tenantID)
	if err != nil {
		s.log.Error("request log: chain lookup failed", "err", err)
		return info
	}
	for _, c := range chains {
		info.chains[c.ID] = c
	}
	return info
}

// providerName is the display name the chain's step on the provider sets,
// else the Surplus label of the provider a chain step pinned with
// surplus-trusted@<provider>, else the keirouter provider's name.
func (i requestLogInfo) providerName(rec store.RecentRecord) string {
	if c, ok := i.chains[rec.ChainID]; ok {
		if name := stepDisplayName(c, rec.Provider); name != "" {
			return name
		}
		for _, st := range c.Steps {
			if st.Provider != rec.Provider {
				continue
			}
			if label := i.s.marketCache.ProviderLabel(market.SlugProvider(st.MarketSlug)); label != "" {
				return label
			}
		}
	}
	return providerDisplayName(i.names, rec.Provider)
}

// discountPct is how far the billed cost sits below what the same tokens cost
// at the chain's official price; nil when there is no official price or
// nothing was billed.
func (i requestLogInfo) discountPct(rec store.RecentRecord) *int {
	c, ok := i.chains[rec.ChainID]
	if !ok {
		return nil
	}
	official, cached := i.official[c.ID]
	if !cached {
		if ref, ok := i.s.chainOfficialPrice(c); ok {
			official = &ref
		}
		i.official[c.ID] = official
	}
	if official == nil {
		return nil
	}
	return requestDiscountPct(rec, *official)
}

// requestDiscountPct prices rec's tokens at the official rates, cache reads
// and writes at the market cache multipliers, and compares the billed cost.
// It floors to a whole percent and never goes below zero.
func requestDiscountPct(rec store.RecentRecord, official officialPrice) *int {
	cost := recordCostUSD(rec)
	if cost <= 0 {
		return nil
	}
	cachedRead := max(rec.CachedTokens, 0)
	cacheWrite := max(rec.CacheWriteTokens, 0)
	standard := max(rec.PromptTokens-cachedRead-cacheWrite, 0)
	list := (float64(standard)*official.InputPerM +
		float64(cachedRead)*official.InputPerM*market.CacheReadMult +
		float64(cacheWrite)*official.InputPerM*market.CacheWriteMult +
		float64(rec.CompletionTokens)*official.OutputPerM) / 1e6
	if list <= 0 {
		return nil
	}
	pct := max(int(math.Floor((1-cost/list)*100+1e-9)), 0)
	return &pct
}
