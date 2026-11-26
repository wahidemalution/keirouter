package gateway

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// publicPricingSettingsKey stores which chains GET /v1/public/pricing shows.
const publicPricingSettingsKey = "public_pricing"

// publicPricingSettings is the operator's choice for the public price API.
// The zero value (off, nothing listed) is the default, so no price becomes
// public until an admin picks it.
type publicPricingSettings struct {
	Enabled bool `json:"enabled"`
	// ChainIDs are the chains the public API may show. IDs, not names, so a
	// rename keeps the choice; a deleted chain simply drops out.
	ChainIDs []string `json:"chain_ids"`
}

func (s *Server) loadPublicPricingSettings(ctx context.Context) publicPricingSettings {
	var out publicPricingSettings
	if s.settings == nil {
		return out
	}
	raw, err := s.settings.Get(ctx, publicPricingSettingsKey)
	if err != nil || raw == "" {
		return out
	}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

// publicPricing serves GET /v1/public/pricing[?model=a,b] without an API key:
// the live price of the chains an admin published, whatever upstream serves
// them. Off, it answers 404.
func (s *Server) publicPricing(w http.ResponseWriter, r *http.Request) {
	cfg := s.loadPublicPricingSettings(r.Context())
	if !cfg.Enabled {
		writeError(w, http.StatusNotFound, "public pricing is not enabled")
		return
	}
	wanted, err := pricingModelFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	prices, err := s.allChainPrices(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "pricing unavailable")
		return
	}
	allowed := make(map[string]publicModelPrice, len(cfg.ChainIDs))
	for _, id := range cfg.ChainIDs {
		if name, ok := prices.idToName[id]; ok && !prices.unlisted[id] {
			allowed[name] = prices.byName[name]
		}
	}
	writePublicPriceList(w, allowed, wanted, prices.asOf)
}

// writePublicPriceList answers with the allowed prices, narrowed to wanted
// when set, with writePriceList's rules: names outside allowed are skipped,
// and naming only such names is a 404. The list is public and identical for
// every caller, so a CDN may hold it briefly and any site may read it.
func writePublicPriceList(w http.ResponseWriter, allowed map[string]publicModelPrice, wanted []string, asOf time.Time) {
	selected := make([]publicModelPrice, 0, len(allowed))
	if len(wanted) == 0 {
		for _, p := range allowed {
			selected = append(selected, p)
		}
	} else {
		seen := map[string]bool{}
		for _, name := range wanted {
			k := strings.ToLower(name)
			if seen[k] {
				continue
			}
			seen[k] = true
			if p, ok := allowed[k]; ok {
				selected = append(selected, p)
			}
		}
		if len(selected) == 0 {
			writeError(w, http.StatusNotFound, "no such model")
			return
		}
	}
	sort.Slice(selected, func(i, j int) bool { return strings.ToLower(selected[i].Model) < strings.ToLower(selected[j].Model) })
	var asOfJSON any
	if !asOf.IsZero() {
		asOfJSON = asOf.UTC().Format(time.RFC3339)
	}
	w.Header().Set("Cache-Control", "public, max-age=5")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	writeJSON(w, http.StatusOK, map[string]any{
		"currency": "USD",
		"unit":     "per_1m_tokens",
		"as_of":    asOfJSON,
		"models":   selected,
	})
}

// publicPricingRateLimiter caps GET /v1/public/pricing per client IP (the
// real client behind a local proxy; see clientIP), with its own budget so it
// neither eats nor shares the landing page's allowance.
func (s *Server) publicPricingRateLimiter(next http.Handler) http.Handler {
	limiter := newIPLimiter(pricingRequestsPerMinute, time.Minute)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := s.clientIP(r)
		if !limiter.Allow(ip) {
			_, wait := limiter.Exceeded(ip)
			w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(wait.Seconds())))))
			writeError(w, http.StatusTooManyRequests, "rate limit exceeded: at most 30 pricing requests per minute")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// adminPublicPricingModel is one chain in the admin tab, with its live public
// price for a preview.
type adminPublicPricingModel struct {
	ChainID   string `json:"chain_id"`
	Published bool   `json:"published"`
	publicModelPrice
}

// adminGetPublicPricing serves the admin tab: the setting plus every chain,
// each with its live public price.
func (s *Server) adminGetPublicPricing(w http.ResponseWriter, r *http.Request) {
	cfg := s.loadPublicPricingSettings(r.Context())
	prices, err := s.allChainPrices(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "pricing unavailable")
		return
	}
	published := make(map[string]bool, len(cfg.ChainIDs))
	for _, id := range cfg.ChainIDs {
		published[id] = true
	}
	models := make([]adminPublicPricingModel, 0, len(prices.idToName))
	ids := make([]string, 0, len(cfg.ChainIDs))
	for id, name := range prices.idToName {
		models = append(models, adminPublicPricingModel{ChainID: id, Published: published[id], publicModelPrice: prices.byName[name]})
		if published[id] {
			ids = append(ids, id)
		}
	}
	sort.Slice(models, func(i, j int) bool { return strings.ToLower(models[i].Model) < strings.ToLower(models[j].Model) })
	sort.Strings(ids)
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":   cfg.Enabled,
		"chain_ids": ids,
		"models":    models,
		"path":      "/v1/public/pricing",
	})
}

// adminUpdatePublicPricing saves the setting. Every chain ID must name an
// existing chain; anything else is refused rather than silently dropped.
func (s *Server) adminUpdatePublicPricing(w http.ResponseWriter, r *http.Request) {
	if s.settings == nil {
		writeError(w, http.StatusServiceUnavailable, "settings store not configured")
		return
	}
	var body publicPricingSettings
	if !decodeJSON(w, r, &body) {
		return
	}
	prices, err := s.allChainPrices(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "pricing unavailable")
		return
	}
	seen := make(map[string]bool, len(body.ChainIDs))
	ids := make([]string, 0, len(body.ChainIDs))
	for _, id := range body.ChainIDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		if _, ok := prices.idToName[id]; !ok {
			writeError(w, http.StatusBadRequest, "unknown chain: "+id)
			return
		}
		seen[id] = true
		ids = append(ids, id)
	}
	sort.Strings(ids)
	body.ChainIDs = ids
	raw, err := json.Marshal(body)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.settings.Set(r.Context(), publicPricingSettingsKey, string(raw)); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.adminGetPublicPricing(w, r)
}
