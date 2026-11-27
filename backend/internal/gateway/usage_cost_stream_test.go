package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/store"
)

// costSSEUpstream streams an OpenAI answer whose usage frame also carries the
// upstream's own usage.cost (OpenRouter style), which must never reach the
// client.
func costSSEUpstream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	flush, _ := w.(http.Flusher)
	for _, l := range []string{
		`data: {"id":"s1","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"hello"}}]}`,
		`data: {"id":"s1","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: {"id":"s1","model":"gpt-4o","choices":[],"usage":{"prompt_tokens":1200,"completion_tokens":85,"total_tokens":1285,"cost":9.99}}`,
		`data: [DONE]`,
	} {
		fmt.Fprintf(w, "%s\n\n", l)
		if flush != nil {
			flush.Flush()
		}
	}
}

const (
	costChatStreamBody     = `{"model":"gpt-6-sol","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hi"}]}`
	costMessagesStreamBody = `{"model":"gpt-6-sol","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`
	costResponsesBody      = `{"model":"gpt-6-sol","stream":true,"input":"hi"}`
)

// sseDataObjects decodes every JSON `data:` payload of an SSE body.
func sseDataObjects(t *testing.T, body string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(body, "\n") {
		payload, ok := strings.CutPrefix(line, "data: ")
		if !ok || payload == "[DONE]" {
			continue
		}
		var obj map[string]any
		require.NoError(t, json.Unmarshal([]byte(payload), &obj), payload)
		out = append(out, obj)
	}
	return out
}

func TestStreamCost_EndToEnd_OpenAI(t *testing.T) {
	h := newE2E(t, costSSEUpstream, pricedE2E)
	_, _ = e2eTokenKey(t, h)
	keyID := paygKeyID(t, h)

	resp := h.post(t, "/v1/chat/completions", costChatStreamBody, h.apiKey)
	body := readAllString(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)

	billed := lastCostNanos(t, h, keyID)
	require.Positive(t, billed)
	var usages []map[string]any
	for _, obj := range sseDataObjects(t, body) {
		if u, ok := obj["usage"].(map[string]any); ok {
			usages = append(usages, u)
		}
	}
	require.Len(t, usages, 1, body)
	require.Equal(t, float64(billed)/1e9, usages[0]["cost"])
	require.Equal(t, 1285.0, usages[0]["total_tokens"])
	require.True(t, strings.HasSuffix(strings.TrimSpace(body), "data: [DONE]"), body)
}

func TestStreamCost_EndToEnd_Anthropic(t *testing.T) {
	h := newE2E(t, costSSEUpstream, pricedE2E)
	_, _ = e2eTokenKey(t, h)
	keyID := paygKeyID(t, h)

	resp := h.post(t, "/v1/messages", costMessagesStreamBody, h.apiKey)
	body := readAllString(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)

	billed := lastCostNanos(t, h, keyID)
	var delta map[string]any
	for _, obj := range sseDataObjects(t, body) {
		if obj["type"] == "message_delta" {
			delta = obj
		}
	}
	require.NotNil(t, delta, body)
	usage := delta["usage"].(map[string]any)
	require.Equal(t, float64(billed)/1e9, usage["cost"])
	require.Equal(t, 85.0, usage["output_tokens"])
}

func TestStreamCost_EndToEnd_Responses(t *testing.T) {
	h := newE2E(t, costSSEUpstream, pricedE2E)
	_, _ = e2eTokenKey(t, h)
	keyID := paygKeyID(t, h)

	resp := h.post(t, "/v1/responses", costResponsesBody, h.apiKey)
	body := readAllString(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)

	billed := lastCostNanos(t, h, keyID)
	var completed []map[string]any
	for _, obj := range sseDataObjects(t, body) {
		if obj["type"] == "response.completed" {
			completed = append(completed, obj)
		}
	}
	require.Len(t, completed, 1, body)
	usage := completed[0]["response"].(map[string]any)["usage"].(map[string]any)
	require.Equal(t, float64(billed)/1e9, usage["cost"])
}

