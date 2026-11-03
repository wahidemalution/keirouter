package gateway

import (
	"context"
	"math"
	"strings"

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
	// InputPerM and OutputPerM are customer prices per million tokens,
	// markup included; nil when no price is known.
	InputPerM  *float64 `json:"input_per_m"`
	OutputPerM *float64 `json:"output_per_m"`
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
	FinalFallbackKept  bool    `json:"final_fallback_kept,omitempty"`
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

	rows := make([]modelSettingsRow, 0, len(chains))
	for _, c := range chains {
		rows = append(rows, s.modelSettingsRow(key, c, market.EffectiveMarkup(c.MarkupPercent, globalMarkup), names))
	}
	return modelSettingsResponse{DefaultPct: key.MinDiscountPct, MaxPct: maxDiscountFloorPct, Models: rows}, nil
}

func (s *Server) modelSettingsRow(key store.APIKey, c store.Chain, markup float64, names map[string]string) modelSettingsRow {
	row := modelSettingsRow{
		Model:              c.Name,
		Supported:          chainCanGuaranteeDiscount(c),
		Providers:          []modelSettingsProvider{},
		HasFallback:        c.FallbackProvider != "" && c.FallbackModel != "",
		FallbackInputPerM:  c.InputPerM,
		FallbackOutputPerM: c.OutputPerM,
	}
	if row.Supported {
		row.EffectivePct = key.EffectiveMinDiscount(c.Name)
		if pct, ok := key.ModelDiscounts[store.NormalizeDiscountModel(c.Name)]; ok {
			row.OverridePct = &pct
		}
	}
	setting := key.ModelFinalFallback[store.NormalizeDiscountModel(c.Name)]
	row.FinalFallback = key.FinalFallbackOn(c.Name)
	row.FinalFallbackKept = setting == store.FinalFallbackKept

	index := map[string]int{}
	for _, t := range dispatch.TargetsFromChain(c) {
		available, in, out := s.stepCustomerPrice(c, t, markup)
		i, seen := index[t.Provider]
		if !seen {
			index[t.Provider] = len(row.Providers)
			row.Providers = append(row.Providers, modelSettingsProvider{
				ID:      t.Provider,
				Name:    providerDisplayName(names, t.Provider),
				Enabled: key.ProviderEnabled(c.Name, t.Provider),
				Locked:  !key.ProviderAllowed(t.Provider),
			})
			i = len(row.Providers) - 1
		}
		// A provider shows the price of its first step that has an offer, and
		// counts as available when any of its steps does.
		p := &row.Providers[i]
		if available && !p.Available {
			p.Available = true
			p.InputPerM, p.OutputPerM = in, out
		}
	}
	return row
}

// stepCustomerPrice prices one chain step the way routing bills it: the same
// pricing passes as chainResultWith run on the step alone, so a step routing
// would drop (no live quote, slug out of stock) is reported unavailable. The
// FastHealth is only peeked at, never advanced: a page load must not shift
// the recovery-trial cadence live routing relies on. The returned prices are per million tokens with markup included, nil if unknown.
func (s *Server) stepCustomerPrice(c store.Chain, t dispatch.Target, markup float64) (available bool, in, out *float64) {
	steps := []dispatch.Target{t}
	steps = priceMarketSteps(steps, s.marketCache, markup)
	steps = priceStaticSteps(steps, markup)
	steps = stampSurplusTrustedWith(steps, c.MarketSlugs, s.marketCache, markup, peekFastProviders)
	if len(steps) == 0 {
		return false, nil, nil
	}
	p := steps[0]
	if p.MarketRateIn > 0 || p.MarketRateOut > 0 {
		return true, pricePtr(p.MarketRateIn), pricePtr(p.MarketRateOut)
	}
	return true, pricePtr(p.InputPerM), pricePtr(p.OutputPerM)
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
