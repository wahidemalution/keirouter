package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/mydisha/keirouter/backend/internal/connectors"
	"github.com/mydisha/keirouter/backend/internal/core"
	"github.com/mydisha/keirouter/backend/internal/store"
)

// applyCapabilityOverrides forces the operator-configured vision/reasoning/tools
// flags onto a resolved capability payload. Only keys present in the JSON are
// applied; an empty or unparseable blob leaves the heuristic result untouched.
func applyCapabilityOverrides(caps *modelCapabilities, raw string) {
	if strings.TrimSpace(raw) == "" {
		return
	}
	var m map[string]bool
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return
	}
	if v, ok := m["vision"]; ok {
		caps.Vision = v
	}
	if v, ok := m["reasoning"]; ok {
		caps.Reasoning = v
	}
	if v, ok := m["tools"]; ok {
		caps.Tools = v
	}
}

// publicOverview serves GET /v1/public/overview. Aggregate-only all-time totals
// plus the number of published routing chains. No caller input, no identifiers,
// no model dispatch.
func (s *Server) publicOverview(w http.ResponseWriter, r *http.Request) {
	if s.cacheHit(w, "public-overview") {
		return
	}
	ctx := r.Context()
	// The landing headline cards are all-time, not a rolling 24h window.
	summary, err := s.usage.SummarizeAccurate(ctx, adminTenant, time.Time{})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "usage unavailable")
		return
	}
	rows, err := s.publicModelRows(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "usage unavailable")
		return
	}
	writeJSONCached(w, s.insightsCache, "public-overview", map[string]any{
		"total_requests": summary.TotalRequests,
		"total_tokens":   summary.PromptTokens + summary.CompletionTokens,
		"success":        summary.SuccessCount,
		"failed":         summary.FailureCount,
		"model_count":    len(rows),
	})
}

// publicModelRow is one published routing chain, joined with its all-time
// aggregate usage. It carries no caller or account identifier.
type publicModelRow struct {
	Name        string
	ModelID     string
	Provider    string
	ProviderID  string
	CapProvider string
	CapModel    string
	// CapOverrides is the operator JSON forcing public capability badges for
	// this chain (empty = heuristic only).
	CapOverrides string
	InputPerM    float64
	OutputPerM   float64
	CachedPerM   float64
	CacheWrite   float64
	SoldOut      bool
	// Official is the list price the discount is measured against; nil when
	// no reference is known. DiscountPct is 0 when there is nothing to show.
	Official    *officialPrice
	DiscountPct int
	// PriceBasis says what the prices are: average_7d (what customers were
	// billed over the last 7 days), live (today's best market quote; a
	// request may cost up to MaxInputPerM/MaxOutputPerM) or fixed (a chain
	// price that does not follow a market).
	PriceBasis    string
	MaxInputPerM  float64
	MaxOutputPerM float64
	AvgTokens     int64
	// Context is the model's context window in tokens from models.dev (0 = unknown).
	Context  int
	Requests int64
	Tokens   int64
	Users    int
}

// chainSoldOut reports whether a market-bound chain has no live quote for any
// of its slugs (chain-level or per-step), so it cannot be priced or routed.
// Chains with no market slug are never sold out on market grounds. A chain the
// operator disabled is always sold out.
func (s *Server) chainSoldOut(c store.Chain) bool {
	if c.Disabled {
		return true
	}
	slugs := append([]string(nil), c.MarketSlugs...)
	for _, st := range c.Steps {
		if st.MarketSlug != "" {
			slugs = append(slugs, st.MarketSlug)
		}
	}
	return len(slugs) > 0 && !s.anyMarketSlugResolves(slugs)
}

// anyMarketSlugResolves reports whether at least one of the chain's bound
// market slugs currently resolves in the live snapshot. A nil cache (tests
// that do not wire one) resolves nothing.
func (s *Server) anyMarketSlugResolves(slugs []string) bool {
	if s.marketCache == nil {
		return false
	}
	for _, slug := range slugs {
		if _, ok := s.marketCache.Rate(slug); ok {
			return true
		}
	}
	return false
}

