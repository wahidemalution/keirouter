package gateway

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mydisha/keirouter/backend/internal/finance"
	"github.com/mydisha/keirouter/backend/internal/hosting"
)

// mountFinance registers the admin finance dashboard endpoints.
func (s *Server) mountFinance(r chi.Router) {
	r.Get("/payments/finance", s.adminFinanceReport)
}

func (s *Server) settingsGet() func(context.Context, string) (string, error) {
	if s.settings != nil {
		return s.settings.Get
	}
	return s.db.Settings().Get
}

// financeBox seals hosting secrets with the master key. It returns a nil
// interface (not a typed nil) when no vault is configured.
func (s *Server) financeBox() hosting.SecretBox {
	if s.vault == nil {
		return nil
	}
	return s.vault.Sealer()
}

func (s *Server) settingsSet() func(context.Context, string, string) error {
	if s.settings != nil {
		return s.settings.Set
	}
	return s.db.Settings().Set
}

// financeLoader returns the loader wired in New, or builds one from the
// server's store (handler tests construct Server directly).
func (s *Server) financeLoader() *finance.Loader {
	if s.fin != nil {
		return s.fin
	}
	return &finance.Loader{DB: s.db, Settings: s.settingsGet(), InFlight: s.inflight.Snapshot}
}

func overviewView(o finance.Overview) map[string]any {
	return map[string]any{
		"billed_usd":             o.BilledUSD,
		"revenue_usd":            o.RevenueUSD,
		"cost_exact_usd":         o.CostExactUSD,
		"cost_estimated_usd":     o.CostEstimatedUSD,
		"profit_usd":             o.ProfitUSD,
		"margin_pct":             o.MarginPct,
		"requests":               o.Requests,
		"credit_in_idr":          o.CreditInIDR,
		"credit_in_usd":          o.CreditInUSD,
		"paid_orders":            o.PaidOrders,
		"outstanding_credit_usd": o.OutstandingCreditUSD,
		"in_flight":              o.InFlight,
	}
}

func seriesView(se finance.Series) map[string]any {
	points := make([]map[string]any, 0, len(se.Points))
	for _, p := range se.Points {
		points = append(points, map[string]any{
			"start":       p.Start.Format(time.RFC3339),
			"billed_usd":  p.BilledUSD,
			"cost_usd":    p.CostUSD,
			"revenue_usd": p.RevenueUSD,
			"requests":    p.Requests,
		})
	}
	return map[string]any{"granularity": se.Granularity, "points": points}
}

// adminFinanceReport returns the overview and chart series for
// ?range=today|7d|30d|all (default 30d).
func (s *Server) adminFinanceReport(w http.ResponseWriter, r *http.Request) {
	rangeName := r.URL.Query().Get("range")
	if rangeName == "" {
		rangeName = "30d"
	}
	rep, err := s.financeLoader().Report(r.Context(), rangeName)
	if err != nil {
		if errors.Is(err, finance.ErrBadRange) {
			writeError(w, http.StatusBadRequest, "range must be one of today, 7d, 30d, all")
			return
		}
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"range":    rep.Range,
		"overview": overviewView(rep.Overview),
		"series":   seriesView(rep.Series),
	})
}
