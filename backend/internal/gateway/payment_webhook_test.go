package gateway

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mydisha/keirouter/backend/internal/store"
	"github.com/stretchr/testify/require"
)

func TestWebhook_RejectsBadAuth(t *testing.T) {
	s := newPaymentTestServer(t)
	s.cfg.Payment.WebhookToken = "whtok_x"
	req := httptest.NewRequest(http.MethodPost, "/webhooks/sumopod", strings.NewReader(`{"event_type":"payment.completed"}`))
	req.Header.Set("X-Webhook-Token", "wrong")
	rec := httptest.NewRecorder()
	s.handleSumopodWebhook(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestWebhook_UnconfiguredFailsClosed(t *testing.T) {
	s := newPaymentTestServer(t)
	rec := httptest.NewRecorder()
	s.handleSumopodWebhook(rec, httptest.NewRequest(http.MethodPost, "/webhooks/sumopod", strings.NewReader(`{}`)))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestWebhook_CompletesOrderOnce(t *testing.T) {
	s := newPaymentTestServer(t)
	s.cfg.Payment.WebhookSecret = "whsec_" + base64.StdEncoding.EncodeToString([]byte("topsecret"))

	key, err := s.identity.Create(t.Context(), store.DefaultTenantID, "", "payer")
	require.NoError(t, err)
	_ = makePaidOrder(t, s, key.Record.ID, "ow1")

	payload := `{"event_type":"payment.completed","data":{"payment_id":"pay-ow1","order_id":"ow1","amount":16000,"status":"completed"}}`
	id, ts := "msg_1", "1700000000"
	mac := hmac.New(sha256.New, []byte("topsecret"))
	mac.Write([]byte(id + "." + ts + "." + payload))
	sig := "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))

	deliver := func() int {
		req := httptest.NewRequest(http.MethodPost, "/webhooks/sumopod", strings.NewReader(payload))
		req.Header.Set("Svix-Id", id)
		req.Header.Set("Svix-Timestamp", ts)
		req.Header.Set("Svix-Signature", sig)
		rec := httptest.NewRecorder()
		s.handleSumopodWebhook(rec, req)
		return rec.Code
	}
	require.Equal(t, http.StatusOK, deliver())

	b, err := s.budgets.ListByScope(t.Context(), store.ScopeAPIKey, key.Record.ID)
	require.NoError(t, err)
	require.Len(t, b, 1)
	require.EqualValues(t, 1_000_000, b[0].LimitMicros)

	// duplicate delivery does not double credit
	require.Equal(t, http.StatusOK, deliver())
	b2, err := s.budgets.ListByScope(t.Context(), store.ScopeAPIKey, key.Record.ID)
	require.NoError(t, err)
	require.EqualValues(t, 1_000_000, b2[0].LimitMicros)
}