func TestStreamCost_EndToEnd_TokenPackage(t *testing.T) {
	for _, c := range []struct{ path, body string }{
		{"/v1/chat/completions", costChatStreamBody},
		{"/v1/messages", costMessagesStreamBody},
		{"/v1/responses", costResponsesBody},
	} {
		t.Run(c.path, func(t *testing.T) {
			h := newE2E(t, costSSEUpstream, pricedE2E)
			tokenKey, _ := e2eTokenKey(t, h)

			resp := h.post(t, c.path, c.body, tokenKey)
			body := readAllString(t, resp)
			require.Equal(t, http.StatusOK, resp.StatusCode, body)
			require.NotContains(t, body, `"cost"`)
		})
	}
}

func TestStreamCost_Bansos(t *testing.T) {
	h := newE2E(t, costSSEUpstream, pricedE2E)
	_, _ = e2eTokenKey(t, h)
	keyID := paygKeyID(t, h)
	ctx := context.Background()
	h.gateway.settings = h.db.Settings() // the e2e harness wires no settings store
	require.NoError(t, h.gateway.saveBansos(ctx, bansosConfig{
		KeyID: keyID, Active: true, Mode: bansosModeUnlimited,
		NoticeEnabled: true, NoticeText: "NOTICE-TEXT", NoticeRate: 100,
	}))
	h.gateway.invalidateBansosNoticeCache()

	resp := h.post(t, "/v1/chat/completions", costChatStreamBody, h.apiKey)
	body := readAllString(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)

	noticeAt := strings.Index(body, "NOTICE-TEXT")
	finishAt := strings.Index(body, `"finish_reason":"stop"`)
	costAt := strings.Index(body, `"cost":`)
	doneAt := strings.Index(body, "data: [DONE]")
	require.Positive(t, noticeAt, body)
	require.Greater(t, finishAt, noticeAt, body)
	require.Greater(t, costAt, finishAt, body)
	require.Greater(t, doneAt, costAt, body)
}

// addPricedChain adds a gpt-6-priced chain whose operator price, not the
// upstream's reported usage.cost, sets the bill: the customer charge then
// differs from the upstream cost, so a leak of the latter is visible.
func addPricedChain(t *testing.T, h *e2eHarness) {
	t.Helper()
	now := time.Now()
	require.NoError(t, h.gateway.chains.Create(context.Background(), store.Chain{
		ID: "chain-priced", TenantID: store.DefaultTenantID, Name: "gpt-6-priced", Strategy: "fallback",
		InputPerM: 3, OutputPerM: 15,
		Steps:     []store.ChainStep{{ID: "step-priced", Position: 0, Provider: "openai", Model: "gpt-4o"}},
		CreatedAt: now, UpdatedAt: now,
	}))
}

// The upstream reports usage.cost 9.99; the chain bills 1200 in at $3/M plus
// 85 out at $15/M. Only the bill may reach the client.
func TestCost_UpstreamCostNeverLeaks(t *testing.T) {
	t.Run("stream", func(t *testing.T) {
		for _, c := range []struct{ name, path, body string }{
			{"chat include_usage", "/v1/chat/completions", `{"model":"gpt-6-priced","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hi"}]}`},
			{"chat direct-eligible", "/v1/chat/completions", `{"model":"gpt-6-priced","stream":true,"messages":[{"role":"user","content":"hi"}]}`},
			{"messages", "/v1/messages", `{"model":"gpt-6-priced","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`},
		} {
			t.Run(c.name, func(t *testing.T) {
				h := newE2E(t, costSSEUpstream, pricedE2E)
				addPricedChain(t, h)
				keyID := paygKeyID(t, h)

				resp := h.post(t, c.path, c.body, h.apiKey)
				body := readAllString(t, resp)
				require.Equal(t, http.StatusOK, resp.StatusCode, body)
				billed := lastCostNanos(t, h, keyID)
				require.Equal(t, int64(4875000), billed, "the chain price sets the bill")
				require.NotContains(t, body, "9.99")
			})
		}
	})
	t.Run("unary", func(t *testing.T) {
		h := newE2E(t, unaryUpstream(`{"role":"assistant","content":"hello"}`,
			`{"prompt_tokens":1200,"completion_tokens":85,"total_tokens":1285,"cost":9.99}`), pricedE2E)
		addPricedChain(t, h)
		keyID := paygKeyID(t, h)

		resp := h.post(t, "/v1/chat/completions", `{"model":"gpt-6-priced","messages":[{"role":"user","content":"hi"}]}`, h.apiKey)
		body := readAllString(t, resp)
		require.Equal(t, http.StatusOK, resp.StatusCode, body)
		require.NotContains(t, body, "9.99")
		require.Equal(t, int64(4875000), lastCostNanos(t, h, keyID))
		require.Equal(t, 0.004875, usageObject(t, body)["cost"])
	})
}

