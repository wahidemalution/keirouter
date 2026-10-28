package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/crypto"
	"github.com/mydisha/keirouter/backend/internal/inflight"
	"github.com/mydisha/keirouter/backend/internal/store"
	"github.com/mydisha/keirouter/backend/internal/vault"
)

func newFinanceTestServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	s := newPaymentTestServer(t)
	s.settings = s.db.Settings()
	s.chains = s.db.Chains()
	s.inflight = inflight.New()
	mk, err := crypto.GenerateMasterKey()
	require.NoError(t, err)
	sealer, err := crypto.NewSealer(mk)
	require.NoError(t, err)
	s.vault = vault.New(sealer)
	r := chi.NewRouter()
	s.mountFinance(r)
	return s, r
}

func doJSON(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m), rec.Body.String())
	return m
}

func TestFinance_RangeValidation(t *testing.T) {
	_, h := newFinanceTestServer(t)
	rec := doJSON(t, h, http.MethodGet, "/payments/finance?range=x", "")
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	rec = doJSON(t, h, http.MethodGet, "/payments/finance?range=7d", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	m := decodeBody(t, rec)
	require.Equal(t, "7d", m["range"])
	require.NotNil(t, m["overview"])
	require.Nil(t, m["accounts"], "upstream balances were removed")
}

func TestFinance_OverviewProfit(t *testing.T) {
	s, h := newFinanceTestServer(t)
	require.NoError(t, s.db.Usage().RecordBatch(t.Context(), []store.UsageRecord{{
		ID: "u1", TenantID: store.DefaultTenantID, APIKeyID: "k1", Provider: "p1", Model: "m",
		Status: "success", CostNanos: 15e9, UpstreamCostNanos: 10e9,
		PricingStatus: "priced", PricingSource: "official", CreatedAt: time.Now().Add(-time.Minute),
	}}))
	rec := doJSON(t, h, http.MethodGet, "/payments/finance?range=all", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	m := decodeBody(t, rec)
	ov := m["overview"].(map[string]any)
	require.InDelta(t, 5.0, ov["profit_usd"].(float64), 1e-9)
	// Revenue is the markup over upstream cost; billed is what users paid.
	require.InDelta(t, 5.0, ov["revenue_usd"].(float64), 1e-9)
	require.InDelta(t, 15.0, ov["billed_usd"].(float64), 1e-9)
	require.InDelta(t, 50.0, ov["margin_pct"].(float64), 1e-9)

	se := m["series"].(map[string]any)
	require.Equal(t, "month", se["granularity"])
	pts := se["points"].([]any)
	require.NotEmpty(t, pts)
	last := pts[len(pts)-1].(map[string]any)
	require.InDelta(t, 15.0, last["billed_usd"].(float64), 1e-9)
	require.InDelta(t, 10.0, last["cost_usd"].(float64), 1e-9)
	require.InDelta(t, 5.0, last["revenue_usd"].(float64), 1e-9)
}

func TestFinance_UpstreamRoutesRemoved(t *testing.T) {
	_, h := newFinanceTestServer(t)
	for _, path := range []string{"/payments/upstream-accounts", "/payments/alerts", "/payments/providers"} {
		require.Equal(t, http.StatusNotFound, doJSON(t, h, http.MethodGet, path, "").Code, path)
	}
}
