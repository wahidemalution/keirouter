package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/config"
	"github.com/mydisha/keirouter/backend/internal/dispatch"
	"github.com/mydisha/keirouter/backend/internal/limits"
	"github.com/mydisha/keirouter/backend/internal/store"
	"github.com/mydisha/keirouter/backend/internal/tokenpkg"
)

const tokenChatBody = `{"model":"gpt-6-sol","messages":[{"role":"user","content":"hi"}]}`

// e2eTokenKey gives the harness a portal user whose token key holds an
// active subscription, and a gpt-6-sol chain routed to the fake upstream.
// It returns the token key's plaintext and id.
func e2eTokenKey(t *testing.T, h *e2eHarness) (string, string) {
	t.Helper()
	ctx := context.Background()
	s := h.gateway
	now := time.Now()
	payg, err := s.identity.Authenticate(ctx, h.apiKey)
	require.NoError(t, err)
	require.NoError(t, s.db.PortalUsers().Upsert(ctx, store.PortalUser{
		GoogleSub: "sub-e2e", Email: "e@example.com", KeyID: payg.ID, CreatedAt: now, UpdatedAt: now,
	}))
	issued, err := s.identity.Generate(store.DefaultTenantID, "", "token:e@example.com")
	require.NoError(t, err)
	issued.Record.PlanID = tokenpkg.DefaultPlanID
	require.NoError(t, s.identity.Keys().Create(ctx, issued.Record))
	tx, err := s.db.SQL().BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, s.db.TokenSubscriptions().CreateOnTx(ctx, tx, store.TokenSubscription{
		GoogleSub: "sub-e2e", KeyID: issued.Record.ID, ExpiresAt: now.Add(24 * time.Hour), CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, tx.Commit())
	require.NoError(t, s.db.Budgets().Create(ctx, store.Budget{
		ID: e2eTokenBudgetID(issued.Record.ID), TenantID: store.DefaultTenantID, ScopeKind: store.ScopeAPIKey,
		ScopeID: issued.Record.ID, LimitTokens: 1_000_000_000, Period: "total", HardCutoff: true,
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, s.chains.Create(ctx, store.Chain{
		ID: "chain-sol", TenantID: store.DefaultTenantID, Name: "gpt-6-sol", Strategy: "fallback",
		Steps:     []store.ChainStep{{ID: "step-sol", Position: 0, Provider: "openai", Model: "gpt-4o"}},
		CreatedAt: now, UpdatedAt: now,
	}))
	return issued.Plaintext, issued.Record.ID
}

// e2eTokenBudgetID names the token budget e2eTokenKey gives its key; token
// keys without one are refused.
func e2eTokenBudgetID(keyID string) string { return "tokbud-" + keyID }

func (h *e2eHarness) get(t *testing.T, path, auth string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, h.server.URL+path, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+auth)
	resp, err := h.server.Client().Do(req)
	require.NoError(t, err)
	return resp
}

func TestTokenKeyDeniedMediaEndpoints(t *testing.T) {
	h := newE2E(t, openAIUpstream())
	tokenKey, _ := e2eTokenKey(t, h)
	img := `{"model":"gpt-6-sol","prompt":"x","images":["data:image/png;base64,iVBORw0KGgo="],"max_tokens":10}`
	cases := []struct{ method, path, body string }{
		{http.MethodPost, "/v1/images/understanding", img},
		{http.MethodPost, "/v1/images/generations", `{"model":"gpt-6-sol","prompt":"x"}`},
		{http.MethodPost, "/v1/videos/generations", `{"model":"gpt-6-sol","prompt":"x"}`},
		{http.MethodGet, "/v1/videos/abc", ``},
		{http.MethodPost, "/v1/audio/speech", `{"model":"gpt-6-sol","input":"x"}`},
		{http.MethodPost, "/v1/audio/transcriptions", `{}`},
		{http.MethodPost, "/v1/embeddings", `{"model":"gpt-6-sol","input":"x"}`},
		{http.MethodPost, "/v1/search", `{"query":"x"}`},
		{http.MethodPost, "/v1/web/fetch", `{"url":"https://example.com"}`},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			var resp *http.Response
			if c.method == http.MethodGet {
				resp = h.get(t, c.path, tokenKey)
			} else {
				resp = h.post(t, c.path, c.body, tokenKey)
			}
			body := readAllString(t, resp)
			require.Equal(t, http.StatusForbidden, resp.StatusCode, body)
			require.Contains(t, body, "endpoint_not_allowed_for_token_package")
		})
	}
}

