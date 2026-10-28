package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/mydisha/keirouter/backend/internal/currency"
	"github.com/mydisha/keirouter/backend/internal/payment"
	"github.com/mydisha/keirouter/backend/internal/store"
	"github.com/stretchr/testify/require"
)

// reuseTestServer is a payment-enabled portal server whose fake SumoPod
// counts create calls and hands out a new payment id each time.
func reuseTestServer(t *testing.T) (*Server, string, *atomic.Int32) {
	t.Helper()
	s, tok := paymentPortalTestServer(t)
	require.NoError(t, s.currencySvc.Save(context.Background(), currency.Settings{
		OverrideEnabled: true, OverrideRate: 16000,
		RefreshIntervalH: 24, SourceURL: currency.DefaultSourceURL,
	}))
	s.cfg.Payment.MinTopupIDR = 10000
	s.cfg.Payment.MaxTopupIDR = 10000000
	s.cfg.Payment.Provider = "sumopod"
	s.cfg.Payment.WebhookToken = "whtok"

	var calls atomic.Int32
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		writeJSON(w, http.StatusOK, payment.CreateResponse{
			PaymentID: fmt.Sprintf("pay-%d", n), Amount: 50000,
			PaymentLinkURL: fmt.Sprintf("https://pay.example/%d", n), Status: "pending",
			ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		})
	}))
	t.Cleanup(gw.Close)
	s.paymentClient = payment.NewClient(gw.URL, "k")
	return s, tok, &calls
}

func postTopup(t *testing.T, s *Server, tok, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/portal/api/topup/orders", strings.NewReader(body))
	req.AddCookie(&http.Cookie{Name: portalSessionCookie, Value: tok})
	rec := httptest.NewRecorder()
	s.handlePortalCreateOrder(rec, req)
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
	return rec.Code, out
}

func listTopups(t *testing.T, s *Server, tok string) []map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/portal/api/topup/orders", nil)
	req.AddCookie(&http.Cookie{Name: portalSessionCookie, Value: tok})
	rec := httptest.NewRecorder()
	s.handlePortalListOrders(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out struct {
		Orders []map[string]any `json:"orders"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out.Orders
}

func cancelOrder(s *Server, id string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/payments/orders/"+id+"/cancel", strings.NewReader(`{}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	s.adminCancelPaymentOrder(rec, req)
	return rec
}

func TestPortalCreateOrderReusesOpenLink(t *testing.T) {
	s, tok, calls := reuseTestServer(t)

	code, first := postTopup(t, s, tok, `{"amount_idr":50000}`)
	require.Equal(t, http.StatusCreated, code, first)

	// A second click, even for another amount, returns the same open order.
	code, second := postTopup(t, s, tok, `{"amount_idr":100000}`)
	require.Equal(t, http.StatusOK, code, second)
	require.Equal(t, first["order_id"], second["order_id"])
	require.Equal(t, first["payment_link_url"], second["payment_link_url"])
	require.Equal(t, true, second["reused"])
	require.EqualValues(t, 1, calls.Load())

	orders := listTopups(t, s, tok)
	require.Len(t, orders, 1)
	require.Equal(t, true, orders[0]["payable"])
	require.Equal(t, "https://pay.example/1", orders[0]["payment_link_url"])
}

func TestPortalCreateOrderAfterLinkExpiresMakesNewLink(t *testing.T) {
	s, tok, calls := reuseTestServer(t)
	u, err := s.db.PortalUsers().GetBySub(t.Context(), "gsub")
	require.NoError(t, err)
	old := time.Now().Add(-2 * time.Hour)
	require.NoError(t, s.db.PaymentOrders().Create(t.Context(), store.PaymentOrder{
		ID: "old", TenantID: adminTenant, KeyID: u.KeyID, GoogleSub: "gsub",
		AmountIDR: 50000, CreditMicros: 3_125_000, FxRateMicros: 16_000_000,
		Status: store.PaymentPending, Provider: "sumopod", ProviderPaymentID: "pay-old",
		PaymentLinkURL: "https://pay.example/old", Actor: "portal",
		CreatedAt: old, UpdatedAt: old, ExpiresAt: old.Add(time.Hour).UTC().Format(time.RFC3339),
	}))

	code, out := postTopup(t, s, tok, `{"amount_idr":50000}`)
	require.Equal(t, http.StatusCreated, code, out)
	require.NotEqual(t, "old", out["order_id"])
	require.EqualValues(t, 1, calls.Load())

	for _, o := range listTopups(t, s, tok) {
		if o["order_id"] == "old" {
			require.Nil(t, o["payable"])
			require.Nil(t, o["payment_link_url"])
		}
	}
}

func TestAdminCancelPaymentOrderAllowsNewLink(t *testing.T) {
	s, tok, calls := reuseTestServer(t)

	code, first := postTopup(t, s, tok, `{"amount_idr":50000}`)
	require.Equal(t, http.StatusCreated, code, first)
	id := first["order_id"].(string)

	rec := cancelOrder(s, id)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got, err := s.db.PaymentOrders().Get(t.Context(), id)
	require.NoError(t, err)
	require.Equal(t, store.PaymentCancelled, got.Status)
	require.Equal(t, "dashboard", got.Actor)

	// Cancelling twice is a conflict.
	require.Equal(t, http.StatusConflict, cancelOrder(s, id).Code)

	// The user can now start a new payment.
	code, second := postTopup(t, s, tok, `{"amount_idr":50000}`)
	require.Equal(t, http.StatusCreated, code, second)
	require.NotEqual(t, id, second["order_id"])
	require.EqualValues(t, 2, calls.Load())

	// SumoPod cannot cancel the old link; paying it still credits the order.
	sendWebhook(t, s, "payment.completed", got.ProviderPaymentID, "")
	got, err = s.db.PaymentOrders().Get(t.Context(), id)
	require.NoError(t, err)
	require.Equal(t, store.PaymentCompleted, got.Status)
}

func TestAdminCancelPaymentOrderRejectsPaidOrder(t *testing.T) {
	s, tok, _ := reuseTestServer(t)
	code, first := postTopup(t, s, tok, `{"amount_idr":50000}`)
	require.Equal(t, http.StatusCreated, code, first)
	id := first["order_id"].(string)
	require.Equal(t, http.StatusOK, approveOrder(s, id).Code)

	rec := cancelOrder(s, id)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	got, err := s.db.PaymentOrders().Get(t.Context(), id)
	require.NoError(t, err)
	require.Equal(t, store.PaymentManual, got.Status)
}
