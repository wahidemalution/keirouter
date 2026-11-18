package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/config"
	"github.com/mydisha/keirouter/backend/internal/store"
)

func adjustTokens(s *Server, sub, body string) (int, map[string]any) {
	rec := adminTokenCall(s, http.MethodPost, "/x", body, map[string]string{"sub": sub}, s.adminAdjustTokenKey)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestAdminAdjustTokenKey(t *testing.T) {
	s, paygKey := newTokenTestServer(t)
	require.True(t, payTokenOrder(t, s, paygKey, "adj1", "tp-100m"))
	sub, err := s.db.TokenSubscriptions().GetBySub(t.Context(), tokenTestSub)
	require.NoError(t, err)
	recordTokens(t, s, "adj-u1", sub.KeyID, 0, 1000)

	// Lower the balance: limit becomes used + tokens_left.
	code, out := adjustTokens(s, tokenTestSub, `{"tokens_left":5000,"reason":"refund partial","idempotency_key":"k1"}`)
	require.Equal(t, http.StatusCreated, code, out)
	adj := out["adjustment"].(map[string]any)
	require.EqualValues(t, 100_000_000, adj["limit_before_tokens"])
	require.EqualValues(t, 6000, adj["limit_after_tokens"])
	require.EqualValues(t, 6000-100_000_000, adj["delta_tokens"])
	require.EqualValues(t, 100_000_000-1000, adj["tokens_left_before"])
	require.EqualValues(t, 5000, adj["tokens_left_after"])
	require.EqualValues(t, 6000, tokenBudget(t, s, sub.KeyID).LimitTokens)

	// Same idempotency key replays without a second ledger row.
	code, out = adjustTokens(s, tokenTestSub, `{"tokens_left":5000,"reason":"refund partial","idempotency_key":"k1"}`)
	require.Equal(t, http.StatusOK, code, out)

	// Raise it again.
	code, out = adjustTokens(s, tokenTestSub, `{"tokens_left":20000,"reason":"goodwill","idempotency_key":"k2"}`)
	require.Equal(t, http.StatusCreated, code, out)
	require.EqualValues(t, 21000, tokenBudget(t, s, sub.KeyID).LimitTokens)

	// 0 left on a used key pins the limit at usage.
	code, out = adjustTokens(s, tokenTestSub, `{"tokens_left":0,"reason":"abuse","idempotency_key":"k3"}`)
	require.Equal(t, http.StatusCreated, code, out)
	require.EqualValues(t, 1000, tokenBudget(t, s, sub.KeyID).LimitTokens)

	rec := adminTokenCall(s, http.MethodGet, "/x", "", map[string]string{"sub": tokenTestSub}, s.adminListTokenAdjustments)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var list struct {
		Adjustments []map[string]any `json:"adjustments"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list.Adjustments, 3)
}

func TestAdminAdjustTokenKeyRejects(t *testing.T) {
	s, paygKey := newTokenTestServer(t)
	require.True(t, payTokenOrder(t, s, paygKey, "adj2", "tp-100m"))
	sub, err := s.db.TokenSubscriptions().GetBySub(t.Context(), tokenTestSub)
	require.NoError(t, err)

	for name, body := range map[string]string{
		"missing tokens":    `{"reason":"x"}`,
		"both fields":       `{"tokens_left":5,"delta_tokens":5,"reason":"x"}`,
		"zero delta":        `{"delta_tokens":0,"reason":"x"}`,
		"huge delta":        `{"delta_tokens":1000000000001,"reason":"x"}`,
		"remove too many":   `{"delta_tokens":-100000001,"reason":"x"}`,
		"remove all unused": `{"delta_tokens":-100000000,"reason":"x"}`, // limit 0 would mean unlimited
		"negative":          `{"tokens_left":-1,"reason":"x"}`,
		"too large":         `{"tokens_left":1000000000001,"reason":"x"}`,
		"no reason":         `{"tokens_left":5,"reason":"  "}`,
		"unchanged":         `{"tokens_left":100000000,"reason":"x"}`,
		"zero unused":       `{"tokens_left":0,"reason":"x"}`, // limit 0 would mean unlimited
	} {
		code, out := adjustTokens(s, tokenTestSub, body)
		require.Equal(t, http.StatusBadRequest, code, name, out)
	}
	require.EqualValues(t, 100_000_000, tokenBudget(t, s, sub.KeyID).LimitTokens)

	code, _ := adjustTokens(s, "nobody", `{"tokens_left":5,"reason":"x"}`)
	require.Equal(t, http.StatusNotFound, code)
}

// A purchase after an adjustment builds on the adjusted limit.
func TestAdminAdjustTokenKeyThenPurchase(t *testing.T) {
	s, paygKey := newTokenTestServer(t)
	require.True(t, payTokenOrder(t, s, paygKey, "adj3", "tp-100m"))
	sub, err := s.db.TokenSubscriptions().GetBySub(t.Context(), tokenTestSub)
	require.NoError(t, err)
	code, out := adjustTokens(s, tokenTestSub, `{"tokens_left":10,"reason":"x"}`)
	require.Equal(t, http.StatusCreated, code, out)
	require.True(t, payTokenOrder(t, s, paygKey, "adj4", "tp-100m"))
	require.EqualValues(t, 100_000_010, tokenBudget(t, s, sub.KeyID).LimitTokens)
}

// delta_tokens adds to or removes from the balance without touching usage.
func TestAdminAdjustTokenKeyDelta(t *testing.T) {
	s, paygKey := newTokenTestServer(t)
	require.True(t, payTokenOrder(t, s, paygKey, "adjd1", "tp-100m"))
	sub, err := s.db.TokenSubscriptions().GetBySub(t.Context(), tokenTestSub)
	require.NoError(t, err)
	recordTokens(t, s, "adjd-u1", sub.KeyID, 0, 1000)

	code, out := adjustTokens(s, tokenTestSub, `{"delta_tokens":5000,"reason":"bonus","idempotency_key":"d1"}`)
	require.Equal(t, http.StatusCreated, code, out)
	adj := out["adjustment"].(map[string]any)
	require.EqualValues(t, 5000, adj["delta_tokens"])
	require.EqualValues(t, 100_000_000-1000+5000, adj["tokens_left_after"])
	require.EqualValues(t, 100_005_000, tokenBudget(t, s, sub.KeyID).LimitTokens)

	code, out = adjustTokens(s, tokenTestSub, `{"delta_tokens":-2000,"reason":"refund clawback","idempotency_key":"d2"}`)
	require.Equal(t, http.StatusCreated, code, out)
	require.EqualValues(t, 100_003_000, tokenBudget(t, s, sub.KeyID).LimitTokens)

	// Removing everything left pins the limit at usage.
	code, out = adjustTokens(s, tokenTestSub, `{"delta_tokens":-100002000,"reason":"abuse","idempotency_key":"d3"}`)
	require.Equal(t, http.StatusCreated, code, out)
	require.EqualValues(t, 1000, tokenBudget(t, s, sub.KeyID).LimitTokens)

	// Nothing is left to remove now.
	code, out = adjustTokens(s, tokenTestSub, `{"delta_tokens":-1,"reason":"x"}`)
	require.Equal(t, http.StatusBadRequest, code, out)
	require.EqualValues(t, 1000, tokenBudget(t, s, sub.KeyID).LimitTokens)
}

// The token package admin routes sit behind the dashboard session: a caller
// without one, such as a portal user, cannot change or read a balance.
func TestTokenAdminRoutesNeedDashboardSession(t *testing.T) {
	s := &Server{cfg: config.Default()}
	s.cfg.Security.BindLoopbackOnly = false
	router := s.routes()
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/token-packages/subscriptions/x/tokens", `{"delta_tokens":1000,"reason":"x"}`},
		{http.MethodGet, "/api/token-packages/subscriptions/x/token-adjustments", ""},
		{http.MethodGet, "/api/token-packages/subscriptions/x/orders", ""},
		{http.MethodGet, "/api/token-packages/overview", ""},
	} {
		req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
		req.RemoteAddr = "127.0.0.1:1234"
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusUnauthorized, rec.Code, "%s %s must require a dashboard session", c.method, c.path)
	}
}

func TestAdminTokenSubscriberOrders(t *testing.T) {
	s, paygKey := newTokenTestServer(t)
	require.True(t, payTokenOrder(t, s, paygKey, "ho1", "tp-100m"))
	makeTokenOrder(t, s, paygKey, "ho2", "tp-100m") // still pending

	rec := adminTokenCall(s, http.MethodGet, "/x", "", map[string]string{"sub": tokenTestSub}, s.adminListTokenSubscriberOrders)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct {
		Orders []struct {
			OrderID   string `json:"order_id"`
			Status    string `json:"status"`
			TokenKind string `json:"token_kind"`
			Tokens    int64  `json:"tokens"`
			Days      int    `json:"days"`
		} `json:"orders"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Orders, 2)
	byID := map[string]string{}
	for _, o := range body.Orders {
		byID[o.OrderID] = o.Status
		require.Equal(t, "tokens", o.TokenKind)
		require.EqualValues(t, 100_000_000, o.Tokens)
		require.Equal(t, 15, o.Days)
	}
	require.Equal(t, "completed", byID["ho1"])
	require.Equal(t, "pending", byID["ho2"])

	rec = adminTokenCall(s, http.MethodGet, "/x", "", map[string]string{"sub": "nobody"}, s.adminListTokenSubscriberOrders)
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestAdminTokenOverviewUpstreamCost(t *testing.T) {
	s, paygKey := newTokenTestServer(t)
	require.True(t, payTokenOrder(t, s, paygKey, "uc1", "tp-100m"))
	sub, err := s.db.TokenSubscriptions().GetBySub(t.Context(), tokenTestSub)
	require.NoError(t, err)
	now := time.Now().UTC()
	rec := func(id, key string, upstream int64, status string, at time.Time) {
		require.NoError(t, s.usage.Record(t.Context(), store.UsageRecord{
			ID: id, TenantID: store.DefaultTenantID, APIKeyID: key, Provider: "custom-openai-a6api", Model: "gpt-6-sol",
			Status: "success", PromptTokens: 100, CostNanos: upstream * 2, UpstreamCostNanos: upstream,
			ReconcileStatus: status, CreatedAt: at,
		}))
	}
	rec("uc-a", sub.KeyID, 1_500_000_000, "reconciled", now)
	rec("uc-b", sub.KeyID, 500_000_000, "pending", now)
	rec("uc-old", sub.KeyID, 2_000_000_000, "reconciled", now.AddDate(0, 0, -60))
	rec("uc-free", sub.KeyID, 0, "", now)
	rec("uc-payg", paygKey, 9_000_000_000, "reconciled", now) // a PAYG key is not counted

	r := adminTokenCall(s, http.MethodGet, "/token-packages/overview", "", nil, s.adminTokenOverview)
	require.Equal(t, http.StatusOK, r.Code, r.Body.String())
	var body struct {
		Upstream struct {
			TotalUSD  float64 `json:"total_usd"`
			RangeUSD  float64 `json:"range_usd"`
			Pending   int64   `json:"pending_requests"`
			Unpriced  int64   `json:"unpriced_requests"`
			Providers []struct {
				Provider string  `json:"provider"`
				TotalUSD float64 `json:"total_usd"`
			} `json:"providers"`
		} `json:"upstream_cost"`
	}
	require.NoError(t, json.Unmarshal(r.Body.Bytes(), &body))
	require.InDelta(t, 4.0, body.Upstream.TotalUSD, 1e-9)
	require.InDelta(t, 2.0, body.Upstream.RangeUSD, 1e-9)
	require.EqualValues(t, 1, body.Upstream.Pending)
	require.EqualValues(t, 1, body.Upstream.Unpriced)
	require.Len(t, body.Upstream.Providers, 1)
	require.Equal(t, "custom-openai-a6api", body.Upstream.Providers[0].Provider)
}
