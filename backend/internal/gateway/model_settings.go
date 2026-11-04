package gateway

import (
	"context"
	"math"
	"strings"
	"time"

	"github.com/mydisha/keirouter/backend/internal/connectors"
	"github.com/mydisha/keirouter/backend/internal/dispatch"
	"github.com/mydisha/keirouter/backend/internal/market"
	"github.com/mydisha/keirouter/backend/internal/store"
)

// modelSettingsProvider is one provider a model's chain can route through,
// as a customer sees it on the Model Settings page.
type modelSettingsProvider struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Enabled is whether requests for the model may use the provider: inside
	// the admin ceiling and not turned off by the customer.
	Enabled bool `json:"enabled"`
	// Locked marks a provider outside the admin ceiling. A customer cannot
	// turn it on.
	Locked bool `json:"locked"`
	// Available is false when every step on this provider is a market step
	// with no live quote right now.
	Available bool `json:"available"`
	// InputPerM and OutputPerM are the best customer price per million
	// tokens, markup included; nil when no price is known.
	InputPerM  *float64 `json:"input_per_m"`
	OutputPerM *float64 `json:"output_per_m"`
	// DiscountPct is the best price's discount below the official price;
	// nil when no official price is known.
	DiscountPct *int `json:"discount_pct"`
	// LatencyP50MS is the provider's published 24h median latency for the
	// model; nil when Surplus publishes none or it is not loaded yet.
	LatencyP50MS *float64 `json:"latency_p50_ms"`
	// Offers is every offer of a Surplus provider, cheapest first, priced
	// like InputPerM. Empty for other providers.
	Offers []modelSettingsOffer `json:"offers"`
}

// modelSettingsOffer is one Surplus offer, markup included.
type modelSettingsOffer struct {
	InputPerM   float64 `json:"input_per_m"`
	OutputPerM  float64 `json:"output_per_m"`
	DiscountPct *int    `json:"discount_pct"`
}

// modelSettingsRow is one model of the Model Settings page: the discount
// fields of the Discount Settings table plus provider and fallback settings.
type modelSettingsRow struct {
	Model        string                  `json:"model"`
	Supported    bool                    `json:"supported"`
	OverridePct  *int                    `json:"override_pct"`
	EffectivePct int                     `json:"effective_pct"`
	Providers    []modelSettingsProvider `json:"providers"`
	HasFallback  bool                    `json:"has_fallback"`
	// FinalFallback is true when the stored setting is 1 or 2.
	FinalFallback bool `json:"final_fallback"`
	// FinalFallbackKept is set only for a fallback the migration kept for a
	// key that existed before per-key routing, never one the customer chose.
	FinalFallbackKept bool `json:"final_fallback_kept,omitempty"`
	// FallbackRuns is whether routing actually tries the fallback for this
	// key and model. A kept fallback (2) is on but does not run while a
	// minimum discount applies to the model.
	FallbackRuns       bool    `json:"fallback_runs"`
	FallbackInputPerM  float64 `json:"fallback_input_per_m"`
	FallbackOutputPerM float64 `json:"fallback_output_per_m"`
}

type modelSettingsResponse struct {
	DefaultPct int                `json:"default_pct"`
	MaxPct     int                `json:"max_pct"`
	Models     []modelSettingsRow `json:"models"`
}

// buildModelSettings builds the Model Settings page for key over chains. It
// loads the key's discount and routing settings itself, since the key from
// s.identity.Get carries neither, so callers only pass the key and the chains
// it may call.
func (s *Server) buildModelSettings(ctx context.Context, key store.APIKey, chains []store.Chain) (modelSettingsResponse, error) {
	keys := s.identity.Keys()
	overrides, err := keys.ModelDiscounts(ctx, key.ID)
	if err != nil {
		return modelSettingsResponse{}, err
	}
	routing, err := keys.KeyRouting(ctx, key.ID)
	if err != nil {
		return modelSettingsResponse{}, err
	}
	key.ModelDiscounts = overrides
	key.AllowedProviders = routing.AllowedProviders
	key.ModelDisabledProviders = routing.DisabledProviders
	key.ModelFinalFallback = routing.FinalFallback

	var globalMarkup float64
	if s.settings != nil {
		globalMarkup = market.LoadSettings(ctx, s.settings.Get).MarkupPercent
	}
	names := s.providerNames(ctx)
	latency := s.modelSettingsLatency(ctx, chains)

	rows := make([]modelSettingsRow, 0, len(chains))
	for _, c := range chains {
		row := s.modelSettingsRow(key, c, market.EffectiveMarkup(c.MarkupPercent, globalMarkup), names, latency)
		s.fillMeasuredLatency(ctx, c, &row)
		rows = append(rows, row)
	}
	return modelSettingsResponse{DefaultPct: key.MinDiscountPct, MaxPct: maxDiscountFloorPct, Models: rows}, nil
}