func TestTokenKeyAllowedChatEndpoints(t *testing.T) {
	h := newE2E(t, openAIUpstream())
	tokenKey, _ := e2eTokenKey(t, h)
	posts := []struct{ path, body string }{
		{"/v1/chat/completions", tokenChatBody},
		{"/v1/messages", `{"model":"gpt-6-sol","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`},
		{"/v1/messages/count_tokens", `{"model":"gpt-6-sol","messages":[{"role":"user","content":"hi"}]}`},
		{"/v1/responses", `{"model":"gpt-6-sol","input":"hi"}`},
		{"/v1beta/models/gpt-6-sol:generateContent", `{"contents":[{"parts":[{"text":"hi"}]}]}`},
	}
	for _, c := range posts {
		t.Run(c.path, func(t *testing.T) {
			resp := h.post(t, c.path, c.body, tokenKey)
			body := readAllString(t, resp)
			require.Equal(t, http.StatusOK, resp.StatusCode, body)
		})
	}
	for _, path := range []string{"/v1/models", "/v1/keys/me/usage"} {
		t.Run(path, func(t *testing.T) {
			resp := h.get(t, path, tokenKey)
			body := readAllString(t, resp)
			require.NotContains(t, body, "endpoint_not_allowed_for_token_package")
			require.NotEqual(t, http.StatusForbidden, resp.StatusCode, body)
		})
	}
}

func TestPAYGKeyMediaUnaffected(t *testing.T) {
	h := newE2E(t, openAIUpstream())
	_, _ = e2eTokenKey(t, h)
	for _, path := range []string{"/v1/images/understanding", "/v1/embeddings", "/v1/search"} {
		resp := h.post(t, path, `{"model":"openai/gpt-4o","input":"x","query":"x"}`, h.apiKey)
		body := readAllString(t, resp)
		require.NotContains(t, body, "endpoint_not_allowed_for_token_package", path)
	}
}

func TestTokenEndpointAllowlist(t *testing.T) {
	allowed := []struct{ method, path string }{
		{http.MethodPost, "/v1/chat/completions"},
		{http.MethodPost, "/v1/messages"},
		{http.MethodPost, "/v1/messages/count_tokens"},
		{http.MethodPost, "/v1/responses"},
		{http.MethodPost, "/v1beta/models/gpt-6-sol:streamGenerateContent"},
		{http.MethodPost, "/v1beta/models/gpt-6-sol:countTokens"},
		{http.MethodGet, "/v1/models"},
		{http.MethodGet, "/v1/models/info"},
		{http.MethodGet, "/v1/models/chat"},
		{http.MethodGet, "/v1/keys/me/usage"},
		{http.MethodGet, "/v1/pricing"},
	}
	for _, c := range allowed {
		require.True(t, tokenEndpointAllowed(c.method, c.path), "%s %s", c.method, c.path)
	}
	denied := []struct{ method, path string }{
		{http.MethodPost, "/v1/images/understanding"},
		{http.MethodPost, "/v1/some/future/endpoint"},
		{http.MethodGet, "/v1/chat/completions"},
		{http.MethodPost, "/v1/models"},
		{http.MethodPost, "/v1beta/models/a/b:generateContent"},
		{http.MethodPost, "/v1beta/models/"},
		{http.MethodPost, "/v1/chat/completions/extra"},
	}
	for _, c := range denied {
		require.False(t, tokenEndpointAllowed(c.method, c.path), "%s %s", c.method, c.path)
	}
}

// tokenRoute resolves model the way the inference handlers do, then applies
// the key's model access filter.
func tokenRoute(t *testing.T, s *Server, key store.APIKey, model string) []dispatch.Target {
	t.Helper()
	ctx := context.Background()
	res, err := resolveTargets(ctx, s.chains, s.db.Aliases(), nil, store.DefaultTenantID, model)
	if err != nil {
		return nil
	}
	got, err := s.filterAllowedTargets(ctx, key.ID, key.PlanID, model, res.PlanOpts.ChainID != "", res.Targets)
	require.NoError(t, err)
	return got
}

func e2eTokenRecord(t *testing.T, h *e2eHarness) store.APIKey {
	t.Helper()
	plain, _ := e2eTokenKey(t, h)
	key, err := h.gateway.identity.Authenticate(context.Background(), plain)
	require.NoError(t, err)
	return key
}

func TestTokenKeyRejectsProviderModelSyntax(t *testing.T) {
	h := newE2E(t, openAIUpstream())
	key := e2eTokenRecord(t, h)
	for _, model := range []string{"openai/gpt-6-sol", "azure/gpt-6-sol", "openai/gpt-4o"} {
		require.Empty(t, tokenRoute(t, h.gateway, key, model), model)
	}

	h2 := newE2E(t, openAIUpstream())
	plain, _ := e2eTokenKey(t, h2)
	resp := h2.post(t, "/v1/chat/completions", `{"model":"openai/gpt-6-sol","messages":[{"role":"user","content":"hi"}]}`, plain)
	body := readAllString(t, resp)
	require.Equal(t, http.StatusForbidden, resp.StatusCode, body)
	require.Contains(t, body, "not permitted to use model")
}

