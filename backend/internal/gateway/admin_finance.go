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
		"token_in_idr":           o.TokenInIDR,
		"token_orders":           o.TokenOrders,
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
// ?range=today|7d|30d|all (default 30d), or ?range=custom&from=YYYY-MM-DD&to=YYYY-MM-DD
// with both days inclusive in the server zone.
func (s *Server) adminFinanceReport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rangeName := q.Get("range")
	if rangeName == "" {
		rangeName = "30d"
	}
	var (
		rep finance.Report
		err error
	)
	if rangeName == "custom" {
		rep, err = s.financeLoader().ReportCustom(r.Context(), q.Get("from"), q.Get("to"))
	} else {
		rep, err = s.financeLoader().Report(r.Context(), rangeName)
	}
	if err != nil {
		if errors.Is(err, finance.ErrBadRange) {
			if rangeName == "custom" {
				writeError(w, http.StatusBadRequest, "custom range needs from and to as YYYY-MM-DD, from on or before to, to not after today, at most 3660 days")
				return
			}
			writeError(w, http.StatusBadRequest, "range must be one of today, 7d, 30d, all, custom")
			return
		}
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	body := map[string]any{
		"range":    rep.Range,
		"overview": overviewView(rep.Overview),
		"series":   seriesView(rep.Series),
	}
	if rep.Range == "custom" {
		body["from"], body["to"] = rep.From, rep.To
	}
	writeJSON(w, http.StatusOK, body)
}