func (s *Server) modelSettingsRow(key store.APIKey, c store.Chain, markup float64, names map[string]string, latency map[string]map[string]*float64) modelSettingsRow {
	row := modelSettingsRow{
		Model:              c.Name,
		Supported:          chainCanGuaranteeDiscount(c),
		Providers:          []modelSettingsProvider{},
		HasFallback:        c.FallbackProvider != "" && c.FallbackModel != "",
		FallbackInputPerM:  c.InputPerM,
		FallbackOutputPerM: c.OutputPerM,
	}
	if row.Supported {
		row.EffectivePct = keyDiscountFloor(key, c.Name, row.Supported)
		if pct, ok := key.ModelDiscounts[store.NormalizeDiscountModel(c.Name)]; ok {
			row.OverridePct = &pct
		}
	}
	setting := key.ModelFinalFallback[store.NormalizeDiscountModel(c.Name)]
	row.FinalFallback = key.FinalFallbackOn(c.Name)
	row.FinalFallbackKept = setting == store.FinalFallbackKept
	row.FallbackRuns = row.HasFallback && finalFallbackRuns(key, c.Name, row.Supported)

	official, hasOfficial := s.chainOfficialPrice(c)
	discount := func(in, out *float64) *int {
		if !hasOfficial || in == nil || out == nil {
			return nil
		}
		pct := discountPercent(*in, *out, official)
		return &pct
	}
	index := map[string]int{}
	for _, t := range dispatch.TargetsFromChain(c) {
		q := s.stepCustomerQuote(c, t, markup)
		i, seen := index[t.Provider]
		if !seen {
			index[t.Provider] = len(row.Providers)
			row.Providers = append(row.Providers, modelSettingsProvider{
				ID:      t.Provider,
				Name:    s.stepProviderName(names, t),
				Enabled: key.ProviderEnabled(c.Name, t.Provider),
				Locked:  !key.ProviderAllowed(t.Provider),
				Offers:  []modelSettingsOffer{},
			})
			i = len(row.Providers) - 1
		}
		// A provider shows the price of its first step that has an offer, and
		// counts as available when any of its steps does.
		p := &row.Providers[i]
		if p.LatencyP50MS == nil {
			if provider, name, ok := market.ParseProviderSlug(t.MarketSlug); ok {
				p.LatencyP50MS = latency[name][strings.ToLower(provider)]
			}
		}
		if !q.available || p.Available {
			continue
		}
		p.Available = true
		p.InputPerM, p.OutputPerM = q.in, q.out
		p.DiscountPct = discount(q.in, q.out)
		for _, o := range q.offers {
			in, out := pricePtr(o.In), pricePtr(o.Out)
			if in == nil || out == nil {
				continue
			}
			p.Offers = append(p.Offers, modelSettingsOffer{InputPerM: *in, OutputPerM: *out, DiscountPct: discount(in, out)})
		}
	}
	return row
}

// stepQuote is one chain step's customer prices per million tokens, markup
// included; a nil price is unknown.
type stepQuote struct {
	available bool
	// in/out is the best price: the cheapest offer for a Surplus market
	// step, the step's own price otherwise.
	in, out *float64
	// offers is every Surplus offer, cheapest first, markup included.
	offers []market.OfferPrice
}

// stepCustomerQuote prices one chain step the way routing bills it: the same
// pricing passes as chainResultWith run on the step alone, so a step routing
// would drop (no live quote, slug out of stock) is reported unavailable. The
// FastHealth is only peeked at, never advanced: a page load must not shift
// the recovery-trial cadence live routing relies on.
func (s *Server) stepCustomerQuote(c store.Chain, t dispatch.Target, markup float64) stepQuote {
	steps := []dispatch.Target{t}
	steps = priceMarketSteps(steps, s.marketCache, markup)
	steps = priceStaticSteps(steps, markup)
	steps = stampSurplusTrustedWith(steps, c.MarketSlugs, s.marketCache, markup, peekFastProviders)
	if len(steps) == 0 {
		return stepQuote{}
	}
	p := steps[0]
	q := stepQuote{available: true}
	if p.MarketRateIn > 0 || p.MarketRateOut > 0 {
		q.in, q.out = pricePtr(p.MarketRateIn), pricePtr(p.MarketRateOut)
	} else {
		q.in, q.out = pricePtr(p.InputPerM), pricePtr(p.OutputPerM)
	}
	// An upstream-cost step bills what the serving offer costs; its best
	// price is the cheapest offer.
	if !market.IsUpstreamCostSlug(p.MarketSlug) || s.marketCache == nil {
		return q
	}
	m, ok := s.marketCache.Lookup(p.MarketSlug)
	if !ok || m.MinAskIn <= 0 || m.MinAskOut <= 0 {
		return q
	}
	mult := 1 + markup/100
	q.in, q.out = pricePtr(m.MinAskIn*mult), pricePtr(m.MinAskOut*mult)
	for _, o := range m.Offers {
		q.offers = append(q.offers, market.OfferPrice{In: o.In * mult, Out: o.Out * mult})
	}
	return q
}