func TestTokenKeyRejectsAlias(t *testing.T) {
	h := newE2E(t, openAIUpstream())
	key := e2eTokenRecord(t, h)
	require.NoError(t, h.gateway.db.Aliases().Set(context.Background(), "gpt-6-terra", "openai/gpt-6-terra"))
	require.Empty(t, tokenRoute(t, h.gateway, key, "gpt-6-terra"))
}

func TestTokenKeyAllowsWhitelistedChain(t *testing.T) {
	h := newE2E(t, openAIUpstream())
	key := e2eTokenRecord(t, h)
	for _, model := range []string{"gpt-6-sol", "chain:gpt-6-sol", "  gpt-6-sol  "} {
		require.Len(t, tokenRoute(t, h.gateway, key, model), 1, model)
	}
}

func TestTokenKeyChainMatchIgnoresPlanCaseAndSpace(t *testing.T) {
	h := newE2E(t, openAIUpstream())
	key := e2eTokenRecord(t, h)
	ctx := context.Background()
	plan, err := h.gateway.db.Plans().Get(ctx, tokenpkg.DefaultPlanID)
	require.NoError(t, err)
	plan.AllowedModels = store.SetPlanAllowedModels([]string{" GPT-6-Sol "})
	require.NoError(t, h.gateway.db.Plans().Update(ctx, plan))
	require.Len(t, tokenRoute(t, h.gateway, key, "gpt-6-sol"), 1)
}

func TestTokenKeyRejectsChainOutsideWhitelist(t *testing.T) {
	h := newE2E(t, openAIUpstream())
	key := e2eTokenRecord(t, h)
	now := time.Now()
	require.NoError(t, h.gateway.chains.Create(context.Background(), store.Chain{
		ID: "chain-other", TenantID: store.DefaultTenantID, Name: "other", Strategy: "fallback",
		Steps:     []store.ChainStep{{ID: "step-other", Position: 0, Provider: "openai", Model: "gpt-6-sol"}},
		CreatedAt: now, UpdatedAt: now,
	}))
	require.Empty(t, tokenRoute(t, h.gateway, key, "other"), "a step model on the whitelist does not grant its chain")
}

func TestTokenKeyIgnoresPerKeyModelOverride(t *testing.T) {
	h := newE2E(t, openAIUpstream())
	key := e2eTokenRecord(t, h)
	require.NoError(t, h.gateway.identity.Keys().SetAllowedModels(context.Background(), key.ID, []string{"*"}))
	require.Empty(t, tokenRoute(t, h.gateway, key, "openai/gpt-4o"))
}

