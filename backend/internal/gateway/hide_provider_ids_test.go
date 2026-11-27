package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/store"
)

// leaksProviderID reports whether a customer-facing value carries a provider
// alias or the marketplace's name.
func leaksProviderID(v string) bool {
	l := strings.ToLower(v)
	return strings.Contains(l, "custom-") || strings.Contains(l, "surplus")
}

func createHeaderChain(t *testing.T, h *e2eHarness, display string) {
	t.Helper()
	now := time.Now()
	require.NoError(t, h.gateway.chains.Create(context.Background(), store.Chain{
		ID: "chain-hdr", TenantID: store.DefaultTenantID, Name: "hdr-model", Strategy: "fallback",
		Steps:     []store.ChainStep{{ID: "step-hdr", Position: 0, Provider: "openai", Model: "gpt-4o", DisplayName: display}},
		CreatedAt: now, UpdatedAt: now,
	}))
}

func TestChatHeadersShowStepName(t *testing.T) {
	h := newE2E(t, openAIUpstream())
	createHeaderChain(t, h, "Alpha")

	resp := h.post(t, "/v1/chat/completions", `{"model":"hdr-model","messages":[{"role":"user","content":"hi"}]}`, h.apiKey)
	body := readAllString(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	require.Equal(t, "Alpha", resp.Header.Get("X-KeiRouter-Provider"))
	require.Equal(t, "hdr-model", resp.Header.Get("X-KeiRouter-Model"), "the model the client asked for, not the upstream model")
}

func TestStreamHeadersShowStepName(t *testing.T) {
	h := newE2E(t, streamNoUsageUpstream())
	createHeaderChain(t, h, "Alpha")

	resp := h.post(t, "/v1/chat/completions", `{"model":"hdr-model","stream":true,"messages":[{"role":"user","content":"hi"}]}`, h.apiKey)
	body := readAllString(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	require.Equal(t, "Alpha", resp.Header.Get("X-KeiRouter-Provider"))
	require.Equal(t, "hdr-model", resp.Header.Get("X-KeiRouter-Model"))
}

func TestPublicProviderHeader(t *testing.T) {
	s, _ := paymentPortalTestServer(t)
	ctx := t.Context()
	now := time.Now()
	require.NoError(t, s.chains.Create(ctx, store.Chain{
		ID: "c-media", TenantID: store.DefaultTenantID, Name: "media-model", Strategy: "fallback",
		Steps: []store.ChainStep{
			{ID: "m1", Position: 0, Provider: "custom-openai-a6api", Model: "x", DisplayName: "Alpha"},
			{ID: "m2", Position: 1, Provider: "custom-openai-go-llm", Model: "x"},
		},
		CreatedAt: now, UpdatedAt: now,
	}))
	require.Equal(t, "Alpha", s.publicProviderHeader(ctx, store.DefaultTenantID, "custom-openai-a6api", "c-media"))
	require.Equal(t, "Provider 2", s.publicProviderHeader(ctx, store.DefaultTenantID, "custom-openai-go-llm", "c-media"))
	require.Equal(t, "Provider", s.publicProviderHeader(ctx, store.DefaultTenantID, "custom-openai-x", ""))
	require.Equal(t, "", s.publicProviderHeader(ctx, store.DefaultTenantID, "", "c-media"))
}

func TestMediaOptionsCarryChainID(t *testing.T) {
	h := newE2E(t, openAIUpstream())
	createHeaderChain(t, h, "Alpha")
	key, err := h.gateway.identity.Authenticate(context.Background(), h.apiKey)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", nil)
	req = req.WithContext(context.WithValue(req.Context(), apiKeyCtxKey, key))
	opts, err := h.gateway.mediaOptions(req, "hdr-model")
	require.NoError(t, err)
	require.Equal(t, "chain-hdr", opts.ChainID)
}

func TestPortalModelSettingsUseStepIDs(t *testing.T) {
	f := newModelSettingsFixture(t, twoProviderChain())

	_, _, rows := f.list(t)
	require.Len(t, rows, 1)
	ids := []string{}
	for _, p := range rows[0].Providers {
		ids = append(ids, p.ID)
		require.False(t, leaksProviderID(p.Name), p.Name)
	}
	require.Equal(t, []string{"ms1a", "ms1b", "ms1c"}, ids)
	require.Equal(t, "Provider 2", rows[0].Providers[1].Name, "no label anywhere falls back to the step position")

	rec := f.do(http.MethodPut, "/portal/api/model-settings/deepseek-v4.1-flash/providers/ms1a", `{"enabled":false}`, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var row settingsRowJSON
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &row))
	require.Equal(t, "ms1a", row.Providers[0].ID)
	require.False(t, row.Providers[0].Enabled)

	auth, err := f.srv.identity.Authenticate(context.Background(), f.plain)
	require.NoError(t, err)
	require.False(t, auth.ProviderEnabled("deepseek-v4.1-flash", "custom-openai-surplus-openrouter"),
		"a toggle by step id is stored under the real provider")
}

func TestResolvePortalProviderStepID(t *testing.T) {
	c := store.Chain{Steps: []store.ChainStep{
		{ID: "s1", Position: 0, Provider: "custom-openai-a6api"},
		{ID: "s2", Position: 1, Provider: "custom-openai-go-llm"},
		{ID: "s3", Position: 2, Provider: "custom-openai-a6api"},
	}}
	got, ok := resolvePortalProvider(c, "s2")
	require.True(t, ok)
	require.Equal(t, "custom-openai-go-llm", got)
	got, ok = resolvePortalProvider(c, "s3")
	require.True(t, ok)
	require.Equal(t, "custom-openai-a6api", got)
}

func TestPortalUsageRecentHidesProviderIDs(t *testing.T) {
	s, tok := paymentPortalTestServer(t)
	ctx := t.Context()
	u, err := s.db.PortalUsers().GetBySub(ctx, "gsub")
	require.NoError(t, err)
	now := time.Now()
	require.NoError(t, s.chains.Create(ctx, store.Chain{
		ID: "chain-r", TenantID: store.DefaultTenantID, Name: "r-model", Strategy: "fallback",
		Steps: []store.ChainStep{
			{ID: "r1", Position: 0, Provider: "custom-openai-a6api", Model: "x", DisplayName: "Alpha"},
			{ID: "r2", Position: 1, Provider: "custom-openai-go-llm", Model: "x"},
		},
		CreatedAt: now, UpdatedAt: now,
	}))
	for i, p := range []string{"custom-openai-a6api", "custom-openai-go-llm"} {
		require.NoError(t, s.usage.Record(ctx, store.UsageRecord{
			ID: "rr" + string(rune('a'+i)), TenantID: store.DefaultTenantID, APIKeyID: u.KeyID, Provider: p,
			Model: "x", ChainID: "chain-r", Status: "success", PromptTokens: 10, CreatedAt: now.UTC(),
		}))
	}

	rec := httptest.NewRecorder()
	s.handlePortalUsage(rec, portalReq(http.MethodGet, "/portal/api/usage?days=7", "", tok))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeJSONBody(t, rec.Body.Bytes())
	var got []string
	for _, row := range body["recent"].([]any) {
		got = append(got, row.(map[string]any)["provider"].(string))
	}
	require.ElementsMatch(t, []string{"Alpha", "Provider 2"}, got)
}