// A token package stream takes the direct path, where upstream frames are
// copied through; OpenRouter's own usage.cost must still never reach it.
func TestCost_TokenKeyDirectStreamHidesUpstreamCost(t *testing.T) {
	h := newE2E(t, costSSEUpstream, pricedE2E)
	tokenKey, _ := e2eTokenKey(t, h)

	resp := h.post(t, "/v1/chat/completions", `{"model":"gpt-6-sol","stream":true,"messages":[{"role":"user","content":"hi"}]}`, tokenKey)
	body := readAllString(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	require.Contains(t, body, `"completion_tokens":85`, "the usage frame itself is kept")
	require.NotContains(t, body, `"cost"`)
	require.NotContains(t, body, "9.99")
}

func TestStripUpstreamCost(t *testing.T) {
	plain := []byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"cost of living\"}}]}\n\n")
	require.Equal(t, plain, stripUpstreamCost(plain), "frames without a usage cost stay byte-for-byte")

	frame := []byte("data: {\"id\":\"s1\",\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"cost\":0.0123,\"cost_details\":{\"upstream_inference_cost\":0.01},\"buyer_cost_micro\":7}}\n\n")
	out := string(stripUpstreamCost(frame))
	require.NotContains(t, out, "cost")
	require.Contains(t, out, `"prompt_tokens":5`)
	require.True(t, strings.HasPrefix(out, "data: {"))
	require.True(t, strings.HasSuffix(out, "}\n\n"))

	responses := []byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":5,\"cost\":0.5}}}\n\n")
	out = string(stripUpstreamCost(responses))
	require.True(t, strings.HasPrefix(out, "event: response.completed\ndata: {"))
	require.NotContains(t, out, `"cost"`)
	require.Contains(t, out, `"input_tokens":5`)
}

// noUsageSSEUpstream answers without any usage frame, as many
// OpenAI-compatible providers do unless asked.
func noUsageSSEUpstream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, l := range []string{
		`data: {"id":"s1","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"hello there"}}]}`,
		`data: {"id":"s1","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	} {
		fmt.Fprintf(w, "%s\n\n", l)
	}
}

func TestStreamCost_EndToEnd_AnthropicNoUpstreamUsage(t *testing.T) {
	h := newE2E(t, noUsageSSEUpstream, pricedE2E)
	_, _ = e2eTokenKey(t, h)
	keyID := paygKeyID(t, h)

	resp := h.post(t, "/v1/messages", costMessagesStreamBody, h.apiKey)
	body := readAllString(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)

	billed := lastCostNanos(t, h, keyID)
	require.Positive(t, billed, "the estimate is billed")
	var delta map[string]any
	for _, obj := range sseDataObjects(t, body) {
		if obj["type"] == "message_delta" {
			delta = obj
		}
	}
	require.NotNil(t, delta, body)
	usage := delta["usage"].(map[string]any)
	require.Equal(t, float64(billed)/1e9, usage["cost"])
	require.Positive(t, usage["output_tokens"])
}
