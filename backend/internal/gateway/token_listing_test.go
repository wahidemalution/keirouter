package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/store"
)

// seedHiddenChain adds a chain the token plan does not whitelist.
func seedHiddenChain(t *testing.T, h *e2eHarness) {
	t.Helper()
	now := time.Now()
	require.NoError(t, h.gateway.chains.Create(context.Background(), store.Chain{
		ID: "chain-hidden", TenantID: store.DefaultTenantID, Name: "secret-chain-b", Strategy: "fallback",
		Steps:     []store.ChainStep{{ID: "step-hidden", Position: 0, Provider: "openai", Model: "gpt-4o"}},
		CreatedAt: now, UpdatedAt: now,
	}))
}

// connectCatalogAccounts lets the harness list catalog models for its openai
// account, which the harness leaves unwired.
func connectCatalogAccounts(h *e2eHarness) { h.gateway.accounts = h.gateway.db.Accounts() }

func listedIDs(t *testing.T, body string) []string {
	t.Helper()
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &out), body)
	ids := make([]string, 0, len(out.Data))
	for _, d := range out.Data {
		ids = append(ids, d.ID)
	}
	return ids
}

func TestTokenKeyModelsByKind(t *testing.T) {
	h := newE2E(t, openAIUpstream())
	tokenKey, _ := e2eTokenKey(t, h)
	seedHiddenChain(t, h)
	connectCatalogAccounts(h)

	for _, kind := range []string{"chains", "llm"} {
		resp := h.get(t, "/v1/models/"+kind, tokenKey)
		body := readAllString(t, resp)
		require.Equal(t, http.StatusOK, resp.StatusCode, body)
		require.Equal(t, []string{"gpt-6-sol"}, listedIDs(t, body), kind)
	}
	for _, kind := range []string{"embedding", "image", "stt", "tts", "search", "fetch"} {
		resp := h.get(t, "/v1/models/"+kind, tokenKey)
		body := readAllString(t, resp)
		require.Equal(t, http.StatusOK, resp.StatusCode, body)
		require.Empty(t, listedIDs(t, body), kind)
	}
	resp := h.get(t, "/v1/models/bogus", tokenKey)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestTokenKeyModelInfoAndPricingRefused(t *testing.T) {
	h := newE2E(t, openAIUpstream())
	tokenKey, _ := e2eTokenKey(t, h)
	connectCatalogAccounts(h)
	for _, path := range []string{"/v1/models/info?id=openai/gpt-4o", "/v1/pricing"} {
		resp := h.get(t, path, tokenKey)
		body := readAllString(t, resp)
		require.Equal(t, http.StatusForbidden, resp.StatusCode, path)
		require.Contains(t, body, "endpoint_not_allowed_for_token_package", path)
	}
}

func TestTokenKeyListingNeverShowsNonWhitelisted(t *testing.T) {
	h := newE2E(t, openAIUpstream())
	tokenKey, _ := e2eTokenKey(t, h)
	seedHiddenChain(t, h)
	connectCatalogAccounts(h)

	paths := []string{
		"/v1/models", "/v1/models/chains", "/v1/models/llm", "/v1/models/embedding",
		"/v1/models/image", "/v1/models/stt", "/v1/models/tts", "/v1/models/search",
		"/v1/models/fetch", "/v1/models/info?id=openai/gpt-4o", "/v1/pricing",
		"/v1/pricing?model=secret-chain-b", "/v1beta/models", "/v1/models/openai/gpt-4o",
	}
	for _, p := range paths {
		resp := h.get(t, p, tokenKey)
		body := readAllString(t, resp)
		for _, leak := range []string{"secret-chain-b", "openai", "gpt-4o", "\"provider\""} {
			require.NotContains(t, body, leak, "%s leaked %q: %s", p, leak, body)
		}
	}
}

func TestPAYGModelListingUnchanged(t *testing.T) {
	h := newE2E(t, openAIUpstream())
	_, _ = e2eTokenKey(t, h)
	seedHiddenChain(t, h)
	connectCatalogAccounts(h)

	resp := h.get(t, "/v1/models", h.apiKey)
	body := readAllString(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	require.ElementsMatch(t, []string{"gpt-6-sol", "secret-chain-b"}, listedIDs(t, body))

	resp = h.get(t, "/v1/models/chains", h.apiKey)
	require.ElementsMatch(t, []string{"gpt-6-sol", "secret-chain-b"}, listedIDs(t, readAllString(t, resp)))

	resp = h.get(t, "/v1/models/llm", h.apiKey)
	body = readAllString(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	require.Contains(t, listedIDs(t, body), "openai/gpt-4o")

	resp = h.get(t, "/v1/models/info?id=openai/gpt-4o", h.apiKey)
	body = readAllString(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	require.Contains(t, body, "gpt-4o")

	resp = h.get(t, "/v1/pricing", h.apiKey)
	require.NotEqual(t, http.StatusForbidden, resp.StatusCode)
}