// publicModelRows lists one entry per routing chain, with all-time usage
// attributed by chain_id. Chains without steps are skipped (nothing routable to
// advertise). Rows sort by request volume, then name. Purely aggregate; safe to
// publish.
func (s *Server) publicModelRows(ctx context.Context) ([]publicModelRow, error) {
	chains, err := s.chains.ListByTenant(ctx, adminTenant)
	if err != nil {
		return nil, err
	}
	usage, err := s.usage.ChainUsageAccurate(ctx, adminTenant, time.Time{})
	if err != nil {
		return nil, err
	}
	// Operator-chosen display providers carry a human label distinct from the
	// slug id; map id -> label so the catalog shows "Google", not "google".
	cats, err := s.db.ProviderCategories().List(ctx, adminTenant)
	if err != nil {
		return nil, err
	}
	labels := make(map[string]string, len(cats))
	for _, c := range cats {
		labels[c.ID] = c.Label
	}
	billed, err := s.chainBilled(ctx)
	if err != nil {
		return nil, err
	}
	live, err := s.surplusPrices(ctx)
	if err != nil {
		return nil, err
	}
	rows := make([]publicModelRow, 0, len(chains))
	for _, c := range chains {
		if len(c.Steps) == 0 {
			continue
		}
		first := c.Steps[0]
		// Chain-configured rates take precedence; all-zero means unset.
		inputPerM, outputPerM, cachedPerM, cacheWritePerM := c.InputPerM, c.OutputPerM, c.CacheReadPerM, c.CacheWritePerM
		soldOut := false
		if s.chainSoldOut(c) {
			// Every bound market slug is missing: the model is sold out.
			// Do not surface a catalog price the market cannot honour.
			soldOut = true
			inputPerM, outputPerM, cachedPerM, cacheWritePerM = 0, 0, 0, 0
		} else if inputPerM <= 0 && outputPerM <= 0 && cachedPerM <= 0 && cacheWritePerM <= 0 {
			price, _ := connectors.ModelPriceByProviderModel(first.Provider, first.Model)
			inputPerM, outputPerM, cachedPerM, cacheWritePerM = price.InputPerM, price.OutputPerM, price.CachedInputPerM, price.CacheWritePerM
		}
		provider, providerID := "combo", "combo"
		if c.DisplayProvider != "" {
			provider, providerID = c.DisplayProvider, c.DisplayProvider
			if label, ok := labels[c.DisplayProvider]; ok && label != "" {
				provider = label
			}
		}
		var official *officialPrice
		discount := 0
		basis := ""
		var maxIn, maxOut float64
		var avgTokens int64
		if !soldOut {
			if ref, ok := s.chainOfficialPrice(c); ok {
				official = &ref
			}
			if rp, ok := realizedFrom(billed[c.ID], official); ok {
				// Publish what customers were billed, not today's best quote.
				cachedPerM = scaleRate(cachedPerM, inputPerM, rp.InputPerM)
				cacheWritePerM = scaleRate(cacheWritePerM, inputPerM, rp.InputPerM)
				inputPerM, outputPerM = rp.InputPerM, rp.OutputPerM
				discount, basis, avgTokens = rp.DiscountPct, "average_7d", rp.Tokens
			} else if chainMarketPriced(c) {
				// Too little traffic for an average: show the live quote as a
				// range and no discount, since requests can bill above it.
				basis = "live"
				if p, ok := live.byName[strings.ToLower(c.Name)]; ok && p.MaxInputPerM != nil && p.MaxOutputPerM != nil {
					maxIn, maxOut = atLeast(*p.MaxInputPerM, inputPerM), atLeast(*p.MaxOutputPerM, outputPerM)
				}
			} else {
				basis = "fixed"
				if official != nil {
					discount = discountPercent(inputPerM, outputPerM, *official)
				}
			}
		}
		u := usage[c.ID]
		rows = append(rows, publicModelRow{
			Name:         c.Name,
			ModelID:      c.Name,
			Provider:     provider,
			ProviderID:   providerID,
			CapProvider:  first.Provider,
			CapModel:     first.Model,
			CapOverrides: c.CapabilityOverrides,
			InputPerM:    inputPerM,
			OutputPerM:   outputPerM,
			CachedPerM:   cachedPerM,
			CacheWrite:   cacheWritePerM,
			SoldOut:      soldOut,
			Official:     official,
			DiscountPct:  discount,
			PriceBasis:   basis,
			MaxInputPerM: maxIn, MaxOutputPerM: maxOut,
			AvgTokens: avgTokens,
			Context:   s.chainContextTokens(c),
			Requests:  u.TotalRequests,
			Tokens:    u.PromptTokens + u.CompletionTokens,
			Users:     u.DistinctKeys,
		})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Requests != rows[j].Requests {
			return rows[i].Requests > rows[j].Requests
		}
		return rows[i].Name < rows[j].Name
	})
	return rows, nil
}

