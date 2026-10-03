package gateway

import (
	"context"
	"math"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mydisha/keirouter/backend/internal/market"
	"github.com/mydisha/keirouter/backend/internal/store"
)

func (s *Server) adminGetMarketPricingSettings(w http.ResponseWriter, r *http.Request) {
	if s.settings == nil {
		writeError(w, http.StatusServiceUnavailable, "settings store not configured")
		return
	}
	writeJSON(w, http.StatusOK, market.LoadSettings(r.Context(), s.settings.Get))
}

func (s *Server) adminUpdateMarketPricingSettings(w http.ResponseWriter, r *http.Request) {
	if s.settings == nil {
		writeError(w, http.StatusInternalServerError, "settings store not configured")
		return
	}
	current := market.LoadSettings(r.Context(), s.settings.Get)
	var patch struct {
		AutoRefresh            *bool    `json:"auto_refresh"`
		RefreshIntervalMinutes *int     `json:"refresh_interval_minutes"`
		MarkupPercent          *float64 `json:"markup_percent"`
	}
	if !decodeJSON(w, r, &patch) {
		return
	}
	if patch.AutoRefresh != nil {
		current.AutoRefresh = *patch.AutoRefresh
	}
	if patch.RefreshIntervalMinutes != nil {
		if *patch.RefreshIntervalMinutes < 1 {
			writeError(w, http.StatusBadRequest, "refresh_interval_minutes must be at least 1")
			return
		}
		current.RefreshIntervalMinutes = *patch.RefreshIntervalMinutes
	}
	if patch.MarkupPercent != nil {
		v := *patch.MarkupPercent
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1000 {
			writeError(w, http.StatusBadRequest, "markup_percent must be between 0 and 1000")
			return
		}
		current.MarkupPercent = v
	}
	if err := market.SaveSettings(r.Context(), s.settings.Set, current); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, current)
}

func (s *Server) adminRefreshMarketPrices(w http.ResponseWriter, r *http.Request) {
	if s.settings == nil {
		writeError(w, http.StatusInternalServerError, "settings store not configured")
		return
	}
	current := market.LoadSettings(r.Context(), s.settings.Get)
	synced := 0
	if s.syncMarketPrices != nil {
		n, err := s.syncMarketPrices(r.Context())
		if err != nil {
			current.LastFetchError = err.Error()
			_ = market.SaveSettings(r.Context(), s.settings.Set, current)
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		synced = n
	}
	current.LastFetchedAt = time.Now().UTC().Format(time.RFC3339)
	current.LastFetchError = ""
	current.LastSyncedCount = synced
	_ = market.SaveSettings(r.Context(), s.settings.Set, current)
	writeJSON(w, http.StatusOK, map[string]any{"synced": synced, "last_fetched_at": current.LastFetchedAt})
}

func (s *Server) countMarketBindings(ctx context.Context) int {
	if s.db == nil {
		return 0
	}
	bindings, err := s.db.MarketBindings().List(ctx, store.DefaultTenantID)
	if err != nil {
		return 0
	}
	if s.marketSnapshot == nil {
		return 0
	}
	snap := s.marketSnapshot()
	n := 0
	for _, b := range bindings {
		if _, ok := market.ComputeRate(b.MarketSlug, snap, 0, 0, 0); ok {
			n++
		}
	}
	return n
}

func (s *Server) adminListMarketBindings(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		writeError(w, http.StatusInternalServerError, "database not configured")
		return
	}
	bindings, err := s.db.MarketBindings().List(r.Context(), store.DefaultTenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"bindings": bindings})
}

func (s *Server) adminSetMarketBinding(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		writeError(w, http.StatusInternalServerError, "database not configured")
		return
	}
	provider := chi.URLParam(r, "provider")
	model := chi.URLParam(r, "model")
	var body struct {
		MarketSlug string `json:"market_slug"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if provider == "" || model == "" || body.MarketSlug == "" {
		writeError(w, http.StatusBadRequest, "provider, model and market_slug are required")
		return
	}
	if err := s.db.MarketBindings().Upsert(r.Context(), store.MarketBinding{
		TenantID: store.DefaultTenantID, ProviderID: provider, ModelID: model, MarketSlug: body.MarketSlug,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, store.MarketBinding{TenantID: store.DefaultTenantID, ProviderID: provider, ModelID: model, MarketSlug: body.MarketSlug})
}

func (s *Server) adminDeleteMarketBinding(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		writeError(w, http.StatusInternalServerError, "database not configured")
		return
	}
	provider := chi.URLParam(r, "provider")
	model := chi.URLParam(r, "model")
	if err := s.db.MarketBindings().Delete(r.Context(), store.DefaultTenantID, provider, model); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}
