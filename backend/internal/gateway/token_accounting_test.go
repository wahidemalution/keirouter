package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/budget"
)

type usageRow struct {
	Status             string
	Prompt, Completion int
	Cached, Reasoning  int
	Source             string
}

// lastUsageRow waits for the key's usage row and returns it.
func lastUsageRow(t *testing.T, h *e2eHarness, keyID string) usageRow {
	t.Helper()
	var row usageRow
	require.Eventually(t, func() bool {
		err := h.gateway.db.SQL().QueryRowContext(context.Background(),
			`SELECT status, prompt_tokens, completion_tokens, cached_tokens, reasoning_tokens, usage_source
			 FROM usage_records WHERE api_key_id = ? ORDER BY created_at DESC LIMIT 1`, keyID).
			Scan(&row.Status, &row.Prompt, &row.Completion, &row.Cached, &row.Reasoning, &row.Source)
		return err == nil
	}, 5*time.Second, 20*time.Millisecond, "a usage row is recorded")
	return row
}

// recordedTokens is what the key's token budget counts as spent.
func recordedTokens(t *testing.T, h *e2eHarness, keyID string) int64 {
	t.Helper()
	dec, err := h.budget.Check(context.Background(), budget.Scope{APIKeyID: keyID})
	require.NoError(t, err)
	for _, bu := range dec.BudgetUsage {
		if bu.Budget.ID == "bud-"+keyID {
			return bu.TokenCount
		}
	}
	t.Fatalf("budget bud-%s not found", keyID)
	return 0
}

// cancelAfterFirstByte opens a stream, reads its first byte and hangs up.
func cancelAfterFirstByte(t *testing.T, h *e2eHarness, path, body, auth string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.server.URL+path, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+auth)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	buf := make([]byte, 1)
	_, err = resp.Body.Read(buf)
	require.NoError(t, err, "the stream started")
	cancel()
	_ = resp.Body.Close()
}

var streamPaths = []struct{ name, path, body string }{
	{"direct", "/v1/chat/completions", `{"model":"gpt-6-sol","stream":true,"max_tokens":4000,"messages":[{"role":"user","content":"hi"}]}`},
	{"translated", "/v1/messages", `{"model":"gpt-6-sol","stream":true,"max_tokens":4000,"messages":[{"role":"user","content":"hi"}]}`},
}

func TestTokenStreamCancelChargesOutputCap(t *testing.T) {
	for _, c := range streamPaths {
		t.Run(c.name, func(t *testing.T) {
			h := newE2E(t, hangingSSEUpstream)
			h.budget.SetTokenHoldTail(0)
			tokenKey, keyID := e2eTokenKey(t, h)
			addKeyBudget(t, h, keyID, 0, 1_000_000)

			cancelAfterFirstByte(t, h, c.path, c.body, tokenKey)

			row := lastUsageRow(t, h, keyID)
			require.Equal(t, "cancelled", row.Status)
			require.Equal(t, 4000, row.Completion, "a cancelled stream is charged the output cap sent upstream")
			require.Positive(t, row.Prompt, "the prompt estimate is charged")
			require.Eventually(t, func() bool { return heldTokens(t, h, keyID) == 0 },
				3*time.Second, 20*time.Millisecond, "the hold is settled")
			require.Equal(t, int64(row.Prompt+row.Completion), recordedTokens(t, h, keyID),
				"the budget counts exactly the recorded tokens")
		})
	}
}

// truncatedSSEUpstream sends one chunk and ends the body cleanly, with no
// finish event, usage or [DONE].
func truncatedSSEUpstream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprintf(w, "%s\n\n", `data: {"id":"s1","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"hi"}}]}`)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// abortedSSEUpstream sends one chunk and resets the connection.
func abortedSSEUpstream(w http.ResponseWriter, r *http.Request) {
	truncatedSSEUpstream(w, r)
	panic(http.ErrAbortHandler)
}

