package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/store"
	"github.com/mydisha/keirouter/backend/internal/tokenpkg"
)

var costKeyRe = regexp.MustCompile(`(?i)cost|usd|spent`)

// costKeyPaths lists the JSON path of every object key matching cost|usd|spent.
func costKeyPaths(v any, path string) []string {
	var out []string
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			if costKeyRe.MatchString(k) {
				out = append(out, path+"."+k)
			}
			out = append(out, costKeyPaths(child, path+"."+k)...)
		}
	case []any:
		for _, child := range x {
			out = append(out, costKeyPaths(child, path+"[]")...)
		}
	}
	return out
}

func decodeJSONBody(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(body, &m))
	return m
}

// paidTokenKey buys tp-100m for the portal session and returns the token key
// with one recorded request that cost 4 USD.
func paidTokenKey(t *testing.T, s *Server, tok string) store.APIKey {
	t.Helper()
	ctx := t.Context()
	rec := createTokenOrder(t, s, tok, `{"terms_version":"`+tokenpkg.TermsVersion+`","package_id":"tp-100m","idempotency_key":"nc1"}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var created map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	o, err := s.db.PaymentOrders().Get(ctx, created["id"].(string))
	require.NoError(t, err)
	_, err = s.markTokenOrderPaid(ctx, o, store.PaymentCompleted, "webhook", "")
	require.NoError(t, err)
	sub, err := s.db.TokenSubscriptions().GetBySub(ctx, "gsub")
	require.NoError(t, err)
	recordTokens(t, s, "tok-req", sub.KeyID, 4_000_000, 500)
	key, err := s.identity.Get(ctx, sub.KeyID)
	require.NoError(t, err)
	return key
}

func TestPortalTokenUsageHasNoCost(t *testing.T) {
	s, tok, _ := tokenPortalServer(t)
	paidTokenKey(t, s, tok)

	out := httptest.NewRecorder()
	s.handlePortalTokenUsage(out, portalReq(http.MethodGet, "/portal/api/token/usage?days=7", "", tok))
	require.Equal(t, http.StatusOK, out.Code, out.Body.String())
	body := decodeJSONBody(t, out.Body.Bytes())
	require.Empty(t, costKeyPaths(body, ""))
	require.NotEmpty(t, body["recent"], "usage rows stay, only cost goes")
	require.Contains(t, body, "current_period")
	require.Contains(t, body, "budgets")
}

func TestPortalUsagePAYGKeepsCost(t *testing.T) {
	s, tok, _ := tokenPortalServer(t)
	u, err := s.db.PortalUsers().GetBySub(t.Context(), "gsub")
	require.NoError(t, err)
	recordTokens(t, s, "payg-cost", u.KeyID, 4_000_000, 500)

	out := httptest.NewRecorder()
	s.handlePortalUsage(out, portalReq(http.MethodGet, "/portal/api/usage?days=7", "", tok))
	require.Equal(t, http.StatusOK, out.Code, out.Body.String())
	body := decodeJSONBody(t, out.Body.Bytes())
	period := body["current_period"].(map[string]any)
	require.InDelta(t, 4.0, period["cost_usd"], 1e-9)
	require.NotEmpty(t, costKeyPaths(body, ""))
}

func TestKeysMeUsageTokenKeyHasNoCost(t *testing.T) {
	s, tok, _ := tokenPortalServer(t)
	tokenKey := paidTokenKey(t, s, tok)
	u, err := s.db.PortalUsers().GetBySub(t.Context(), "gsub")
	require.NoError(t, err)
	recordTokens(t, s, "payg-cost", u.KeyID, 4_000_000, 500)
	paygKey, err := s.identity.Get(t.Context(), u.KeyID)
	require.NoError(t, err)

	call := func(key store.APIKey) map[string]any {
		req := httptest.NewRequest(http.MethodGet, "/v1/keys/me/usage", nil)
		req = req.WithContext(context.WithValue(req.Context(), apiKeyCtxKey, key))
		rec := httptest.NewRecorder()
		s.handleKeyUsage(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		return decodeJSONBody(t, rec.Body.Bytes())
	}
	got := call(tokenKey)
	require.Empty(t, costKeyPaths(got, ""))
	require.Contains(t, got, "current_period")
	require.NotEmpty(t, got["budgets"])

	payg := call(paygKey)
	require.Contains(t, payg["current_period"], "cost_usd", "PAYG keeps cost")
}
