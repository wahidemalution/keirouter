package gateway

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mydisha/keirouter/backend/internal/dispatch"
	"github.com/mydisha/keirouter/backend/internal/market"
	"github.com/mydisha/keirouter/backend/internal/store"
)

// publicModelPrice is one model of GET /v1/public/pricing. Every field is
// listed here on purpose: nothing internal (provider IDs, slugs, markup,
// upstream cost) can reach the public through an embedded struct.
type publicModelPrice struct {
	Model     string `json:"model"`
	Available bool   `json:"available"`
	// PriceType is "fixed" when every route bills a static price that is
	// never re-billed, "variable" otherwise.
	PriceType      string   `json:"price_type"`
	InputPerM      *float64 `json:"input_per_m"`
	OutputPerM     *float64 `json:"output_per_m"`
	CachedPerM     *float64 `json:"cached_input_per_m"`
	CacheWritePerM *float64 `json:"cache_write_per_m"`
	// MaxInputPerM/MaxOutputPerM are the highest rates any route, the chain
	// fallback included, can bill; null when a route has no known rate.
	MaxInputPerM  *float64 `json:"max_input_per_m"`
	MaxOutputPerM *float64 `json:"max_output_per_m"`
	// MaxGuaranteed is true only when billing cannot exceed the max: every
	// route bills a rate fixed before the request.
	MaxGuaranteed bool `json:"max_guaranteed"`
	// MinRequestCost is the highest per-request minimum charge of any route,
	// in USD; null when none has one.
	MinRequestCost     *float64         `json:"min_request_cost"`
	OfficialInputPerM  *float64         `json:"official_input_per_m"`
	OfficialOutputPerM *float64         `json:"official_output_per_m"`
	DiscountPct        *int             `json:"discount_pct"`
	AvgInputPerM       *float64         `json:"avg_input_per_m"`
	AvgOutputPerM      *float64         `json:"avg_output_per_m"`
	AvgWindowDays      *int             `json:"avg_window_days"`
	AvgSampleTokens    *int64           `json:"avg_sample_tokens"`
	Providers          []publicProvider `json:"providers"`
}

// publicProvider is one upstream route of a model, in declared step order.
type publicProvider struct {
	Name          string             `json:"name"`
	Available     bool               `json:"available"`
	InputPerM     *float64           `json:"input_per_m"`
	OutputPerM    *float64           `json:"output_per_m"`
	CachedPerM    *float64           `json:"cached_input_per_m"`
	DiscountPct   *int               `json:"discount_pct"`
	LatencyP50MS  *float64           `json:"latency_p50_ms"`
	FinalFallback bool               `json:"final_fallback,omitempty"`
	Prices        []publicPriceLevel `json:"prices"`
}

// publicPriceLevel is one distinct price of a provider, with how many offers
// share it.
type publicPriceLevel struct {
	InputPerM   float64 `json:"input_per_m"`
	OutputPerM  float64 `json:"output_per_m"`
	Offers      int     `json:"offers"`
	DiscountPct *int    `json:"discount_pct"`
	Tag         string  `json:"tag,omitempty"`
}

// routeBound is the most one stamped route can bill and whether billing
// enforces it.
type routeBound struct {
	In, Out float64
	// Known is false when the route has no rate at all.
	Known bool
	// Guaranteed is true when the bill is fixed before the request.
	Guaranteed bool
	// Fixed is true for a static or chain price that never moves.
	Fixed bool
	// MinChargeUSD is the route's per-request minimum, billed multiplier
	// included.
	MinChargeUSD float64
}

// stampRoute prices one chain step the way request resolution does, so the
// public price and the bill come from the same rates. It reports false when
// the step cannot serve right now (no live quote).
func (s *Server) stampRoute(c store.Chain, t dispatch.Target, markup float64) (dispatch.Target, bool) {
	steps := []dispatch.Target{t}
	steps = priceMarketSteps(steps, s.marketCache, markup)
	steps = priceStaticSteps(steps, markup)
	steps = stampSurplusTrustedWith(steps, c.MarketSlugs, s.marketCache, markup, peekFastProviders)
	steps = stampCostLookup(steps)
	if len(steps) == 0 {
		return dispatch.Target{}, false
	}
	return steps[0], true
}

// boundOf is the highest rate a stamped route bills. Routes billed from a
// cost the upstream reports or a reconciled usage log are not clipped to
// their stamped rate, so their bound is not guaranteed.
func boundOf(t dispatch.Target, markup float64) routeBound {
	var b routeBound
	switch {
	case t.MarketRateIn > 0 || t.MarketRateOut > 0:
		b = routeBound{In: t.MarketRateIn, Out: t.MarketRateOut, Known: true, Guaranteed: true}
		b.Fixed = isStaticStep(t)
		if market.IsUpstreamCostSlug(t.MarketSlug) || t.BillUpstreamCost || t.CostLookup {
			b.Guaranteed, b.Fixed = false, false
		}
	case t.InputPerM > 0 || t.OutputPerM > 0:
		b = routeBound{In: t.InputPerM, Out: t.OutputPerM, Known: true, Guaranteed: true, Fixed: true}
	}
	if t.MinChargeMicro > 0 {
		mult := 1.0
		switch {
		case t.BillUpstreamCost && t.UpstreamCostMult > 0:
			mult = t.UpstreamCostMult
		case isStaticStep(t):
			mult = staticBillMult(t, markup)
		}
		b.MinChargeUSD = t.MinChargeMicro / 1e6 * mult
	}
	return b
}

