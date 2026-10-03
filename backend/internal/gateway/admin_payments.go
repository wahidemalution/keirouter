package gateway

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/mydisha/keirouter/backend/internal/store"
)

// adminListPaymentOrders returns every payment order for the Payments page.
func (s *Server) adminListPaymentOrders(w http.ResponseWriter, r *http.Request) {
	orders, err := s.db.PaymentOrders().ListAll(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	out := make([]map[string]any, 0, len(orders))
	for _, o := range orders {
		view := s.orderView(o)
		view["google_sub"] = o.GoogleSub
		view["key_id"] = o.KeyID
		view["provider"] = o.Provider
		view["provider_payment_id"] = o.ProviderPaymentID
		view["actor"] = o.Actor
		if key, kerr := s.identity.Get(r.Context(), o.KeyID); kerr == nil {
			view["key_display"] = key.Display
		}
		if u, uerr := s.db.PortalUsers().GetBySub(r.Context(), o.GoogleSub); uerr == nil {
			view["user_email"] = u.Email
		}
		out = append(out, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"orders": out})
}

// adminPaymentSummary aggregates revenue and credit totals.
func (s *Server) adminPaymentSummary(w http.ResponseWriter, r *http.Request) {
	orders, err := s.db.PaymentOrders().ListAll(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	var totalIDR, totalCredit int64
	counts := map[string]int{}
	for _, o := range orders {
		counts[string(o.Status)]++
		if o.Status == store.PaymentCompleted || o.Status == store.PaymentManual {
			totalIDR += o.AmountIDR
			totalCredit += o.CreditMicros
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"total_idr":        totalIDR,
		"total_credit_usd": float64(totalCredit) / 1_000_000,
		"count_by_status":  counts,
	})
}

// adminApprovePaymentOrder manually credits a pending order.
func (s *Server) adminApprovePaymentOrder(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		Reason string `json:"reason"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	reason := strings.TrimSpace(body.Reason)
	if len(reason) > 500 {
		writeError(w, http.StatusBadRequest, "reason must be 500 characters or fewer")
		return
	}
	order, err := s.db.PaymentOrders().Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "order not found")
			return
		}
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	if order.Status != store.PaymentPending {
		writeError(w, http.StatusConflict, "order is already "+string(order.Status))
		return
	}
	updated, budget, applied, err := s.creditPaymentOrder(r.Context(), order, store.PaymentManual, "dashboard", reason)
	if err != nil {
		if errors.Is(err, ErrKeyDisabled) {
			writeError(w, http.StatusConflict, "order key is disabled")
			return
		}
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	if !applied {
		writeError(w, http.StatusConflict, "order was already handled")
		return
	}
	view := s.orderView(updated)
	view["budget_limit_usd"] = float64(budget.LimitMicros) / 1_000_000
	writeJSON(w, http.StatusOK, view)
}
