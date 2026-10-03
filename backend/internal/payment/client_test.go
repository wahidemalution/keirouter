package payment

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCreatePayment(t *testing.T) {
	var gotKey, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-Api-Key")
		gotPath = r.URL.Path
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		require.Equal(t, "INV-1", body["order_id"])
		require.EqualValues(t, 50000, body["amount"])
		require.Equal(t, "IDR", body["currency"])
		_ = json.NewEncoder(w).Encode(map[string]any{
			"payment_id": "pay-1", "order_id": "INV-1", "amount": 50000,
			"fee": 750, "net_amount": 49250,
			"payment_link_url": "https://pay.example/pay/pay-1",
			"status":           "pending", "expires_at": "2026-01-01T12:00:00Z",
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "secret-key")
	resp, err := c.CreatePayment(t.Context(), CreateRequest{
		OrderID: "INV-1", AmountIDR: 50000, Currency: "IDR",
		ExpiresInHours: 24, SuccessReturnURL: "https://app/s", CancelReturnURL: "https://app/c",
		PaymentMethodTypeCode: "QRIS",
	})
	require.NoError(t, err)
	require.Equal(t, "secret-key", gotKey)
	require.Equal(t, "/api/v1/payments", gotPath)
	require.Equal(t, "pay-1", resp.PaymentID)
	require.Equal(t, "https://pay.example/pay/pay-1", resp.PaymentLinkURL)
	require.EqualValues(t, 49250, resp.NetAmount)
}

func TestCreatePaymentNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad"}`))
	}))
	defer srv.Close()
	_, err := NewClient(srv.URL, "k").CreatePayment(t.Context(), CreateRequest{OrderID: "x", AmountIDR: 1})
	require.Error(t, err)
}

func TestVerifySignature(t *testing.T) {
	secret := "whsec_" + base64.StdEncoding.EncodeToString([]byte("topsecret"))
	raw := []byte(`{"event_type":"payment.completed"}`)
	id, ts := "msg_1", "1700000000"
	mac := hmac.New(sha256.New, []byte("topsecret"))
	mac.Write([]byte(id + "." + ts + "." + string(raw)))
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	require.True(t, VerifySignature(secret, id, ts, "v1,"+sig, raw))
	require.True(t, VerifySignature(secret, id, ts, "v1,deadbeef v1,"+sig, raw))
	require.False(t, VerifySignature(secret, id, ts, "v1,"+sig, []byte("tampered")))
	require.False(t, VerifySignature(secret, id, ts, "v1,wrong", raw))
	require.False(t, VerifySignature(secret, id, ts, "", raw))
}

func TestVerifyToken(t *testing.T) {
	require.True(t, VerifyToken("whtok_abc", "whtok_abc"))
	require.False(t, VerifyToken("whtok_abc", "whtok_abd"))
	require.False(t, VerifyToken("", ""))
}
