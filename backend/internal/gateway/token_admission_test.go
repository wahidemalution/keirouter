package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
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

// silentSSEUpstream opens the stream and then sends nothing until the
// gateway gives up, like a model reasoning before its first token.
func silentSSEUpstream(calls *atomic.Int32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		zeroByteSSEUpstream(w, r)
		<-r.Context().Done()
	}
}

// A stream the gateway times out during admission was accepted upstream and
// may have been generating: a token key is charged the cut-stream amount,
// once, with no fallback.
func TestTokenStreamAdmissionTimeoutCharged(t *testing.T) {
	for _, c := range channelStreamPaths[:2] {
		t.Run(c.name, func(t *testing.T) {
			var calls atomic.Int32
			h := newE2E(t, silentSSEUpstream(&calls), func(o *e2eOptions) { o.stallTimeout = 300 * time.Millisecond })
			h.budget.SetTokenHoldTail(0)
			tokenKey, keyID := e2eTokenKey(t, h)
			addKeyBudget(t, h, keyID, 0, 1_000_000)
			addFallbackStep(t, h)

			_ = readAllString(t, h.post(t, c.path, c.body, tokenKey))

			row := lastUsageRow(t, h, keyID)
			require.Equal(t, 4000, row.Completion, "charged the output cap (status %s)", row.Status)
			require.Positive(t, row.Prompt)
			require.EqualValues(t, 1, calls.Load(), "no free retry after a charged timeout")
			require.Eventually(t, func() bool { return heldTokens(t, h, keyID) == 0 },
				3*time.Second, 20*time.Millisecond)
			require.Equal(t, int64(row.Prompt+row.Completion), recordedTokens(t, h, keyID))
		})
	}
}

func TestPAYGStreamAdmissionTimeoutUnchanged(t *testing.T) {
	var calls atomic.Int32
	h := newE2E(t, silentSSEUpstream(&calls), func(o *e2eOptions) { o.stallTimeout = 300 * time.Millisecond })
	_, _ = e2eTokenKey(t, h)
	keyID := paygKeyID(t, h)
	addFallbackStep(t, h)

	_ = readAllString(t, h.post(t, channelStreamPaths[1].path, channelStreamPaths[1].body, h.apiKey))

	row := lastUsageRow(t, h, keyID)
	require.Zero(t, row.Prompt)
	require.Zero(t, row.Completion)
	require.EqualValues(t, 2, calls.Load(), "PAYG still falls back")
}

const tokenUnaryBody = `{"model":"gpt-6-sol","max_tokens":4000,"messages":[{"role":"user","content":"hi"}]}`

// hangingUnaryUpstream receives the request and never answers.
func hangingUnaryUpstream(w http.ResponseWriter, r *http.Request) {
	_, _ = io.ReadAll(r.Body)
	<-r.Context().Done()
}

// A unary request that hits the request deadline after it was sent upstream
// is charged the prompt estimate and output cap.
func TestTokenUnaryDeadlineChargedOnceSent(t *testing.T) {
	h := newE2E(t, hangingUnaryUpstream, func(o *e2eOptions) { o.requestTimeout = 300 * time.Millisecond })
	h.budget.SetTokenHoldTail(0)
	tokenKey, keyID := e2eTokenKey(t, h)
	addKeyBudget(t, h, keyID, 0, 1_000_000)

	_ = readAllString(t, h.post(t, "/v1/chat/completions", tokenUnaryBody, tokenKey))

	row := lastUsageRow(t, h, keyID)
	require.Equal(t, "failed", row.Status)
	require.Equal(t, 4000, row.Completion)
	require.Positive(t, row.Prompt)
	require.Eventually(t, func() bool { return heldTokens(t, h, keyID) == 0 },
		3*time.Second, 20*time.Millisecond)
	require.Equal(t, int64(row.Prompt+row.Completion), recordedTokens(t, h, keyID))
}

func TestPAYGUnaryDeadlineUnchanged(t *testing.T) {
	h := newE2E(t, hangingUnaryUpstream, func(o *e2eOptions) { o.requestTimeout = 300 * time.Millisecond })
	_, _ = e2eTokenKey(t, h)
	keyID := paygKeyID(t, h)

	_ = readAllString(t, h.post(t, "/v1/chat/completions", tokenUnaryBody, h.apiKey))

	row := lastUsageRow(t, h, keyID)
	require.Equal(t, "failed", row.Status)
	require.Zero(t, row.Prompt)
	require.Zero(t, row.Completion)
}

// headerlessUpstream reads the request and holds the response headers until
// the gateway hangs up, like new-api waiting for the first chunk.
func headerlessUpstream(received chan<- struct{}) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		select {
		case received <- struct{}{}:
		default:
		}
		<-r.Context().Done()
	}
}

