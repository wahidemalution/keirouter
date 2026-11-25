package gateway

import (
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
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

// TestImageGenerationClosedForAPIKeys: /v1/images/generations never debits
// (SEC-MEDIA-NO-DEBIT), so it refuses every API key before any upstream call.
// Image generation is offered through the customer portal only.
func TestImageGenerationClosedForAPIKeys(t *testing.T) {
	var imageCalls atomic.Int32
	inner := imageUpstream()
	h := newE2E(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/images/generations" {
			imageCalls.Add(1)
		}
		inner(w, r)
	})

	resp := h.post(t, "/v1/images/generations",
		`{"model":"openai/dall-e-3","prompt":"a cat"}`, h.apiKey)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	require.Contains(t, string(body), "image generation tersedia di panel")
	require.Equal(t, int32(0), imageCalls.Load(), "upstream must not be called")
}
