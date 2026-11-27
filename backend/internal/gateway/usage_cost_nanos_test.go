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

// subMicroUsage at $0.15/M in and $0.60/M out costs 7*150 + 3*600 = 2850
// nanos: not a whole number of micros, so any rounding shows.
const subMicroUsage = `{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}`

func addSubMicroChain(t *testing.T, h *e2eHarness) {
	t.Helper()
	now := time.Now()
	require.NoError(t, h.gateway.chains.Create(context.Background(), store.Chain{
		ID: "chain-submicro", TenantID: store.DefaultTenantID, Name: "gpt-6-cheap", Strategy: "fallback",
		InputPerM: 0.15, OutputPerM: 0.6,
		Steps:     []store.ChainStep{{ID: "step-submicro", Position: 0, Provider: "openai", Model: "gpt-4o"}},
		CreatedAt: now, UpdatedAt: now,
	}))
}

func subMicroSSEUpstream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, l := range []string{
		`data: {"id":"s1","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"hello"}}]}`,
		`data: {"id":"s1","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: {"id":"s1","model":"gpt-4o","choices":[],"usage":` + subMicroUsage + `}`,
		`data: [DONE]`,
	} {
		fmt.Fprintf(w, "%s\n\n", l)
	}
}

var subMicroCases = []struct{ name, path, unary, stream string }{
	{"chat", "/v1/chat/completions",
		`{"model":"gpt-6-cheap","messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"gpt-6-cheap","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hi"}]}`},
	{"messages", "/v1/messages",
		`{"model":"gpt-6-cheap","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"gpt-6-cheap","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`},
	{"responses", "/v1/responses",
		`{"model":"gpt-6-cheap","input":"hi"}`,
		`{"model":"gpt-6-cheap","stream":true,"input":"hi"}`},
}

func TestCost_SubMicroChargeIsNotRounded(t *testing.T) {
	for _, c := range subMicroCases {
		t.Run(c.name+"/unary", func(t *testing.T) {
			h := newE2E(t, unaryUpstream(`{"role":"assistant","content":"hello"}`, subMicroUsage), pricedE2E)
			addSubMicroChain(t, h)
			keyID := paygKeyID(t, h)

			resp := h.post(t, c.path, c.unary, h.apiKey)
			body := readAllString(t, resp)
			require.Equal(t, http.StatusOK, resp.StatusCode, body)
			require.Equal(t, int64(2850), lastCostNanos(t, h, keyID))
			require.Contains(t, body, `"cost":0.00000285`)
		})
		t.Run(c.name+"/stream", func(t *testing.T) {
			h := newE2E(t, subMicroSSEUpstream, pricedE2E)
			addSubMicroChain(t, h)
			keyID := paygKeyID(t, h)

			resp := h.post(t, c.path, c.stream, h.apiKey)
			body := readAllString(t, resp)
			require.Equal(t, http.StatusOK, resp.StatusCode, body)
			require.Equal(t, int64(2850), lastCostNanos(t, h, keyID))
			require.Contains(t, body, `"cost":0.00000285`)
		})
	}
}