// cancelWhenReceived sends a request and hangs up once the upstream has it.
func cancelWhenReceived(t *testing.T, h *e2eHarness, received <-chan struct{}, path, body, auth string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.server.URL+path, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+auth)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()
	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream never received the request")
	}
	cancel()
	<-done
}

// A client that leaves while the upstream holds the response headers is
// charged as a cut stream: the request was sent and may be generating.
func TestTokenStreamCancelBeforeHeadersCharged(t *testing.T) {
	for _, c := range streamPaths {
		t.Run(c.name, func(t *testing.T) {
			received := make(chan struct{}, 1)
			h := newE2E(t, headerlessUpstream(received))
			h.budget.SetTokenHoldTail(0)
			tokenKey, keyID := e2eTokenKey(t, h)
			addKeyBudget(t, h, keyID, 0, 1_000_000)

			cancelWhenReceived(t, h, received, c.path, c.body, tokenKey)

			row := lastUsageRow(t, h, keyID)
			require.Equal(t, "cancelled", row.Status)
			require.Equal(t, 4000, row.Completion)
			require.Positive(t, row.Prompt)
			require.Eventually(t, func() bool { return heldTokens(t, h, keyID) == 0 },
				3*time.Second, 20*time.Millisecond)
			require.Equal(t, int64(row.Prompt+row.Completion), recordedTokens(t, h, keyID))
		})
	}
}

func TestPAYGStreamCancelBeforeHeadersUnchanged(t *testing.T) {
	for _, c := range streamPaths {
		t.Run(c.name, func(t *testing.T) {
			received := make(chan struct{}, 1)
			h := newE2E(t, headerlessUpstream(received))
			_, _ = e2eTokenKey(t, h)
			keyID := paygKeyID(t, h)

			cancelWhenReceived(t, h, received, c.path, c.body, h.apiKey)

			row := lastUsageRow(t, h, keyID)
			require.Zero(t, row.Prompt)
			require.Zero(t, row.Completion)
		})
	}
}

// countingHeaderlessUpstream holds the response headers until the gateway
// gives up, counting calls.
func countingHeaderlessUpstream(calls *atomic.Int32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.ReadAll(r.Body)
		<-r.Context().Done()
	}
}

// A stream whose upstream holds its headers past the header timeout was sent
// and may be reasoning: a token key is charged the cut-stream amount, once,
// with no fallback.
func TestTokenStreamHeaderTimeoutCharged(t *testing.T) {
	for _, c := range streamPaths {
		t.Run(c.name, func(t *testing.T) {
			var calls atomic.Int32
			h := newE2E(t, countingHeaderlessUpstream(&calls), func(o *e2eOptions) { o.headerTimeout = 300 * time.Millisecond })
			h.budget.SetTokenHoldTail(0)
			tokenKey, keyID := e2eTokenKey(t, h)
			addKeyBudget(t, h, keyID, 0, 1_000_000)
			addFallbackStep(t, h)

			_ = readAllString(t, h.post(t, c.path, c.body, tokenKey))

			row := lastUsageRow(t, h, keyID)
			require.Equal(t, 4000, row.Completion, "charged the output cap (status %s)", row.Status)
			require.Positive(t, row.Prompt)
			require.EqualValues(t, 1, calls.Load(), "no free retry after a charged timeout")
			require.Eventually(t, func() bool { return heldTokens(t, h, keyID) == 0 },
				3*time.Second, 20*time.Millisecond)
			require.Equal(t, int64(row.Prompt+row.Completion), recordedTokens(t, h, keyID))
		})
	}
}

func TestPAYGStreamHeaderTimeoutUnchanged(t *testing.T) {
	var calls atomic.Int32
	h := newE2E(t, countingHeaderlessUpstream(&calls), func(o *e2eOptions) { o.headerTimeout = 300 * time.Millisecond })
	_, _ = e2eTokenKey(t, h)
	keyID := paygKeyID(t, h)
	addFallbackStep(t, h)

	_ = readAllString(t, h.post(t, streamPaths[1].path, streamPaths[1].body, h.apiKey))

	row := lastUsageRow(t, h, keyID)
	require.Zero(t, row.Prompt)
	require.Zero(t, row.Completion)
	require.EqualValues(t, 2, calls.Load(), "PAYG still falls back")
}

func TestTokenResponsesPreviousResponseIDRefused(t *testing.T) {
	var calls atomic.Int32
	h := newE2E(t, reasoningOnlyLengthSSE(&calls))
	tokenKey, _ := e2eTokenKey(t, h)

	resp := h.post(t, "/v1/responses", `{"model":"gpt-6-sol","input":"hi","previous_response_id":"resp_1"}`, tokenKey)
	body := readAllString(t, resp)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, body)
	require.Contains(t, body, "previous_response_id_not_supported_for_token_package")
	require.Zero(t, calls.Load())
}
