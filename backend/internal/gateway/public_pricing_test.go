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

func adminPublicPricing(t *testing.T, gw *Server, method, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, "/api/public-pricing", strings.NewReader(body))
	rec := httptest.NewRecorder()
	if method == http.MethodPut {
		gw.adminUpdatePublicPricing(rec, req)
	} else {
		gw.adminGetPublicPricing(rec, req)
	}
	var out map[string]any
	if rec.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	}
	return rec, out
}

func getPublicPricing(t *testing.T, h http.Handler, ip, query string) (*httptest.ResponseRecorder, pricingResp) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/public/pricing"+query, nil)
	req.RemoteAddr = ip + ":40000"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out pricingResp
	if rec.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	}
	return rec, out
}

// Nothing is public until an admin turns it on and picks chains; then only
// the picked chains are shown, from any upstream, with no API key.
func TestPublicPricingOnlyPublishedChains(t *testing.T) {
	gw, _, _ := pricingFixture(t)
	h := gw.routes()

	rec, _ := getPublicPricing(t, h, "203.0.113.1", "")
	require.Equal(t, http.StatusNotFound, rec.Code, "off by default")

	// The admin tab lists every chain, none published yet.
	rec, cfg := adminPublicPricing(t, gw, http.MethodGet, "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, false, cfg["enabled"])
	models := cfg["models"].([]any)
	require.Len(t, models, 3, "fixed is listed too")
	require.Equal(t, "fixed", models[0].(map[string]any)["model"])
	require.Equal(t, false, models[0].(map[string]any)["published"])

	rec, _ = adminPublicPricing(t, gw, http.MethodPut, `{"enabled":true,"chain_ids":["nope"]}`)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "unknown chain: nope")

	rec, cfg = adminPublicPricing(t, gw, http.MethodPut, `{"enabled":true,"chain_ids":["glm","glm","fixed"]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, []any{"fixed", "glm"}, cfg["chain_ids"])

	rec, out := getPublicPricing(t, h, "203.0.113.1", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "public, max-age=5", rec.Header().Get("Cache-Control"))
	require.Equal(t, "*", rec.Header().Get("Access-Control-Allow-Origin"))
	require.NotContains(t, rec.Body.String(), `"source"`)
	require.Len(t, out.Models, 2)
	byName := map[string]map[string]any{}
	for _, m := range out.Models {
		byName[m["model"].(string)] = m
	}
	require.InDelta(t, 0.25, byName["glm-trusted"]["input_per_m"], 1e-9)
	require.Equal(t, "fixed", byName["fixed"]["price_type"])
	require.NotEmpty(t, byName["glm-trusted"]["providers"])

	// Unpublished chains are skipped, whatever the caller asks for.
	rec, out = getPublicPricing(t, h, "203.0.113.1", "?model=glm-trusted,luna-trusted")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, out.Models, 1)
	rec, _ = getPublicPricing(t, h, "203.0.113.1", "?model=luna-trusted")
	require.Equal(t, http.StatusNotFound, rec.Code)

	// Turning it off hides everything again.
	rec, _ = adminPublicPricing(t, gw, http.MethodPut, `{"enabled":false,"chain_ids":["glm"]}`)
	require.Equal(t, http.StatusOK, rec.Code)
	rec, _ = getPublicPricing(t, h, "203.0.113.1", "")
	require.Equal(t, http.StatusNotFound, rec.Code)
}

// A published chain that is hidden or in maintenance answers exactly like a
// name that does not exist, so the public cannot probe for it.
func TestPublicPricingHiddenChainLooksMissing(t *testing.T) {
	gw, _, _ := pricingFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	require.NoError(t, gw.chains.Create(ctx, store.Chain{ID: "secret", TenantID: store.DefaultTenantID, Name: "secret-model",
		Strategy: "priority", Hidden: true, InputPerM: 1, OutputPerM: 1, CreatedAt: now, UpdatedAt: now,
		Steps: []store.ChainStep{{ID: "s9", Provider: "openai", Model: "gpt-4o"}}}))
	rec, _ := adminPublicPricing(t, gw, http.MethodPut, `{"enabled":true,"chain_ids":["glm","secret"]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	h := gw.routes()

	hidden, _ := getPublicPricing(t, h, "203.0.113.2", "?model=secret-model")
	missing, _ := getPublicPricing(t, h, "203.0.113.2", "?model=no-such-model")
	require.Equal(t, missing.Code, hidden.Code)
	require.JSONEq(t, missing.Body.String(), hidden.Body.String())
	rec, out := getPublicPricing(t, h, "203.0.113.2", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, out.Models, 1)
}

// Nothing internal reaches the public body.
func TestPublicPricingLeaksNothingInternal(t *testing.T) {
	gw, _, _ := pricingFixture(t)
	rec, _ := adminPublicPricing(t, gw, http.MethodPut, `{"enabled":true,"chain_ids":["glm","luna","fixed"]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec, _ = getPublicPricing(t, gw.routes(), "203.0.113.3", "")
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	for _, leak := range []string{"custom-openai", "surplus-trusted", "markup", "zai", `"s1"`, `"s2"`, `"s3"`, `"glm"`, `"luna"`, "chain_id"} {
		require.NotContains(t, body, leak)
	}
}

// Each client IP gets 30 requests a minute; another IP is unaffected.
func TestPublicPricingLimitsPerIP(t *testing.T) {
	gw, _, _ := pricingFixture(t)
	rec, _ := adminPublicPricing(t, gw, http.MethodPut, `{"enabled":true,"chain_ids":["glm"]}`)
	require.Equal(t, http.StatusOK, rec.Code)
	h := gw.routes()

	for i := 0; i < pricingRequestsPerMinute; i++ {
		rec, _ = getPublicPricing(t, h, "203.0.113.5", "")
		require.Equal(t, http.StatusOK, rec.Code, "request %d", i+1)
	}
	rec, _ = getPublicPricing(t, h, "203.0.113.5", "")
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.NotEmpty(t, rec.Header().Get("Retry-After"))

	rec, _ = getPublicPricing(t, h, "203.0.113.6", "")
	require.Equal(t, http.StatusOK, rec.Code)
}