func TestAdminUpdatePlanGuardsTokenPlan(t *testing.T) {
	s, _ := newTokenTestServer(t)
	call := func(id, body string) *httptest.ResponseRecorder {
		return adminTokenCall(s, http.MethodPut, "/plans/"+id, body, map[string]string{"id": id}, s.adminUpdatePlan)
	}
	for _, body := range []string{
		`{"allowed_models":[]}`, `{"allowed_models":["  "]}`, `{"rpm_limit":0}`, `{"concurrency_limit":0}`,
	} {
		rec := call(tokenpkg.DefaultPlanID, body)
		require.Equal(t, http.StatusBadRequest, rec.Code, body+": "+rec.Body.String())
	}
	plan, err := s.db.Plans().Get(t.Context(), tokenpkg.DefaultPlanID)
	require.NoError(t, err)
	require.NotEmpty(t, store.GetPlanAllowedModels(plan))
	require.EqualValues(t, 60, plan.RPMLimit)
	require.EqualValues(t, 3, plan.ConcurrencyLimit)

	rec := call(tokenpkg.DefaultPlanID, `{"rpm_limit":30,"concurrency_limit":2}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = call("default", `{"allowed_models":[],"rpm_limit":0,"concurrency_limit":0}`)
	require.Equal(t, http.StatusOK, rec.Code, "other plans keep 0 = unlimited: "+rec.Body.String())
}

func TestAdminPutTokenSettingsRefusesUnlimitedRates(t *testing.T) {
	s, _ := newTokenTestServer(t)
	for _, body := range []string{`{"rpm_limit":0}`, `{"concurrency_limit":0}`} {
		rec := adminTokenCall(s, http.MethodPut, "/token-packages/settings", body, nil, s.adminPutTokenSettings)
		require.Equal(t, http.StatusBadRequest, rec.Code, body+": "+rec.Body.String())
	}
	rec := adminTokenCall(s, http.MethodPut, "/token-packages/settings", `{"rpm_limit":1,"concurrency_limit":1}`, nil, s.adminPutTokenSettings)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func TestAdminUpdateKeyRefusesTokenKeyModels(t *testing.T) {
	s, paygKey := newTokenTestServer(t)
	require.True(t, payTokenOrder(t, s, paygKey, "t1", "tp-100m"))
	sub, err := s.db.TokenSubscriptions().GetBySub(t.Context(), tokenTestSub)
	require.NoError(t, err)

	rec := adminTokenCall(s, http.MethodPatch, "/keys/"+sub.KeyID, `{"allowed_models":["*"],"disabled":true}`,
		map[string]string{"id": sub.KeyID}, s.adminUpdateKey)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	own, err := s.identity.Keys().GetAllowedModels(t.Context(), sub.KeyID)
	require.NoError(t, err)
	require.Empty(t, own)
	key, err := s.identity.Get(t.Context(), sub.KeyID)
	require.NoError(t, err)
	require.False(t, key.Disabled, "a refused request changes nothing")

	rec = adminTokenCall(s, http.MethodPatch, "/keys/"+sub.KeyID, `{"disabled":true}`,
		map[string]string{"id": sub.KeyID}, s.adminUpdateKey)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = adminTokenCall(s, http.MethodPatch, "/keys/"+paygKey, `{"allowed_models":["gpt-4o"]}`,
		map[string]string{"id": paygKey}, s.adminUpdateKey)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func TestLimiterAlwaysEnforceIgnoresDisabledSwitch(t *testing.T) {
	m := limits.NewMemory(limits.MemoryConfig{Enabled: false, Window: time.Minute})
	req := limits.Request{APIKeyID: "k", Limits: limits.EffectiveLimits{Concurrency: 1, AlwaysEnforce: true}}
	release, dec, err := m.Acquire(context.Background(), req)
	require.NoError(t, err)
	require.True(t, dec.Allowed)
	_, dec, err = m.Acquire(context.Background(), req)
	require.NoError(t, err)
	require.False(t, dec.Allowed, "second request exceeds concurrency 1")
	release(0)

	req.Limits.AlwaysEnforce = false
	for range 3 {
		_, dec, err = m.Acquire(context.Background(), req)
		require.NoError(t, err)
		require.True(t, dec.Allowed, "PAYG keys follow the global switch")
	}
}

func TestTokenKeyRateLimitedWhenGlobalLimitsOff(t *testing.T) {
	for _, c := range []struct{ path, body string }{
		{"/v1/chat/completions", tokenChatBody},
		{"/v1beta/models/gpt-6-sol:generateContent", `{"contents":[{"parts":[{"text":"hi"}]}]}`},
	} {
		t.Run(c.path, func(t *testing.T) {
			cfg := config.Default()
			cfg.Limits.Enabled = false
			lim := limits.NewMemory(limits.MemoryConfig{Enabled: false, Window: time.Minute})
			h := newE2E(t, openAIUpstream(), func(o *e2eOptions) {
				o.cfg = cfg
				o.limiter = lim
			})
			lim.SetEnabled(false) // the gateway applies the stored setting at startup
			tokenKey, _ := e2eTokenKey(t, h)
			ctx := context.Background()
			plan, err := h.gateway.db.Plans().Get(ctx, tokenpkg.DefaultPlanID)
			require.NoError(t, err)
			plan.RPMLimit = 1
			require.NoError(t, h.gateway.db.Plans().Update(ctx, plan))

			resp := h.post(t, c.path, c.body, tokenKey)
			body := readAllString(t, resp)
			require.Equal(t, http.StatusOK, resp.StatusCode, body)
			resp = h.post(t, c.path, c.body, tokenKey)
			body = readAllString(t, resp)
			require.Equal(t, http.StatusTooManyRequests, resp.StatusCode, body)

			for range 3 {
				resp := h.post(t, "/v1/chat/completions", `{"model":"openai/gpt-4o","messages":[{"role":"user","content":"hi"}]}`, h.apiKey)
				body := readAllString(t, resp)
				require.Equal(t, http.StatusOK, resp.StatusCode, "PAYG keys stay unlimited: "+body)
			}
		})
	}
}

func TestAdminDeletePAYGKeyStopsTokenKey(t *testing.T) {
	s, paygKey := newTokenTestServer(t)
	plain := tokenKeyPlaintext(t, s, paygKey)
	rec, called := callAuth(s, plain)
	require.Equal(t, http.StatusOK, rec.Code, "warm the token cache")
	require.True(t, called)

	del := httptest.NewRecorder()
	s.adminDeleteKey(del, withChiID(http.MethodDelete, "/keys/"+paygKey, paygKey))
	require.Equal(t, http.StatusNoContent, del.Code, del.Body.String())

	rec, called = callAuth(s, plain)
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	require.True(t, strings.Contains(rec.Body.String(), "token_package_expired"))
	require.False(t, called)
}
