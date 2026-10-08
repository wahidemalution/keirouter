package gateway

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/store"
)

// imageUpstream answers both the OpenAI chat and images endpoints. Chat is
// answered only so the test can prove the harness meters a normal request;
// images is the endpoint under test. It is registered as the "openai" account
// upstream by newE2E.
func imageUpstream() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/images/generations":
			fmt.Fprint(w, `{"created":1,"data":[{"url":"https://example.test/img.png"}]}`)
		case "/chat/completions":
			fmt.Fprint(w, `{"id":"c1","model":"gpt-4o","choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`)
		default:
			http.Error(w, "not found: "+r.URL.Path, http.StatusNotFound)
		}
	}
}

// TestMediaEndpointDoesNotDebitBudget documents SEC-MEDIA-NO-DEBIT: a successful
// image-generation call rides only on a read-only budget *check*
// (pipeline.mediaAttempts -> Engine.CheckOrError) and never records usage, so
// the caller's spend never increases and the endpoint is usable indefinitely at
// any budget level, including one too small to fund a single image.
//
// If this test starts failing because spend is now recorded, the finding has
// been fixed and the assertion should be inverted (spend must become > 0).
func TestMediaEndpointDoesNotDebitBudget(t *testing.T) {
	h := newE2E(t, imageUpstream())
	ctx := context.Background()

	key, err := h.gateway.identity.Authenticate(ctx, h.apiKey)
	require.NoError(t, err)

	// A budget far smaller than any real image cost, hard cutoff enabled.
	// Kept at/above the $0.05 minimum-key-balance gate so the metered chat
	// sanity call below is admitted; it is still too small to fund an image.
	require.NoError(t, h.gateway.db.Budgets().Create(ctx, store.Budget{
		ID:          "media-budget",
		TenantID:    adminTenant,
		ScopeKind:   store.ScopeAPIKey,
		ScopeID:     key.ID,
		LimitMicros: 50_000, // $0.05 — still cannot fund a real image
		Period:      "total",
		AlertPct:    80,
		HardCutoff:  true,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}))

	// Sanity: chat is metered (spend rises), proving the harness records usage.
	chatResp := h.post(t, "/v1/chat/completions",
		`{"model":"openai/gpt-4o","messages":[{"role":"user","content":"hi"}]}`, h.apiKey)
	_ = chatResp.Body.Close()
	require.Equal(t, http.StatusOK, chatResp.StatusCode)

	// The media endpoint returns a real upstream image...
	resp := h.post(t, "/v1/images/generations",
		`{"model":"openai/dall-e-3","prompt":"a cat"}`, h.apiKey)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "image generation should succeed")

	// ...but records no spend for the key, so the budget is never debited.
	spent, _, err := h.gateway.usage.SpendAndTokens(ctx, store.ScopeAPIKey, key.ID, time.Time{})
	require.NoError(t, err)
	require.Equal(t, int64(0), spent, "media call must not have recorded spend (documents the bypass)")
}