// publicChainPrice describes one chain for the public price list: the
// headline of its first available route, the bound over every route, and
// each provider with its price levels. markup is the chain's effective markup.
func (s *Server) publicChainPrice(c store.Chain, markup float64, latency map[string]map[string]*float64) publicModelPrice {
	p := publicModelPrice{Model: c.Name, PriceType: "variable", Providers: []publicProvider{}}
	official, hasOfficial := s.chainOfficialPrice(c)
	if hasOfficial {
		p.OfficialInputPerM, p.OfficialOutputPerM = pricePtr(official.InputPerM), pricePtr(official.OutputPerM)
	}
	discount := func(in, out *float64) *int {
		if !hasOfficial || in == nil || out == nil {
			return nil
		}
		pct := discountPercent(*in, *out, official)
		return &pct
	}

	var bounds []routeBound
	index := map[string]int{}
	headline := -1
	var headCache, headCacheWrite float64
	if !c.Disabled && !c.Maintenance {
		for _, t := range dispatch.TargetsFromChain(c) {
			if stamped, ok := s.stampRoute(c, t, markup); ok {
				bounds = append(bounds, boundOf(stamped, markup))
			}
			q := s.stepCustomerQuote(c, t, markup)
			i, seen := index[t.Provider]
			if !seen {
				i = len(p.Providers)
				index[t.Provider] = i
				p.Providers = append(p.Providers, publicProvider{
					Name: s.publicProviderName(c, t, i+1), Prices: []publicPriceLevel{},
				})
			}
			pr := &p.Providers[i]
			if pr.LatencyP50MS == nil {
				if provider, name, ok := market.ParseProviderSlug(t.MarketSlug); ok {
					pr.LatencyP50MS = latency[name][strings.ToLower(provider)]
				}
			}
			if !q.available || pr.Available {
				continue
			}
			pr.Available = true
			pr.InputPerM, pr.OutputPerM = q.in, q.out
			pr.CachedPerM = pricePtr(q.cacheRead)
			pr.DiscountPct = discount(q.in, q.out)
			pr.Prices = priceLevels(q, discount)
			if headline < 0 && q.in != nil && q.out != nil {
				headline = i
				headCache, headCacheWrite = q.cacheRead, q.cacheWrite
			}
			p.Available = true
		}
		if fb, ok := s.publicFallback(c, len(p.Providers)+1, discount); ok {
			p.Providers = append(p.Providers, fb.provider)
			bounds = append(bounds, fb.bound)
		}
	}

	if headline >= 0 {
		h := p.Providers[headline]
		p.InputPerM, p.OutputPerM = h.InputPerM, h.OutputPerM
		p.CachedPerM, p.CacheWritePerM = pricePtr(headCache), pricePtr(headCacheWrite)
		p.DiscountPct = h.DiscountPct
	}
	applyBounds(&p, bounds)
	return p
}

// applyBounds sets the max, its guarantee, the minimum charge and the price
// type from every route's bound. One route without a known rate leaves the
// max unknown rather than understated.
func applyBounds(p *publicModelPrice, bounds []routeBound) {
	if len(bounds) == 0 {
		return
	}
	known, guaranteed, fixed := true, true, true
	var maxIn, maxOut, minCharge float64
	for _, b := range bounds {
		known = known && b.Known
		guaranteed = guaranteed && b.Guaranteed
		fixed = fixed && b.Fixed
		maxIn, maxOut = max(maxIn, b.In), max(maxOut, b.Out)
		minCharge = max(minCharge, b.MinChargeUSD)
	}
	if known {
		p.MaxInputPerM, p.MaxOutputPerM = pricePtr(maxIn), pricePtr(maxOut)
	}
	p.MaxGuaranteed = known && guaranteed
	if known && fixed {
		p.PriceType = "fixed"
	}
	p.MinRequestCost = pricePtr(minCharge)
}

// priceLevels groups a route's offers into distinct prices, cheapest first.
// A route without an order book has one level: its own price.
func priceLevels(q stepQuote, discount func(in, out *float64) *int) []publicPriceLevel {
	levels := []publicPriceLevel{}
	add := func(in, out *float64) {
		if in == nil || out == nil {
			return
		}
		if n := len(levels); n > 0 && levels[n-1].InputPerM == *in && levels[n-1].OutputPerM == *out {
			levels[n-1].Offers++
			return
		}
		levels = append(levels, publicPriceLevel{InputPerM: *in, OutputPerM: *out, Offers: 1, DiscountPct: discount(in, out)})
	}
	if len(q.offers) == 0 {
		add(q.in, q.out)
	}
	for _, o := range q.offers {
		add(pricePtr(o.In), pricePtr(o.Out))
	}
	if len(levels) > 1 {
		levels[0].Tag, levels[len(levels)-1].Tag = "cheapest", "highest"
	}
	return levels
}

