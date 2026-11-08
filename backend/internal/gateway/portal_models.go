package gateway

import (
	"net/http"
	"strings"
)

// portalModelPrice is one row of the portal Models page: a chain the key may
// call, priced the same way as the public catalog.
type portalModelPrice struct {
	Name       string `json:"name"`
	Provider   string `json:"provider"`
	ProviderID string `json:"provider_id"`
	// InputPerM/OutputPerM are the best price per 1M tokens: the cheapest
	// live offer for a market chain, the set price otherwise.
	InputPerM  float64 `json:"input_per_m"`
	OutputPerM float64 `json:"output_per_m"`
	CachedPerM float64 `json:"cached_per_m"`
	// PriceBasis is live, fixed, or empty when sold out.
	PriceBasis    string   `json:"price_basis"`
	MaxInputPerM  *float64 `json:"max_input_per_m"`
	MaxOutputPerM *float64 `json:"max_output_per_m"`
	OfficialIn    *float64 `json:"official_input_per_m"`
	OfficialOut   *float64 `json:"official_output_per_m"`
	// DiscountUpToPct is the best discount off the official price: the live
	// quote's for a market chain, the fixed price's otherwise. Nil when none.
	DiscountUpToPct *int `json:"discount_up_to_pct"`
	ContextTokens   *int `json:"context_tokens"`
	SoldOut         bool `json:"sold_out"`
}

// handlePortalModels serves GET /portal/api/models: every chain the key may
// call with its best price and the discount it can reach.
func (s *Server) handlePortalModels(w http.ResponseWriter, r *http.Request) {
	u, ok := s.portalUserOrError(w, r)
	if !ok {
		return
	}
	key, ok := s.portalKey(w, r, u)
	if !ok {
		return
	}
	chains, err := s.portalChains(r, key, u.PlanID)
	if err != nil {
		s.log.Error("portal models: list models failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to load models")
		return
	}
	rows, err := s.publicModelRows(r.Context())
	if err != nil {
		s.log.Error("portal models: price rows failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to load prices")
		return
	}
	byName := make(map[string]publicModelRow, len(rows))
	for _, row := range rows {
		byName[strings.ToLower(row.Name)] = row
	}
	out := make([]portalModelPrice, 0, len(chains))
	for _, c := range chains {
		row, ok := byName[strings.ToLower(c.Name)]
		if !ok {
			// No routable steps: nothing to price, but the key may still name it.
			out = append(out, portalModelPrice{Name: c.Name, Provider: "combo", ProviderID: "combo", SoldOut: true})
			continue
		}
		out = append(out, portalPriceFromRow(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": out})
}

func portalPriceFromRow(m publicModelRow) portalModelPrice {
	p := portalModelPrice{
		Name: m.Name, Provider: m.Provider, ProviderID: m.ProviderID,
		InputPerM: m.InputPerM, OutputPerM: m.OutputPerM, CachedPerM: m.CachedPerM,
		PriceBasis: m.PriceBasis, SoldOut: m.SoldOut,
	}
	if m.MaxInputPerM > 0 && m.MaxOutputPerM > 0 {
		p.MaxInputPerM, p.MaxOutputPerM = &m.MaxInputPerM, &m.MaxOutputPerM
	}
	if m.Official != nil {
		p.OfficialIn, p.OfficialOut = &m.Official.InputPerM, &m.Official.OutputPerM
	}
	// A live chain's best case is its quote; a fixed chain's discount is exact.
	upTo := m.UpToDiscountPct
	if m.PriceBasis == "fixed" {
		upTo = m.DiscountPct
	}
	if upTo > 0 {
		p.DiscountUpToPct = &upTo
	}
	if m.Context > 0 {
		p.ContextTokens = &m.Context
	}
	return p
}
