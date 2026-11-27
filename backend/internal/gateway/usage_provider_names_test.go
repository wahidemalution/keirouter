package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/store"
)

func TestPublicProviderName(t *testing.T) {
	s, _ := paymentPortalTestServer(t)
	info := requestLogInfo{
		s: s,
		chains: map[string]store.Chain{"c1": {ID: "c1", Steps: []store.ChainStep{
			{Provider: "custom-openai-a6api", Position: 0, DisplayName: "Alpha"},
			{Provider: "custom-openai-go-llm", Position: 1},
			{Provider: "custom-openai-surplus-alibaba-dashscope", Position: 2},
			{Provider: "custom-openai-mkt", Position: 3},
		}}},
		names: map[string]string{
			"custom-openai-go-llm": "Go LLM",
			"custom-openai-mkt":    "Surplus Beta",
		},
		official: map[string]*officialPrice{},
	}
	cases := []struct{ provider, chainID, want string }{
		{"custom-openai-a6api", "c1", "Alpha"},
		{"custom-openai-go-llm", "c1", "Go LLM"},
		{"custom-openai-surplus-alibaba-dashscope", "c1", "Provider 3"},
		{"custom-openai-mkt", "c1", "Beta"},
		{"custom-openai-x", "", "Provider"},
		{"custom-openai-x", "c1", "Provider"},
	}
	for _, c := range cases {
		t.Run(c.provider+"@"+c.chainID, func(t *testing.T) {
			require.Equal(t, c.want, info.publicProviderName(c.provider, c.chainID))
		})
	}
}

func TestKeyUsageModelsHideProviderIDs(t *testing.T) {
	s, tok := paymentPortalTestServer(t)
	ctx := t.Context()
	u, err := s.db.PortalUsers().GetBySub(ctx, "gsub")
	require.NoError(t, err)
	now := time.Now()
	require.NoError(t, s.chains.Create(ctx, store.Chain{
		ID: "chain-sol", TenantID: store.DefaultTenantID, Name: "gpt-6-sol", Strategy: "fallback",
		Steps: []store.ChainStep{
			{ID: "s1", Position: 0, Provider: "custom-openai-a6api", Model: "gpt-6-sol", DisplayName: "Alpha"},
			{ID: "s2", Position: 1, Provider: "custom-openai-go-llm", Model: "gpt-6-sol"},
		},
		CreatedAt: now, UpdatedAt: now,
	}))
	for i, p := range []string{"custom-openai-a6api", "custom-openai-go-llm"} {
		require.NoError(t, s.usage.Record(ctx, store.UsageRecord{
			ID: "r" + string(rune('a'+i)), TenantID: store.DefaultTenantID, APIKeyID: u.KeyID, Provider: p,
			Model: "gpt-6-sol", ChainID: "chain-sol", Status: "success", PromptTokens: 10, CreatedAt: now.UTC(),
		}))
	}
	key, err := s.identity.Get(ctx, u.KeyID)
	require.NoError(t, err)

	providers := func(body map[string]any) []string {
		var out []string
		for _, row := range body["models"].([]any) {
			out = append(out, row.(map[string]any)["provider"].(string))
		}
		return out
	}

	got := providers(keyUsageBody(t, s, key))
	require.ElementsMatch(t, []string{"Alpha", "Provider 2"}, got)

	rec := httptest.NewRecorder()
	s.handlePortalUsage(rec, portalReq("GET", "/portal/api/usage?days=7", "", tok))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	portal := providers(decodeJSONBody(t, rec.Body.Bytes()))
	require.ElementsMatch(t, []string{"Alpha", "Provider 2"}, portal)
	for _, p := range append(got, portal...) {
		require.False(t, strings.Contains(p, "custom-openai"), p)
	}
}
