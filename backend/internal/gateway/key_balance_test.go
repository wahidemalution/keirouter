package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/store"
)

func keyUsageBody(t *testing.T, s *Server, key store.APIKey) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/keys/me/usage", nil)
	req = req.WithContext(context.WithValue(req.Context(), apiKeyCtxKey, key))
	rec := httptest.NewRecorder()
	s.handleKeyUsage(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return decodeJSONBody(t, rec.Body.Bytes())
}

func balanceOf(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	b, ok := body["balance"].(map[string]any)
	require.True(t, ok, "balance block missing: %v", body)
	return b
}

func TestKeyBalanceTokenPackage(t *testing.T) {
	s, tok, _ := tokenPortalServer(t)
	key := paidTokenKey(t, s, tok)

	body := keyUsageBody(t, s, key)
	require.Empty(t, costKeyPaths(body, ""))
	b := balanceOf(t, body)
	require.Equal(t, "token_package", b["type"])
	require.Equal(t, true, b["active"])
	require.EqualValues(t, 100_000_000, b["tokens_limit"])
	require.EqualValues(t, 500, b["tokens_used"])
	require.EqualValues(t, 100_000_000-500, b["tokens_remaining"])
	require.EqualValues(t, 15, b["days_remaining"])
	_, err := time.Parse(time.RFC3339, b["expires_at"].(string))
	require.NoError(t, err)
	require.Contains(t, b, "rpm_limit")
	require.Contains(t, b, "concurrency_limit")
}

func TestKeyBalanceTokenPackageExpired(t *testing.T) {
	s, tok, _ := tokenPortalServer(t)
	key := paidTokenKey(t, s, tok)
	setTokenExpiryFor(t, s, "gsub", time.Now().Add(-time.Hour))
	s.invalidateTokenSub(key.ID)

	b := balanceOf(t, keyUsageBody(t, s, key))
	require.Equal(t, "token_package", b["type"])
	require.Equal(t, false, b["active"])
	require.EqualValues(t, 0, b["tokens_remaining"])
	require.EqualValues(t, 0, b["days_remaining"])
}

func TestKeyBalanceTokenOvershootClamps(t *testing.T) {
	s, tok, _ := tokenPortalServer(t)
	key := paidTokenKey(t, s, tok)
	recordTokens(t, s, "over", key.ID, 0, 100_000_000)

	b := balanceOf(t, keyUsageBody(t, s, key))
	require.EqualValues(t, 0, b["tokens_remaining"])
}

func TestKeyBalancePAYG(t *testing.T) {
	s, _ := paymentPortalTestServer(t)
	ctx := t.Context()
	u, err := s.db.PortalUsers().GetBySub(ctx, "gsub")
	require.NoError(t, err)
	require.NoError(t, s.budgets.Create(ctx, store.Budget{
		ID: "b-payg-" + u.KeyID, TenantID: store.DefaultTenantID, ScopeKind: store.ScopeAPIKey, ScopeID: u.KeyID,
		LimitMicros: 10_000_000, Period: "total", AlertPct: 80, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}))
	recordTokens(t, s, "payg-1", u.KeyID, 4_000_000, 500)
	key, err := s.identity.Get(ctx, u.KeyID)
	require.NoError(t, err)

	b := balanceOf(t, keyUsageBody(t, s, key))
	require.Equal(t, "payg", b["type"])
	require.InDelta(t, 10.0, b["usd_limit"], 1e-9)
	require.InDelta(t, 4.0, b["usd_spent"], 1e-9)
	require.InDelta(t, 6.0, b["usd_remaining"], 1e-9)
	require.IsType(t, false, b["alert"])
}

func TestKeyBalancePAYGNoBudget(t *testing.T) {
	s, _ := paymentPortalTestServer(t)
	ctx := t.Context()
	u, err := s.db.PortalUsers().GetBySub(ctx, "gsub")
	require.NoError(t, err)
	key, err := s.identity.Get(ctx, u.KeyID)
	require.NoError(t, err)

	b := balanceOf(t, keyUsageBody(t, s, key))
	require.Equal(t, "payg", b["type"])
	require.InDelta(t, 0.0, b["usd_limit"], 1e-9)
	require.InDelta(t, 0.0, b["usd_remaining"], 1e-9)
}

func TestDaysRemaining(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		expires time.Time
		want    int64
	}{
		{"15 days", now.Add(15 * 24 * time.Hour), 15},
		{"14 days 1 hour", now.Add(14*24*time.Hour + time.Hour), 15},
		{"1 minute", now.Add(time.Minute), 1},
		{"expired", now.Add(-time.Hour), 0},
		{"zero", time.Time{}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, daysRemaining(c.expires, now))
		})
	}
}

// TestKeyBalanceExpiredTokenKeyOverHTTP goes through the auth middleware: an
// expired token key still reads its own usage and balance, and nothing else.
func TestKeyBalanceExpiredTokenKeyOverHTTP(t *testing.T) {
	h := newE2E(t, openAIUpstream())
	// The harness leaves these unset; the usage handler needs them.
	h.gateway.budgets, h.gateway.usage = h.db.Budgets(), h.db.Usage()
	tokenKey, keyID := e2eTokenKey(t, h)
	setTokenExpiryFor(t, h.gateway, "sub-e2e", time.Now().Add(-time.Hour))
	h.gateway.invalidateTokenSub(keyID)

	resp := h.get(t, "/v1/keys/me/usage", tokenKey)
	body := readAllString(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	b := balanceOf(t, decodeJSONBody(t, []byte(body)))
	require.Equal(t, false, b["active"])
	require.EqualValues(t, 0, b["tokens_remaining"])

	resp = h.post(t, "/v1/chat/completions", `{"model":"gpt-6-sol","messages":[{"role":"user","content":"hi"}]}`, tokenKey)
	body = readAllString(t, resp)
	require.Equal(t, http.StatusForbidden, resp.StatusCode, body)
	require.Contains(t, body, "token_package_expired")
}