// publicProviderName is the name the public sees for a route: the operator's
// display name, else the label Surplus itself publishes for a pinned
// provider, else "Provider N". Never a provider ID.
func (s *Server) publicProviderName(c store.Chain, t dispatch.Target, n int) string {
	if name := stepDisplayName(c, t.Provider); name != "" {
		return name
	}
	if id := market.SlugProvider(t.MarketSlug); id != "" {
		if label := s.marketCache.ProviderLabel(id); label != "" {
			return label
		}
	}
	return fmt.Sprintf("Provider %d", n)
}

type publicFallbackRoute struct {
	provider publicProvider
	bound    routeBound
}

// publicFallback prices the chain's last-resort target. It is listed last and
// counts toward the max, since some keys can be billed through it, but it
// never sets the headline.
func (s *Server) publicFallback(c store.Chain, n int, discount func(in, out *float64) *int) (publicFallbackRoute, bool) {
	if c.FallbackProvider == "" || c.FallbackModel == "" {
		return publicFallbackRoute{}, false
	}
	t, ok := s.priceChainFallback(chainFallbackTarget(c, false))
	if !ok {
		return publicFallbackRoute{}, false
	}
	t = stampCostLookup([]dispatch.Target{t})[0]
	in, out := pricePtr(t.MarketRateIn), pricePtr(t.MarketRateOut)
	q := stepQuote{available: true, in: in, out: out}
	return publicFallbackRoute{
		provider: publicProvider{
			Name: fmt.Sprintf("Provider %d", n), Available: true, InputPerM: in, OutputPerM: out,
			CachedPerM: pricePtr(t.MarketCacheReadRate), DiscountPct: discount(in, out),
			FinalFallback: true, Prices: priceLevels(q, discount),
		},
		bound: boundOf(t, 0),
	}, true
}

// publicPriceList is one build of the public price list over every chain.
type publicPriceList struct {
	byName   map[string]publicModelPrice // lower-cased chain name -> price
	idToName map[string]string           // chain ID -> lower-cased chain name
	unlisted map[string]bool             // chain ID -> hidden, maintenance or disabled
	asOf     time.Time
}

// publicPriceCache holds the last list for pricingCacheTTL. One entry only,
// so arbitrary ?model= values cannot grow it.
type publicPriceCache struct {
	mu      sync.Mutex
	builtAt time.Time
	list    publicPriceList
}

// allChainPrices prices every chain of the operator, whatever serves it, with
// its 7-day billed average.
func (s *Server) allChainPrices(ctx context.Context) (publicPriceList, error) {
	c := &s.publicPrices
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.list.byName != nil && time.Since(c.builtAt) < pricingCacheTTL {
		return c.list, nil
	}
	chains, err := s.chains.ListByTenant(ctx, adminTenant)
	if err != nil {
		return publicPriceList{}, err
	}
	settings := market.DefaultSettings()
	if s.settings != nil {
		settings = market.LoadSettings(ctx, s.settings.Get)
	}
	billed, err := s.chainBilled(ctx)
	if err != nil {
		return publicPriceList{}, err
	}
	latency := s.modelSettingsLatency(ctx, chains)
	list := publicPriceList{
		byName:   make(map[string]publicModelPrice, len(chains)),
		idToName: make(map[string]string, len(chains)),
		unlisted: map[string]bool{},
	}
	for _, ch := range chains {
		if len(ch.Steps) == 0 {
			continue
		}
		name := strings.ToLower(ch.Name)
		p := s.publicChainPrice(ch, market.EffectiveMarkup(ch.MarkupPercent, settings.MarkupPercent), latency)
		s.applyPublicRealized(&p, ch, billed[ch.ID])
		list.byName[name] = p
		list.idToName[ch.ID] = name
		if !ch.Listed() || ch.Disabled {
			list.unlisted[ch.ID] = true
		}
	}
	if s.marketCache != nil {
		list.asOf = s.marketCache.FetchedAt()
	}
	c.list, c.builtAt = list, time.Now()
	return list, nil
}

// applyPublicRealized adds the chain's 7-day billed average to an available
// price. It leaves discount_pct alone: that is the live headline's discount.
func (s *Server) applyPublicRealized(p *publicModelPrice, c store.Chain, b store.ChainBilledUsage) {
	if !p.Available {
		return
	}
	var official *officialPrice
	if ref, ok := s.chainOfficialPrice(c); ok {
		official = &ref
	}
	rp, ok := realizedFrom(b, official)
	if !ok {
		return
	}
	days, tokens := realizedWindowDays, rp.Tokens
	p.AvgInputPerM, p.AvgOutputPerM = price(rp.InputPerM), price(rp.OutputPerM)
	p.AvgWindowDays, p.AvgSampleTokens = &days, &tokens
}
