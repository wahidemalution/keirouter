package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/config"
)

func getModels(t *testing.T, h http.Handler, key string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestE2E_ModelsListLimitedPerKey(t *testing.T) {
	h := newE2E(t, openAIUpstream(), func(o *e2eOptions) {
		o.cfg.Limits.ModelsRequestsPerMinute = 2
	})

	require.Equal(t, http.StatusOK, getModels(t, h.handler, h.apiKey).Code)
	require.Equal(t, http.StatusOK, getModels(t, h.handler, h.apiKey).Code)
	rec := getModels(t, h.handler, h.apiKey)
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.NotEmpty(t, rec.Header().Get("Retry-After"))
	require.Contains(t, rec.Body.String(), "2 model list requests per minute")

	// Raising the limit from the dashboard takes effect immediately.
	limit := 10
	h.gateway.applyLimitSettings(EndpointSettings{ModelsRequestsPerMinute: &limit})
	require.Equal(t, http.StatusOK, getModels(t, h.handler, h.apiKey).Code)

	// 0 turns it off.
	off := 0
	h.gateway.applyLimitSettings(EndpointSettings{ModelsRequestsPerMinute: &off})
	for range 20 {
		require.Equal(t, http.StatusOK, getModels(t, h.handler, h.apiKey).Code)
	}
}

func TestModelsLimitDefaultsTo60(t *testing.T) {
	cfg := config.Default()
	require.Equal(t, 60, cfg.Limits.ModelsRequestsPerMinute)
	s := &Server{cfg: cfg}
	es := s.effectiveLimitSettings(EndpointSettings{})
	require.Equal(t, 60, *es.ModelsRequestsPerMinute)
}
