package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/connectors"
	"github.com/mydisha/keirouter/backend/internal/costlookup"
)

func callCustomProvider(t *testing.T, h http.HandlerFunc, method, id string, body any) (int, string) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(method, "/custom-providers/"+id, bytes.NewReader(raw))
	if id != "" {
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", id)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec.Code, rec.Body.String()
}

func createSubrouter(t *testing.T, s *Server) string {
	t.Helper()
	code, body := callCustomProvider(t, s.adminCreateCustomProvider, http.MethodPost, "", map[string]any{
		"display_name": "Subrouter Test", "dialect": "openai", "base_url": "https://asiasouth.up.railway.app/v1"})
	require.Equal(t, http.StatusCreated, code, body)
	var out map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &out))
	id := out["id"].(string)
	t.Cleanup(func() { connectors.UnregisterDynamicProvider(id) })
	return id
}

func enableBody() map[string]any {
	return map[string]any{"cost_lookup": "subrouter", "cost_lookup_user_id": "19579",
		"cost_lookup_token": "sys-secret-token", "cost_lookup_token_name": "Stable API 05"}
}

func TestAdminCostLookupTokenNeverReturned(t *testing.T) {
	s, db := newCustomProviderTestServer(t)
	id := createSubrouter(t, s)

	code, body := callCustomProvider(t, s.adminUpdateCustomProvider, http.MethodPatch, id, enableBody())
	require.Equal(t, http.StatusOK, code, body)
	require.NotContains(t, body, "sys-secret-token")

	code, list := callCustomProvider(t, s.adminListCustomProviders, http.MethodGet, "", nil)
	require.Equal(t, http.StatusOK, code)
	require.NotContains(t, list, "sys-secret-token")
	var out struct{ Providers []map[string]any }
	require.NoError(t, json.Unmarshal([]byte(list), &out))
	require.Len(t, out.Providers, 1)
	p := out.Providers[0]
	require.Equal(t, "subrouter", p["cost_lookup"])
	require.Equal(t, "19579", p["cost_lookup_user_id"])
	require.Equal(t, "Stable API 05", p["cost_lookup_token_name"])
	require.Equal(t, true, p["cost_lookup_token_set"])
	_, hasToken := p["cost_lookup_token"]
	require.False(t, hasToken)

	stored, err := db.CustomProviders().GetProvider(context.Background(), id)
	require.NoError(t, err)
	require.NotEqual(t, "sys-secret-token", stored.CostLookupToken)
	plain, err := costlookup.OpenToken(s.vault.Sealer(), stored.CostLookupToken)
	require.NoError(t, err)
	require.Equal(t, "sys-secret-token", plain)
	require.True(t, connectors.CostLookupEnabled(id))

	code, body = callCustomProvider(t, s.adminUpdateCustomProvider, http.MethodPatch, id, map[string]any{"cost_lookup": ""})
	require.Equal(t, http.StatusOK, code, body)
	require.False(t, connectors.CostLookupEnabled(id))
}

func TestAdminCostLookupValidation(t *testing.T) {
	s, _ := newCustomProviderTestServer(t)
	id := createSubrouter(t, s)
	cases := map[string]map[string]any{
		"unknown kind": {"cost_lookup": "other", "cost_lookup_user_id": "1", "cost_lookup_token": "t"},
		"no user id":   {"cost_lookup": "subrouter", "cost_lookup_token": "t"},
		"no token":     {"cost_lookup": "subrouter", "cost_lookup_user_id": "1"},
		"bad base url": {"cost_lookup": "subrouter", "cost_lookup_user_id": "1", "cost_lookup_token": "t", "cost_lookup_base_url": "https://subrouter.ai/#x"},
		"private url":  {"cost_lookup": "subrouter", "cost_lookup_user_id": "1", "cost_lookup_token": "t", "cost_lookup_base_url": "http://127.0.0.1:8080"},
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			code, resp := callCustomProvider(t, s.adminUpdateCustomProvider, http.MethodPatch, id, body)
			require.Equal(t, http.StatusBadRequest, code, resp)
		})
	}
	require.False(t, connectors.CostLookupEnabled(id))
}

func TestAdminCostLookupKeepsTokenOnBlankUpdate(t *testing.T) {
	s, db := newCustomProviderTestServer(t)
	id := createSubrouter(t, s)
	code, body := callCustomProvider(t, s.adminUpdateCustomProvider, http.MethodPatch, id, enableBody())
	require.Equal(t, http.StatusOK, code, body)
	before, err := db.CustomProviders().GetProvider(context.Background(), id)
	require.NoError(t, err)
	require.NoError(t, db.CustomProviders().UpdateCostLookupStatus(context.Background(), id, "unauthorized"))

	code, body = callCustomProvider(t, s.adminUpdateCustomProvider, http.MethodPatch, id,
		map[string]any{"cost_lookup_token": "", "cost_lookup_token_name": "Other"})
	require.Equal(t, http.StatusOK, code, body)
	after, err := db.CustomProviders().GetProvider(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, before.CostLookupToken, after.CostLookupToken)
	require.Equal(t, "Other", after.CostLookupTokenName)
	require.Equal(t, "", after.CostLookupStatus, "changing cost-lookup settings clears the last status")
	require.True(t, strings.HasPrefix(body, "{"))
}

func callCustomProviderURL(t *testing.T, h http.HandlerFunc, target, id string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec.Code, rec.Body.String()
}