// publicModels serves GET /v1/public/models: one entry per routing chain, with
// per-1M prices, capabilities, and all-time usage aggregated by chain_id.
// Prices are the 7-day billed average when the chain has enough traffic,
// otherwise the chain's configured rates (or the first step's catalog price);
// price_basis says which. Aggregate only.
func (s *Server) publicModels(w http.ResponseWriter, r *http.Request) {
	if s.cacheHit(w, "public-models") {
		return
	}
	rows, err := s.publicModelRows(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "usage unavailable")
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, m := range rows {
		caps, _ := capabilityPayload(m.CapProvider, m.CapModel, core.ServiceLLM)
		applyCapabilityOverrides(&caps, m.CapOverrides)
		var officialIn, officialOut, discount any
		if m.Official != nil {
			officialIn, officialOut = m.Official.InputPerM, m.Official.OutputPerM
		}
		if m.DiscountPct > 0 {
			discount = m.DiscountPct
		}
		var contextTokens any
		if m.Context > 0 {
			contextTokens = m.Context
		}
		var maxIn, maxOut, basis, windowDays, sampleTokens any
		if m.MaxInputPerM > 0 && m.MaxOutputPerM > 0 {
			maxIn, maxOut = m.MaxInputPerM, m.MaxOutputPerM
		}
		if m.PriceBasis != "" {
			basis = m.PriceBasis
		}
		if m.PriceBasis == "average_7d" {
			windowDays, sampleTokens = realizedWindowDays, m.AvgTokens
		}
		out = append(out, map[string]any{
			"name":              m.Name,
			"model_id":          m.ModelID,
			"provider":          m.Provider,
			"provider_id":       m.ProviderID,
			"input_per_m":       m.InputPerM,
			"output_per_m":      m.OutputPerM,
			"cached_per_m":      m.CachedPerM,
			"cache_write_per_m": m.CacheWrite,
			"sold_out":          m.SoldOut,
			// Official list price and the whole-percent discount from it;
			// null when no official reference is known.
			"official_input_per_m":  officialIn,
			"official_output_per_m": officialOut,
			"discount_pct":          discount,
			// price_basis: average_7d | live | fixed (null when sold out).
			// For live, max_*_per_m bound what a request can cost.
			"price_basis":       basis,
			"avg_window_days":   windowDays,
			"avg_sample_tokens": sampleTokens,
			"max_input_per_m":   maxIn,
			"max_output_per_m":  maxOut,
			"context_tokens":    contextTokens,
			"capabilities":      caps,
			"usage": map[string]any{
				"users":    m.Users,
				"requests": m.Requests,
				"tokens":   m.Tokens,
			},
		})
	}
	writeJSONCached(w, s.insightsCache, "public-models", map[string]any{"models": out})
}