// erroringSSEUpstream sends one chunk, then an in-band error event.
func erroringSSEUpstream(w http.ResponseWriter, r *http.Request) {
	truncatedSSEUpstream(w, r)
	fmt.Fprintf(w, "%s\n\n", `data: {"error":{"message":"upstream overloaded","type":"server_error"}}`)
}

func TestTokenStreamUpstreamDisconnectCharged(t *testing.T) {
	upstreams := []struct {
		name string
		h    http.HandlerFunc
	}{
		{"clean_eof", truncatedSSEUpstream},
		{"connection_reset", abortedSSEUpstream},
		{"error_event", erroringSSEUpstream},
	}
	for _, up := range upstreams {
		for _, c := range streamPaths {
			t.Run(up.name+"/"+c.name, func(t *testing.T) {
				h := newE2E(t, up.h)
				tokenKey, keyID := e2eTokenKey(t, h)
				addKeyBudget(t, h, keyID, 0, 1_000_000)

				resp := h.post(t, c.path, c.body, tokenKey)
				_ = readAllString(t, resp)

				row := lastUsageRow(t, h, keyID)
				require.Equal(t, 4000, row.Completion, "a cut stream is charged the output cap (status %s)", row.Status)
				require.Positive(t, row.Prompt)
			})
		}
	}
}

