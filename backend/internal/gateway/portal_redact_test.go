package gateway

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/store"
)

func TestPortalProviderIDAndName(t *testing.T) {
	for _, tc := range []struct{ id, name, wantID, wantName string }{
		{"custom-openai-surplus-dashscope-cn", "Alibaba DashScope · China", "dashscope-cn", "Alibaba DashScope · China"},
		{"custom-openai-surplus-openrouter", "Surplus OpenRouter", "openrouter", "OpenRouter"},
		{"custom-openai-surplus-openrouter", "custom-openai-surplus-openrouter", "openrouter", "openrouter"},
		{"custom-openai-surplus", "Surplus", "market", "Otomatis"},
		{"custom-openai-surplus", "custom-openai-surplus", "market", "Otomatis"},
		{"deepseek", "DeepSeek", "deepseek", "DeepSeek"},
		{"custom-openai-x", "", "custom-openai-x", "custom-openai-x"},
	} {
		require.Equal(t, tc.wantID, portalProviderID(tc.id), tc.id)
		require.Equal(t, tc.wantName, portalProviderName(tc.name, tc.id), tc.name)
	}
}

func TestResolvePortalProvider(t *testing.T) {
	c := store.Chain{Steps: []store.ChainStep{
		{Provider: "custom-openai-surplus-openrouter"},
		{Provider: "custom-openai-surplus"},
		{Provider: "deepseek"},
	}}
	for in, want := range map[string]string{
		"openrouter":                       "custom-openai-surplus-openrouter",
		"custom-openai-surplus-openrouter": "custom-openai-surplus-openrouter",
		"market":                           "custom-openai-surplus",
		"deepseek":                         "deepseek",
	} {
		got, ok := resolvePortalProvider(c, in)
		require.True(t, ok, in)
		require.Equal(t, want, got, in)
	}
	_, ok := resolvePortalProvider(c, "nope")
	require.False(t, ok)

	// Two aliases that redact to the same id are ambiguous.
	dup := store.Chain{Steps: []store.ChainStep{{Provider: "custom-openai-surplus-zai"}, {Provider: "surplus-zai"}}}
	_, ok = resolvePortalProvider(dup, "zai")
	require.False(t, ok)
}

// The portal's Model Settings and usage never name the marketplace, and a
// provider toggled by its portal id is stored under its real alias.
func TestPortalResponsesHideMarketplace(t *testing.T) {
	chain := twoProviderChain()
	chain.Steps = append(chain.Steps, store.ChainStep{ID: "ms1d", Position: 3,
		Provider: "custom-openai-surplus", Model: "deepseek-v4.1-flash", MarketSlug: "surplus-trusted:deepseek-v4.1-flash"})
	f := newModelSettingsFixture(t, chain)
	ctx := context.Background()
	for id, name := range map[string]string{"custom-openai-surplus": "Surplus", "custom-openai-surplus-openrouter": "Surplus OpenRouter"} {
		require.NoError(t, f.srv.db.CustomProviders().CreateProvider(ctx, store.CustomProvider{
			ID: id, TenantID: adminTenant, DisplayName: name, Alias: strings.ReplaceAll(id, "-", ""),
			Dialect: "openai", BaseURL: "https://example.test/v1",
		}))
	}
	f.mux.Get("/portal/api/usage", f.srv.handlePortalUsage)

	rec := f.do(http.MethodGet, "/portal/api/model-settings", "", true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotContains(t, strings.ToLower(rec.Body.String()), "surplus")

	rec = f.do(http.MethodPut, "/portal/api/model-settings/deepseek-v4.1-flash/providers/openrouter", `{"enabled":false}`, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotContains(t, strings.ToLower(rec.Body.String()), "surplus")
	routing, err := f.srv.db.APIKeys().KeyRouting(ctx, f.keyID)
	require.NoError(t, err)
	require.Contains(t, routing.DisabledProviders["deepseek-v4.1-flash"], "custom-openai-surplus-openrouter")

	now := time.Now().UTC()
	for i, provider := range []string{"custom-openai-surplus", "custom-openai-surplus-openrouter"} {
		require.NoError(t, f.srv.db.Usage().Record(ctx, store.UsageRecord{
			ID: fmt.Sprintf("u-redact-%d", i), TenantID: store.DefaultTenantID, APIKeyID: f.keyID, ChainID: chain.ID,
			Provider: provider, Model: "deepseek-v4.1-flash", Status: "success",
			PromptTokens: 10, CompletionTokens: 5, CostNanos: 1000, CreatedAt: now.Add(time.Duration(-i) * time.Second),
		}))
	}
	rec = f.do(http.MethodGet, "/portal/api/usage", "", true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := rec.Body.String()
	require.NotContains(t, strings.ToLower(body), "surplus")
	require.Contains(t, body, `"provider_name":"Otomatis"`)
	require.Contains(t, body, `"provider":"openrouter"`)
}
