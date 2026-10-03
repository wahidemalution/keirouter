package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mydisha/keirouter/backend/internal/currency"
	"github.com/mydisha/keirouter/backend/internal/payment"
	"github.com/mydisha/keirouter/backend/internal/store"
	"github.com/stretchr/testify/require"
)

// paymentPortalTestServer builds on newPortalTestServer with payment enabled,
// a currency service, and a bound portal user.
func paymentPortalTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	srv := newPortalTestServer(t)
	ctx := context.Background()

	srv.cfg.Payment.Enabled = true
	srv.paymentClient = payment.NewClient("http://gw", "k")
	srv.currencySvc = currency.New(srv.db.Settings())

	issued, err := srv.identity.Create(ctx, store.DefaultTenantID, "", "portal-key")
	require.NoError(t, err)
	require.NoError(t, srv.db.PortalUsers().Upsert(ctx, store.PortalUser{
		GoogleSub: "gsub", Email: "u@example.com", KeyID: issued.Record.ID,
	}))

	tok, err := srv.auth.IssuePortalSession("portal:gsub", "u@example.com")
	require.NoError(t, err)
	return srv, tok
}

func TestPortalCreateOrder_RequiresSessionAndKey(t *testing.T) {
	s, tok := paymentPortalTestServer(t)

	// no session -> 401
	rec := httptest.NewRecorder()
	s.handlePortalListOrders(rec, httptest.NewRequest(http.MethodGet, "/portal/api/topup/orders", nil))
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	// with session: list empty
	req := httptest.NewRequest(http.MethodGet, "/portal/api/topup/orders", nil)
	req.AddCookie(&http.Cookie{Name: portalSessionCookie, Value: tok})
	rec = httptest.NewRecorder()
	s.handlePortalListOrders(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func TestPortalCreateOrder_ValidatesAmount(t *testing.T) {
	s, tok := paymentPortalTestServer(t)
	for _, body := range []string{`{"amount_idr":1}`, `{"amount_idr":999999999999}`, `{"amount_idr":0}`} {
		req := httptest.NewRequest(http.MethodPost, "/portal/api/topup/orders", strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: portalSessionCookie, Value: tok})
		rec := httptest.NewRecorder()
		s.handlePortalCreateOrder(rec, req)
		require.Equal(t, http.StatusBadRequest, rec.Code, body)
	}
}

func TestPortalCreateOrderHappyPath(t *testing.T) {
	s, tok := paymentPortalTestServer(t)
	ctx := context.Background()

	// Fixed rate via settings-backed currency override.
	require.NoError(t, s.currencySvc.Save(ctx, currency.Settings{
		OverrideEnabled: true, OverrideRate: 16000,
		RefreshIntervalH: 24, SourceURL: currency.DefaultSourceURL,
	}))
	s.cfg.Payment.MinTopupIDR = 10000
	s.cfg.Payment.MaxTopupIDR = 10000000

	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/payments", r.URL.Path)
		writeJSON(w, http.StatusOK, payment.CreateResponse{
			PaymentID: "pay-1", OrderID: "o-1", Amount: 50000,
			PaymentLinkURL: "https://pay.example/x", Status: "pending",
			ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		})
	}))
	defer gw.Close()
	s.paymentClient = payment.NewClient(gw.URL, "k")

	req := httptest.NewRequest(http.MethodPost, "/portal/api/topup/orders", strings.NewReader(`{"amount_idr":50000}`))
	req.AddCookie(&http.Cookie{Name: portalSessionCookie, Value: tok})
	rec := httptest.NewRecorder()
	s.handlePortalCreateOrder(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "pending", body["status"])
	require.Equal(t, "https://pay.example/x", body["payment_link_url"])
	// 50000 IDR / 16000 (IDR per USD) = 3.125 USD.
	require.Equal(t, 3.125, body["credit_usd"])
	require.Equal(t, 16000.0, body["fx_rate"])

	listRec := httptest.NewRecorder()
	listReq := httptest.NewRequest(http.MethodGet, "/portal/api/topup/orders", nil)
	listReq.AddCookie(&http.Cookie{Name: portalSessionCookie, Value: tok})
	s.handlePortalListOrders(listRec, listReq)
	require.Equal(t, http.StatusOK, listRec.Code, listRec.Body.String())
	require.Contains(t, listRec.Body.String(), body["order_id"].(string))
}

func TestPortalPaymentConfigDisabled(t *testing.T) {
	srv := newPortalTestServer(t) // payment not enabled
	rec := httptest.NewRecorder()
	srv.handlePortalPaymentConfig(rec, httptest.NewRequest(http.MethodGet, "/portal/payment/config", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"enabled":false`)
}
