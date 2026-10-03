package gateway

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/mydisha/keirouter/backend/internal/payment"
	"github.com/mydisha/keirouter/backend/internal/store"
)

const maxWebhookBytes = 1 << 20

type sumopodWebhook struct {
	EventType string `json:"event_type"`
	Data      struct {
		PaymentID string `json:"payment_id"`
		OrderID   string `json:"order_id"`
		Amount    int64  `json:"amount"`
		Status    string `json:"status"`
	} `json:"data"`
}

// handleSumopodWebhook receives SumoPod payment events. It authenticates with
// the configured Svix signature and/or webhook token; if neither is
// configured it fails closed with 503. A delivery that arrives when both are
// configured must satisfy both. Verified deliveries always get a 2xx so the
// gateway stops retrying, and credit is applied at most once via the guarded
// creditPaymentOrder transition.
func (s *Server) handleSumopodWebhook(w http.ResponseWriter, r *http.Request) {
	p := s.cfg.Payment
	if p.WebhookSecret == "" && p.WebhookToken == "" {
		writeError(w, http.StatusServiceUnavailable, "webhook not configured")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	checks, passed := 0, 0
	if p.WebhookSecret != "" {
		checks++
		if payment.VerifySignature(p.WebhookSecret, r.Header.Get("Svix-Id"), r.Header.Get("Svix-Timestamp"), r.Header.Get("Svix-Signature"), raw) {
			passed++
		}
	}
	if p.WebhookToken != "" {
		checks++
		if payment.VerifyToken(p.WebhookToken, r.Header.Get("X-Webhook-Token")) {
			passed++
		}
	}
	if passed != checks {
		writeError(w, http.StatusUnauthorized, "invalid webhook signature")
		return
	}

	var ev sumopodWebhook
	if err := json.Unmarshal(raw, &ev); err != nil {
		writeError(w, http.StatusBadRequest, "invalid payload")
		return
	}

	order, err := s.db.PaymentOrders().GetByProviderPaymentID(r.Context(), "sumopod", ev.Data.PaymentID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}

	switch ev.EventType {
	case "payment.completed":
		if _, _, _, cerr := s.creditPaymentOrder(r.Context(), order, store.PaymentCompleted, "webhook", ""); cerr != nil {
			s.log.Error("webhook: credit failed", "order", order.ID, "err", cerr)
			writeError(w, http.StatusInternalServerError, "failed to apply credit")
			return
		}
	case "payment.failed", "payment.expired":
		to := store.PaymentFailed
		if ev.EventType == "payment.expired" {
			to = store.PaymentExpired
		}
		tx, terr := s.db.SQL().BeginTx(r.Context(), nil)
		if terr != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		_, _ = s.db.PaymentOrders().TransitionOnTx(r.Context(), tx, order.ID, store.PaymentPending, to, "webhook", "", "")
		_ = tx.Commit()
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}