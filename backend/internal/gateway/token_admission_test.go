package gateway

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/store"
)

// reasoningOnlyLengthSSE finishes on max_tokens with no visible output and
// reports the hidden reasoning only as completion tokens, the way a6api does.
func reasoningOnlyLengthSSE(calls *atomic.Int32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, l := range []string{
			`data: {"id":"s1","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`,
			`data: {"id":"s1","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"length"}]}`,
			`data: {"id":"s1","model":"gpt-4o","choices":[],"usage":{"prompt_tokens":50,"completion_tokens":3000,"total_tokens":3050}}`,
			`data: [DONE]`,
		} {
			fmt.Fprintf(w, "%s\n\n", l)
		}
	}
}

// addFallbackStep gives the token chain a second step, so a rejected attempt
// would be retried there.
func addFallbackStep(t *testing.T, h *e2eHarness) {
	t.Helper()
	ctx := context.Background()
	chain, err := h.gateway.chains.Get(ctx, "chain-sol")
	require.NoError(t, err)
	chain.Steps = append(chain.Steps, store.ChainStep{ID: "step-sol-2", Position: 1, Provider: "openai", Model: "gpt-4o-mini"})
	require.NoError(t, h.gateway.chains.Update(ctx, chain))
}

// channelStreamPaths all take the parsed channel path against the OpenAI
// upstream: tools force it for chat, the others are translated.
var channelStreamPaths = []struct{ name, path, body string }{
	{"chat_tools", "/v1/chat/completions", `{"model":"gpt-6-sol","stream":true,"max_tokens":4000,"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}]}`},
	{"messages", "/v1/messages", `{"model":"gpt-6-sol","stream":true,"max_tokens":4000,"messages":[{"role":"user","content":"hi"}]}`},
	{"responses", "/v1/responses", `{"model":"gpt-6-sol","stream":true,"max_output_tokens":4000,"input":"hi"}`},
}

// A channel stream that spent its whole output on hidden reasoning is billed
// the usage the upstream reported, once, with no fallback.
func TestTokenReasoningOnlyChannelStreamChargedReportedUsage(t *testing.T) {
	for _, c := range channelStreamPaths {
		t.Run(c.name, func(t *testing.T) {
			var calls atomic.Int32
			h := newE2E(t, reasoningOnlyLengthSSE(&calls))
			h.budget.SetTokenHoldTail(0)
			tokenKey, keyID := e2eTokenKey(t, h)
			addKeyBudget(t, h, keyID, 0, 1_000_000)
			addFallbackStep(t, h)

			_ = readAllString(t, h.post(t, c.path, c.body, tokenKey))

			row := lastUsageRow(t, h, keyID)
			require.Equal(t, "success", row.Status)
			require.Equal(t, 50, row.Prompt)
			require.Equal(t, 3000, row.Completion, "the reported reasoning tokens are billed")
			require.EqualValues(t, 1, calls.Load(), "no fallback after a charged attempt")
			require.Eventually(t, func() bool { return heldTokens(t, h, keyID) == 0 },
				3*time.Second, 20*time.Millisecond, "the hold is settled")
			require.Equal(t, int64(3050), recordedTokens(t, h, keyID))
		})
	}
}

// PAYG keeps the old admission: no cost reported, so the attempt is rejected
// and the chain falls back.
func TestPAYGReasoningOnlyChannelStreamUnchanged(t *testing.T) {
	var calls atomic.Int32
	h := newE2E(t, reasoningOnlyLengthSSE(&calls))
	_, _ = e2eTokenKey(t, h)
	keyID := paygKeyID(t, h)
	addFallbackStep(t, h)

	_ = readAllString(t, h.post(t, channelStreamPaths[1].path, channelStreamPaths[1].body, h.apiKey))

	row := lastUsageRow(t, h, keyID)
	require.Equal(t, "failed", row.Status)
	require.Zero(t, row.Prompt)
	require.Zero(t, row.Completion)
	require.EqualValues(t, 2, calls.Load(), "PAYG still falls back")
}

// An attempt rejected after the upstream reported output (here an error
// event follows the usage frame) is billed that usage and not retried.
func TestTokenRejectedChannelStreamKeepsReportedUsage(t *testing.T) {
	var calls atomic.Int32
	upstream := func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, l := range []string{
			`data: {"id":"s1","model":"gpt-4o","choices":[],"usage":{"prompt_tokens":50,"completion_tokens":3000,"total_tokens":3050}}`,
			`data: {"error":{"message":"upstream overloaded","type":"server_error"}}`,
		} {
			fmt.Fprintf(w, "%s\n\n", l)
		}
	}
	h := newE2E(t, upstream)
	h.budget.SetTokenHoldTail(0)
	tokenKey, keyID := e2eTokenKey(t, h)
	addKeyBudget(t, h, keyID, 0, 1_000_000)
	addFallbackStep(t, h)

	_ = readAllString(t, h.post(t, channelStreamPaths[1].path, channelStreamPaths[1].body, tokenKey))

	row := lastUsageRow(t, h, keyID)
	require.Equal(t, "failed", row.Status)
	require.Equal(t, 50, row.Prompt)
	require.Equal(t, 3000, row.Completion)
	require.EqualValues(t, 1, calls.Load(), "no fallback after a charged attempt")
	require.Eventually(t, func() bool { return heldTokens(t, h, keyID) == 0 },
		3*time.Second, 20*time.Millisecond)
	require.Equal(t, int64(3050), recordedTokens(t, h, keyID))
}