func TestTokenStreamCompleteWithoutUsageEstimated(t *testing.T) {
	noUsage := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, l := range []string{
			`data: {"id":"s1","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"hello there"}}]}`,
			`data: {"id":"s1","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			`data: [DONE]`,
		} {
			fmt.Fprintf(w, "%s\n\n", l)
		}
	}
	for _, c := range streamPaths {
		t.Run(c.name, func(t *testing.T) {
			h := newE2E(t, noUsage)
			tokenKey, keyID := e2eTokenKey(t, h)
			addKeyBudget(t, h, keyID, 0, 1_000_000)

			resp := h.post(t, c.path, c.body, tokenKey)
			_ = readAllString(t, resp)

			row := lastUsageRow(t, h, keyID)
			require.Equal(t, "success", row.Status)
			require.Positive(t, row.Prompt)
			require.Positive(t, row.Completion)
			require.Less(t, row.Completion, 100, "a finished stream is estimated from its output, not the cap")
		})
	}
}

// unaryUpstream answers a non-streaming chat call with the given message JSON
// and usage JSON ("" for none).
func unaryUpstream(message, usage string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body := `{"id":"up1","model":"gpt-4o","choices":[{"message":` + message + `,"finish_reason":"stop"}]`
		if usage != "" {
			body += `,"usage":` + usage
		}
		fmt.Fprint(w, body+"}")
	}
}

func TestTokenUnaryNoUsageNeverZero(t *testing.T) {
	longReasoning := strings.Repeat("think ", 700) // 4200 chars
	cases := []struct {
		name          string
		message       string
		minCompletion int
	}{
		{"text", `{"role":"assistant","content":"hello from upstream"}`, 1},
		{"empty", `{"role":"assistant","content":""}`, 1},
		{"reasoning", `{"role":"assistant","content":"ok","reasoning_content":"` + longReasoning + `"}`, 1000},
		{"tool_call", `{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"` + strings.Repeat("x", 700) + `\"}"}}]}`, 150},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newE2E(t, unaryUpstream(c.message, ""))
			tokenKey, keyID := e2eTokenKey(t, h)
			addKeyBudget(t, h, keyID, 0, 1_000_000)

			resp := h.post(t, "/v1/chat/completions", `{"model":"gpt-6-sol","max_tokens":4000,"messages":[{"role":"user","content":"hi"}]}`, tokenKey)
			body := readAllString(t, resp)
			require.Equal(t, http.StatusOK, resp.StatusCode, body)

			row := lastUsageRow(t, h, keyID)
			require.Positive(t, row.Prompt, "the prompt estimate is charged")
			require.GreaterOrEqual(t, row.Completion, c.minCompletion)
			require.Equal(t, "estimated", row.Source)
		})
	}
}

func TestTokenReasoningTokensCountedE2E(t *testing.T) {
	const usage = `{"prompt_tokens":50,"completion_tokens":1200,"total_tokens":1250,"completion_tokens_details":{"reasoning_tokens":1000}}`
	stream := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, l := range []string{
			`data: {"id":"s1","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"hi"}}]}`,
			`data: {"id":"s1","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			`data: {"id":"s1","model":"gpt-4o","choices":[],"usage":` + usage + `}`,
			`data: [DONE]`,
		} {
			fmt.Fprintf(w, "%s\n\n", l)
		}
	}
	cases := []struct {
		name, path, body string
		up               http.HandlerFunc
	}{
		{"unary", "/v1/chat/completions", `{"model":"gpt-6-sol","max_tokens":4000,"messages":[{"role":"user","content":"hi"}]}`,
			unaryUpstream(`{"role":"assistant","content":"hi"}`, usage)},
		{"stream_direct", streamPaths[0].path, streamPaths[0].body, stream},
		{"stream_translated", streamPaths[1].path, streamPaths[1].body, stream},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newE2E(t, c.up)
			tokenKey, keyID := e2eTokenKey(t, h)
			addKeyBudget(t, h, keyID, 0, 1_000_000)

			resp := h.post(t, c.path, c.body, tokenKey)
			_ = readAllString(t, resp)

			row := lastUsageRow(t, h, keyID)
			require.Equal(t, 50, row.Prompt)
			require.Equal(t, 1200, row.Completion, "reasoning tokens are inside completion_tokens")
			require.Equal(t, 1000, row.Reasoning)
		})
	}
}

func TestTokenCachedTokensCountedE2E(t *testing.T) {
	h := newE2E(t, unaryUpstream(`{"role":"assistant","content":"hi"}`,
		`{"prompt_tokens":1000,"completion_tokens":10,"total_tokens":1010,"prompt_tokens_details":{"cached_tokens":800}}`))
	tokenKey, keyID := e2eTokenKey(t, h)
	addKeyBudget(t, h, keyID, 0, 1_000_000)

	resp := h.post(t, "/v1/chat/completions", `{"model":"gpt-6-sol","messages":[{"role":"user","content":"hi"}]}`, tokenKey)
	_ = readAllString(t, resp)

	row := lastUsageRow(t, h, keyID)
	require.Equal(t, 1000, row.Prompt, "cached prompt tokens stay inside prompt_tokens")
	require.Equal(t, 800, row.Cached)
	require.Equal(t, int64(1010), recordedTokens(t, h, keyID))
}

func TestPAYGRecordingUnchanged(t *testing.T) {
	for _, c := range streamPaths {
		t.Run("cancel/"+c.name, func(t *testing.T) {
			h := newE2E(t, hangingSSEUpstream)
			_, _ = e2eTokenKey(t, h) // the gpt-6-sol chain
			keyID := paygKeyID(t, h)

			cancelAfterFirstByte(t, h, c.path, c.body, h.apiKey)

			row := lastUsageRow(t, h, keyID)
			require.Equal(t, "cancelled", row.Status)
			require.Equal(t, 1, row.Completion, "PAYG keeps the visible-output estimate")
			require.Equal(t, "estimated", row.Source)
		})
		for _, up := range []struct {
			name, status string
			h            http.HandlerFunc
		}{
			{"clean_eof", "success", truncatedSSEUpstream},
			{"connection_reset", "failed", abortedSSEUpstream},
		} {
			t.Run(up.name+"/"+c.name, func(t *testing.T) {
				h := newE2E(t, up.h)
				_, _ = e2eTokenKey(t, h)
				keyID := paygKeyID(t, h)

				resp := h.post(t, c.path, c.body, h.apiKey)
				_ = readAllString(t, resp)

				row := lastUsageRow(t, h, keyID)
				require.Equal(t, up.status, row.Status)
				require.Equal(t, 1, row.Completion)
			})
		}
	}
	t.Run("unary_no_usage", func(t *testing.T) {
		h := newE2E(t, unaryUpstream(`{"role":"assistant","content":"hello from upstream"}`, ""))
		_, _ = e2eTokenKey(t, h)
		keyID := paygKeyID(t, h)

		resp := h.post(t, "/v1/chat/completions", `{"model":"gpt-6-sol","messages":[{"role":"user","content":"hi"}]}`, h.apiKey)
		body := readAllString(t, resp)
		require.Equal(t, http.StatusOK, resp.StatusCode, body)

		row := lastUsageRow(t, h, keyID)
		require.Zero(t, row.Prompt)
		require.Zero(t, row.Completion)
		require.Equal(t, "none", row.Source)
	})
}

// streamOptionsUpstream records whether a stream request asked for usage.
type streamOptionsUpstream struct {
	mu   sync.Mutex
	seen []bool
}

func (s *streamOptionsUpstream) handler(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body struct {
		StreamOptions *struct {
			IncludeUsage bool `json:"include_usage"`
		} `json:"stream_options"`
	}
	_ = json.Unmarshal(raw, &body)
	s.mu.Lock()
	s.seen = append(s.seen, body.StreamOptions != nil && body.StreamOptions.IncludeUsage)
	s.mu.Unlock()
	sseUpstream(w, r)
}

func TestTokenStreamRequestsUpstreamUsage(t *testing.T) {
	for _, c := range streamPaths {
		t.Run(c.name, func(t *testing.T) {
			up := &streamOptionsUpstream{}
			// A third-party OpenAI-compatible upstream, which gets no
			// stream_options unless asked (first-party OpenAI always does).
			h := newE2E(t, up.handler, func(o *e2eOptions) { o.provider = "groq" })
			tokenKey, _ := e2eTokenKey(t, h)
			ctx := context.Background()
			chain, err := h.gateway.chains.Get(ctx, "chain-sol")
			require.NoError(t, err)
			chain.Steps[0].Provider = "groq"
			require.NoError(t, h.gateway.chains.Update(ctx, chain))

			resp := h.post(t, c.path, c.body, tokenKey)
			body := readAllString(t, resp)
			require.Equal(t, http.StatusOK, resp.StatusCode, body)
			resp = h.post(t, c.path, c.body, h.apiKey)
			body = readAllString(t, resp)
			require.Equal(t, http.StatusOK, resp.StatusCode, body)

			up.mu.Lock()
			defer up.mu.Unlock()
			require.Equal(t, []bool{true, false}, up.seen,
				"a token key asks the upstream for its usage event; PAYG does not")
		})
	}
}

// zeroByteSSEUpstream opens the stream and ends it without a byte.
func zeroByteSSEUpstream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// errorOnlySSEUpstream sends only an in-band error event.
func errorOnlySSEUpstream(w http.ResponseWriter, r *http.Request) {
	zeroByteSSEUpstream(w, r)
	fmt.Fprintf(w, "%s\n\n", `data: {"error":{"message":"upstream overloaded","type":"server_error"}}`)
}

// resetBeforeOutputSSEUpstream opens the stream and resets the connection
// before the first token.
func resetBeforeOutputSSEUpstream(w http.ResponseWriter, r *http.Request) {
	zeroByteSSEUpstream(w, r)
	panic(http.ErrAbortHandler)
}

// An upstream that fails before any output costs the token key nothing, on
// the direct path as on the translated one (where admission refuses it).
func TestTokenStreamFailureBeforeOutputNotCharged(t *testing.T) {
	upstreams := []struct {
		name string
		h    http.HandlerFunc
	}{
		{"clean_eof", zeroByteSSEUpstream},
		{"error_event", errorOnlySSEUpstream},
		{"reset", resetBeforeOutputSSEUpstream},
	}
	for _, up := range upstreams {
		for _, c := range streamPaths {
			t.Run(up.name+"/"+c.name, func(t *testing.T) {
				h := newE2E(t, up.h)
				h.budget.SetTokenHoldTail(0)
				tokenKey, keyID := e2eTokenKey(t, h)
				addKeyBudget(t, h, keyID, 0, 1_000_000)

				_ = readAllString(t, h.post(t, c.path, c.body, tokenKey))

				row := lastUsageRow(t, h, keyID)
				require.Equal(t, "failed", row.Status)
				require.Zero(t, row.Prompt)
				require.Zero(t, row.Completion)
				require.Eventually(t, func() bool { return heldTokens(t, h, keyID) == 0 },
					3*time.Second, 20*time.Millisecond)
			})
		}
	}
}

// TestTokenStreamCancelDuringAdmissionCharged: the client leaves while the
// upstream has accepted the stream but sent no output yet (hidden reasoning).
func TestTokenStreamCancelDuringAdmissionCharged(t *testing.T) {
	accepted := make(chan struct{}, 1)
	upstream := func(w http.ResponseWriter, r *http.Request) {
		zeroByteSSEUpstream(w, r)
		accepted <- struct{}{}
		<-r.Context().Done()
	}
	h := newE2E(t, upstream)
	h.budget.SetTokenHoldTail(0)
	tokenKey, keyID := e2eTokenKey(t, h)
	addKeyBudget(t, h, keyID, 0, 1_000_000)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.server.URL+streamPaths[1].path, strings.NewReader(streamPaths[1].body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tokenKey)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()
	select {
	case <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream never received the request")
	}
	cancel()
	<-done

	row := lastUsageRow(t, h, keyID)
	require.Equal(t, "cancelled", row.Status)
	require.Equal(t, 4000, row.Completion, "an accepted stream is charged the cap")
	require.Positive(t, row.Prompt)
	require.Eventually(t, func() bool { return heldTokens(t, h, keyID) == 0 },
		3*time.Second, 20*time.Millisecond)
}

// TestPAYGStreamCancelDuringAdmissionUnchanged: PAYG still records nothing.
func TestPAYGStreamCancelDuringAdmissionUnchanged(t *testing.T) {
	accepted := make(chan struct{}, 1)
	upstream := func(w http.ResponseWriter, r *http.Request) {
		zeroByteSSEUpstream(w, r)
		accepted <- struct{}{}
		<-r.Context().Done()
	}
	h := newE2E(t, upstream)
	_, _ = e2eTokenKey(t, h)
	keyID := paygKeyID(t, h)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.server.URL+streamPaths[1].path, strings.NewReader(streamPaths[1].body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+h.apiKey)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()
	<-accepted
	cancel()
	<-done

	row := lastUsageRow(t, h, keyID)
	require.Equal(t, "cancelled", row.Status)
	require.Zero(t, row.Prompt)
	require.Zero(t, row.Completion)
}

// longHangingSSEUpstream streams 700 characters, then holds the stream open.
func longHangingSSEUpstream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprintf(w, "data: {\"id\":\"s1\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":%q}}]}\n\n", strings.Repeat("x", 700))
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	<-r.Context().Done()
}

// TestTokenStreamCancelVisibleOutputAboveCap: an upstream that streamed more
// than the cap is charged what it visibly streamed.
func TestTokenStreamCancelVisibleOutputAboveCap(t *testing.T) {
	for _, c := range streamPaths {
		t.Run(c.name, func(t *testing.T) {
			h := newE2E(t, longHangingSSEUpstream)
			tokenKey, keyID := e2eTokenKey(t, h)
			addKeyBudget(t, h, keyID, 0, 1_000_000)

			body := strings.Replace(c.body, `"max_tokens":4000`, `"max_tokens":5`, 1)
			cancelAfterFirstByte(t, h, c.path, body, tokenKey)

			row := lastUsageRow(t, h, keyID)
			require.Equal(t, "cancelled", row.Status)
			require.Equal(t, 200, row.Completion, "700 visible chars at 3.5 per token, above the cap of 5")
		})
	}
}