// hasQuote reports whether the snapshot holds a live quote for slug.
func (s *Server) hasQuote(slug string) bool {
	if s.marketCache == nil {
		return false
	}
	m, ok := s.marketCache.Lookup(canonicalSlug(slug))
	return ok && m.MinAskIn > 0 && m.MinAskOut > 0
}

// modelSettingsLatencyWait bounds how long a page load waits for Surplus
// performance data. Slower fetches finish in the background and fill the
// cache, so the next poll shows them.
const modelSettingsLatencyWait = 2 * time.Second

// modelSettingsLatency returns each Surplus model's published median latency
// by lowercased provider id, for the models the chains pin per provider. It
// reads through the model detail cache, so a model is fetched at most once
// per detailPerfTTL, and a model still loading is simply absent.
func (s *Server) modelSettingsLatency(ctx context.Context, chains []store.Chain) map[string]map[string]*float64 {
	seen := map[string]bool{}
	var names []string
	for _, c := range chains {
		for _, slug := range surplusSlugs(c) {
			// Only models with a live quote: without one there is nothing to
			// show the latency next to.
			if _, name, ok := market.ParseProviderSlug(slug); ok && !seen[name] && s.hasQuote(slug) {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	out := make(map[string]map[string]*float64, len(names))
	if len(names) == 0 {
		return out
	}
	type result struct {
		name   string
		routes []market.RoutePerf
	}
	results := make(chan result, len(names))
	// Shared fetches outlive this request; the detail cache times them out.
	fetchCtx := context.WithoutCancel(ctx)
	for _, name := range names {
		go func() {
			perf, ok, err := s.detail.performance(name, func() ([]market.RoutePerf, time.Time, error) {
				return market.FetchSurplusPerformanceRoutes(fetchCtx, s.surplusURL, name)
			})
			if err != nil {
				s.log.Debug("model settings: performance fetch failed", "model", name, "err", err)
			}
			if !ok {
				perf.routes = nil
			}
			results <- result{name: name, routes: perf.routes}
		}()
	}
	timeout := time.NewTimer(modelSettingsLatencyWait)
	defer timeout.Stop()
	for range names {
		select {
		case r := <-results:
			byProvider := map[string]*float64{}
			for _, route := range r.routes {
				if route.P50LatencyMS != nil && *route.P50LatencyMS > 0 {
					byProvider[strings.ToLower(route.Provider)] = route.P50LatencyMS
				}
			}
			out[r.name] = byProvider
		case <-timeout.C:
			return out
		case <-ctx.Done():
			return out
		}
	}
	return out
}

// pricePtr rounds a price for display; non-positive means unknown.
func pricePtr(v float64) *float64 {
	if v <= 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	r := math.Round(v*1e6) / 1e6
	return &r
}

// providerNames maps keirouter provider ids to display names: custom
// providers first, then the built-in catalog.
func (s *Server) providerNames(ctx context.Context) map[string]string {
	names := map[string]string{}
	if list, err := s.db.CustomProviders().ListProviders(ctx, adminTenant); err == nil {
		for _, p := range list {
			if name := strings.TrimSpace(p.DisplayName); name != "" {
				names[p.ID] = name
			}
		}
	}
	return names
}

// stepProviderName is the name a customer sees for a step: for a
// surplus-trusted@<provider> step, the label Surplus publishes for that
// provider (e.g. "OpenRouter"); otherwise the keirouter provider's name.
func (s *Server) stepProviderName(names map[string]string, t dispatch.Target) string {
	if label := s.marketCache.ProviderLabel(market.SlugProvider(t.MarketSlug)); label != "" {
		return label
	}
	return providerDisplayName(names, t.Provider)
}

// providerDisplayName resolves id through names, then the catalog.
func providerDisplayName(names map[string]string, id string) string {
	if n := names[id]; n != "" {
		return n
	}
	if spec, ok := connectors.SpecByAlias(id); ok && spec.DisplayName != "" {
		return spec.DisplayName
	}
	return id
}
