package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/budget"
	"github.com/mydisha/keirouter/backend/internal/limits"
	"github.com/mydisha/keirouter/backend/internal/store"
	"github.com/mydisha/keirouter/backend/internal/tokenpkg"
)

// capturingUpstream answers like openAIUpstream and keeps the last request
// body it received.
type capturingUpstream struct {
	mu    sync.Mutex
	last  map[string]json.RawMessage
	calls atomic.Int32
}

func (c *capturingUpstream) handler() http.HandlerFunc {
	ok := openAIUpstream()
	return func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]json.RawMessage
		_ = json.Unmarshal(raw, &body)
		c.mu.Lock()
		c.last = body
		c.mu.Unlock()
		c.calls.Add(1)
		ok(w, r)
	}
}

// maxTokens returns the output cap the upstream received, -1 when absent.
func (c *capturingUpstream) maxTokens(t *testing.T) int {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	require.NotNil(t, c.last, "upstream was not called")
	raw, ok := c.last["max_tokens"]
	if !ok {
		raw, ok = c.last["max_completion_tokens"]
	}
	if !ok {
		return -1
	}
	var n int
	require.NoError(t, json.Unmarshal(raw, &n))
	return n
}

func addKeyBudget(t *testing.T, h *e2eHarness, keyID string, limitMicros, limitTokens int64) {
	t.Helper()
	now := time.Now()
	require.NoError(t, h.gateway.db.Budgets().Create(context.Background(), store.Budget{
		ID: "bud-" + keyID, TenantID: store.DefaultTenantID, ScopeKind: store.ScopeAPIKey, ScopeID: keyID,
		LimitMicros: limitMicros, LimitTokens: limitTokens, Period: "total", HardCutoff: true,
		CreatedAt: now, UpdatedAt: now,
	}))
}

func paygKeyID(t *testing.T, h *e2eHarness) string {
	t.Helper()
	key, err := h.gateway.identity.Authenticate(context.Background(), h.apiKey)
	require.NoError(t, err)
	return key.ID
}

var tokenFamilies = []struct {
	name, path, withMax, noMax string
}{
	{"chat", "/v1/chat/completions",
		`{"model":"gpt-6-sol","max_tokens":100000,"messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"gpt-6-sol","messages":[{"role":"user","content":"hi"}]}`},
	{"chat_max_completion_tokens", "/v1/chat/completions",
		`{"model":"gpt-6-sol","max_completion_tokens":100000,"messages":[{"role":"user","content":"hi"}]}`,
		""},
	{"messages", "/v1/messages",
		`{"model":"gpt-6-sol","max_tokens":100000,"messages":[{"role":"user","content":"hi"}]}`,
		""},
	{"responses", "/v1/responses",
		`{"model":"gpt-6-sol","input":"hi","max_output_tokens":100000}`,
		`{"model":"gpt-6-sol","input":"hi"}`},
	{"gemini", "/v1beta/models/gpt-6-sol:generateContent",
		`{"contents":[{"parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":100000}}`,
		`{"contents":[{"parts":[{"text":"hi"}]}]}`},
}

func TestTokenOutputCapClamped(t *testing.T) {
	for _, f := range tokenFamilies {
		t.Run(f.name+"/clamped_to_remaining", func(t *testing.T) {
			up := &capturingUpstream{}
			h := newE2E(t, up.handler())
			tokenKey, keyID := e2eTokenKey(t, h)
			addKeyBudget(t, h, keyID, 0, 5_000)

			resp := h.post(t, f.path, f.withMax, tokenKey)
			body := readAllString(t, resp)
			require.Equal(t, http.StatusOK, resp.StatusCode, body)
			got := up.maxTokens(t)
			require.Less(t, got, 5_000, "output must leave room for the prompt")
			require.Greater(t, got, 4_900, "only the prompt estimate is taken off")
		})
		if f.noMax == "" {
			continue
		}
		t.Run(f.name+"/absent_gets_ceiling", func(t *testing.T) {
			up := &capturingUpstream{}
			h := newE2E(t, up.handler())
			tokenKey, keyID := e2eTokenKey(t, h)
			addKeyBudget(t, h, keyID, 0, 20_000_000)

			resp := h.post(t, f.path, f.noMax, tokenKey)
			body := readAllString(t, resp)
			require.Equal(t, http.StatusOK, resp.StatusCode, body)
			require.Equal(t, 32768, up.maxTokens(t))
		})
	}
}

func TestTokenOutputCapRefusesMultipleChoices(t *testing.T) {
	up := &capturingUpstream{}
	h := newE2E(t, up.handler())
	tokenKey, _ := e2eTokenKey(t, h)

	resp := h.post(t, "/v1/chat/completions", `{"model":"gpt-6-sol","n":4,"max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`, tokenKey)
	body := readAllString(t, resp)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, body)
	require.Contains(t, body, "n_not_supported_for_token_package")
	require.Zero(t, up.calls.Load(), "a refused request never reaches the upstream")

	resp = h.post(t, "/v1/chat/completions", `{"model":"gpt-6-sol","n":1,"prediction":{"type":"content","content":"x"},"messages":[{"role":"user","content":"hi"}]}`, tokenKey)
	body = readAllString(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	up.mu.Lock()
	_, hasPrediction := up.last["prediction"]
	up.mu.Unlock()
	require.False(t, hasPrediction, "output-buying passthrough params are dropped")
}

func TestPAYGMultipleChoicesUnaffected(t *testing.T) {
	up := &capturingUpstream{}
	h := newE2E(t, up.handler())
	resp := h.post(t, "/v1/chat/completions", `{"model":"openai/gpt-4o","n":4,"messages":[{"role":"user","content":"hi"}]}`, h.apiKey)
	body := readAllString(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	up.mu.Lock()
	defer up.mu.Unlock()
	require.JSONEq(t, "4", string(up.last["n"]))
}

func TestTokenKeyWithoutTokenBudgetRefused(t *testing.T) {
	up := &capturingUpstream{}
	h := newE2E(t, up.handler())
	tokenKey, keyID := e2eTokenKey(t, h)
	resp := h.post(t, "/v1/chat/completions", tokenChatBody, tokenKey)
	body := readAllString(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)

	require.NoError(t, h.gateway.db.Budgets().Delete(context.Background(), e2eTokenBudgetID(keyID)))
	h.budget.InvalidateBudgetCache()
	calls := up.calls.Load()
	resp = h.post(t, "/v1/chat/completions", tokenChatBody, tokenKey)
	body = readAllString(t, resp)
	require.Equal(t, http.StatusPaymentRequired, resp.StatusCode, body)
	require.Contains(t, body, "insufficient_tokens")
	require.Equal(t, calls, up.calls.Load())
}

func TestTokenGeminiCountTokensAnsweredLocally(t *testing.T) {
	up := &capturingUpstream{}
	h := newE2E(t, up.handler())
	tokenKey, keyID := e2eTokenKey(t, h)
	resp := h.post(t, "/v1beta/models/gpt-6-sol:countTokens", `{"contents":[{"parts":[{"text":"hello there"}]}]}`, tokenKey)
	body := readAllString(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var out struct {
		TotalTokens int `json:"totalTokens"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &out))
	require.Positive(t, out.TotalTokens)
	require.Zero(t, up.calls.Load(), "no upstream call")
	_, tokens, err := h.gateway.db.Usage().SpendAndTokens(context.Background(), store.ScopeAPIKey, keyID, time.Time{})
	require.NoError(t, err)
	require.Zero(t, tokens, "nothing metered")
}

func TestTokenKeyInsufficientTokensResponse(t *testing.T) {
	for _, f := range tokenFamilies {
		t.Run(f.name, func(t *testing.T) {
			up := &capturingUpstream{}
			h := newE2E(t, up.handler())
			tokenKey, keyID := e2eTokenKey(t, h)
			addKeyBudget(t, h, keyID, 0, 3)

			resp := h.post(t, f.path, f.withMax, tokenKey)
			body := readAllString(t, resp)
			require.Equal(t, http.StatusPaymentRequired, resp.StatusCode, body)
			require.Contains(t, body, "insufficient_tokens")
			require.Contains(t, body, "tersisa 3 token")
			require.Zero(t, up.calls.Load(), "a refused request never reaches the upstream")
		})
	}
}

func TestPAYGNoTokenReservationOrClamp(t *testing.T) {
	up := &capturingUpstream{}
	h := newE2E(t, up.handler())
	_, _ = e2eTokenKey(t, h)
	addKeyBudget(t, h, paygKeyID(t, h), 10_000_000, 1_000_000)

	resp := h.post(t, "/v1/chat/completions", `{"model":"openai/gpt-4o","messages":[{"role":"user","content":"hi"}]}`, h.apiKey)
	body := readAllString(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	require.Equal(t, -1, up.maxTokens(t), "PAYG output cap is not inserted")

	resp = h.post(t, "/v1/chat/completions", `{"model":"openai/gpt-4o","max_tokens":5000000,"messages":[{"role":"user","content":"hi"}]}`, h.apiKey)
	body = readAllString(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	require.Equal(t, 5_000_000, up.maxTokens(t), "PAYG output cap is not clamped to its token limit")
}

// panicWriter panics on the first body write after the headers went out.
type panicWriter struct {
	*httptest.ResponseRecorder
	panicked atomic.Bool
}

func (p *panicWriter) Write(b []byte) (int, error) {
	if strings.Contains(string(b), "data:") && p.panicked.CompareAndSwap(false, true) {
		panic("relay write failed")
	}
	return p.ResponseRecorder.Write(b)
}

func sseUpstream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	flush, _ := w.(http.Flusher)
	for _, l := range []string{
		`data: {"id":"s1","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"hi"}}]}`,
		`data: {"id":"s1","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}}`,
		`data: [DONE]`,
	} {
		fmt.Fprintf(w, "%s\n\n", l)
		if flush != nil {
			flush.Flush()
		}
	}
}

func TestDirectStreamPanicReleasesConcurrencySlot(t *testing.T) {
	lim := limits.NewMemory(limits.MemoryConfig{Enabled: true, Window: time.Minute})
	h := newE2E(t, sseUpstream, func(o *e2eOptions) { o.limiter = lim })
	tokenKey, _ := e2eTokenKey(t, h)
	ctx := context.Background()
	plan, err := h.gateway.db.Plans().Get(ctx, tokenpkg.DefaultPlanID)
	require.NoError(t, err)
	plan.ConcurrencyLimit = 1
	plan.RPMLimit = 100
	require.NoError(t, h.gateway.db.Plans().Update(ctx, plan))

	body := `{"model":"gpt-6-sol","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tokenKey)
	pw := &panicWriter{ResponseRecorder: httptest.NewRecorder()}
	h.handler.ServeHTTP(pw, req)
	require.True(t, pw.panicked.Load(), "the relay must have panicked")

	resp := h.post(t, "/v1/chat/completions", body, tokenKey)
	out := readAllString(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode, "the panicked stream released its slot: "+out)
}

func TestTokenGeminiActionsPinned(t *testing.T) {
	for _, action := range []string{"generateContent", "streamGenerateContent", "countTokens"} {
		require.True(t, tokenEndpointAllowed(http.MethodPost, "/v1beta/models/gpt-6-sol:"+action), action)
	}
	for _, path := range []string{
		"/v1beta/models/gpt-6-sol:embedContent",
		"/v1beta/models/gpt-6-sol:batchEmbedContents",
		"/v1beta/models/gpt-6-sol:predict",
		"/v1beta/models/gpt-6-sol:streamAnything",
		"/v1beta/models/gpt-6-sol",
		"/v1beta/models/:generateContent",
	} {
		require.False(t, tokenEndpointAllowed(http.MethodPost, path), path)
	}
}

func TestTokenKeyModelsListsOnlyWhitelistedChains(t *testing.T) {
	h := newE2E(t, openAIUpstream())
	tokenKey, _ := e2eTokenKey(t, h)
	now := time.Now()
	require.NoError(t, h.gateway.chains.Create(context.Background(), store.Chain{
		ID: "chain-other", TenantID: store.DefaultTenantID, Name: "other-chain", Strategy: "fallback",
		Steps:     []store.ChainStep{{ID: "step-other", Position: 0, Provider: "openai", Model: "gpt-4o"}},
		CreatedAt: now, UpdatedAt: now,
	}))

	ids := func(auth string) []string {
		resp := h.get(t, "/v1/models", auth)
		body := readAllString(t, resp)
		require.Equal(t, http.StatusOK, resp.StatusCode, body)
		var out struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal([]byte(body), &out))
		var got []string
		for _, m := range out.Data {
			got = append(got, m.ID)
		}
		return got
	}
	require.Equal(t, []string{"gpt-6-sol"}, ids(tokenKey))
	require.ElementsMatch(t, []string{"gpt-6-sol", "other-chain"}, ids(h.apiKey))
}

func heldTokens(t *testing.T, h *e2eHarness, keyID string) int64 {
	t.Helper()
	dec, err := h.budget.Check(context.Background(), budget.Scope{APIKeyID: keyID})
	require.NoError(t, err)
	for _, bu := range dec.BudgetUsage {
		if bu.Budget.ID == "bud-"+keyID {
			return bu.ReservedTokens
		}
	}
	t.Fatalf("budget bud-%s not found", keyID)
	return 0
}

func TestTokenHoldReleasedAfterRequest(t *testing.T) {
	failing := func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"bad request"}}`, http.StatusBadRequest)
	}
	cases := []struct {
		name     string
		upstream http.HandlerFunc
		body     string
		path     string
	}{
		{"stream_translated", sseUpstream, `{"model":"gpt-6-sol","stream":true,"max_tokens":4000,"messages":[{"role":"user","content":"hi"}]}`, "/v1/messages"},
		{"unary", openAIUpstream(), `{"model":"gpt-6-sol","max_tokens":4000,"messages":[{"role":"user","content":"hi"}]}`, ""},
		{"stream", sseUpstream, `{"model":"gpt-6-sol","stream":true,"max_tokens":4000,"messages":[{"role":"user","content":"hi"}]}`, ""},
		{"stream_upstream_error", failing, `{"model":"gpt-6-sol","stream":true,"max_tokens":4000,"messages":[{"role":"user","content":"hi"}]}`, ""},
		{"upstream_error", failing, `{"model":"gpt-6-sol","max_tokens":4000,"messages":[{"role":"user","content":"hi"}]}`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newE2E(t, c.upstream)
			h.budget.SetTokenHoldTail(200 * time.Millisecond)
			tokenKey, keyID := e2eTokenKey(t, h)
			addKeyBudget(t, h, keyID, 0, 1_000_000)

			path := c.path
			if path == "" {
				path = "/v1/chat/completions"
			}
			resp := h.post(t, path, c.body, tokenKey)
			_ = readAllString(t, resp)
			require.Eventually(t, func() bool { return heldTokens(t, h, keyID) == 0 },
				3*time.Second, 20*time.Millisecond, "the request's token hold is released")
		})
	}
}

// hangingSSEUpstream sends one chunk, then holds the stream open until the
// client goes away.
func hangingSSEUpstream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprintf(w, "%s\n\n", `data: {"id":"s1","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"hi"}}]}`)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	<-r.Context().Done()
}

func TestTokenHoldReleasedOnClientCancel(t *testing.T) {
	for _, c := range []struct{ name, path, body string }{
		{"direct", "/v1/chat/completions", `{"model":"gpt-6-sol","stream":true,"max_tokens":4000,"messages":[{"role":"user","content":"hi"}]}`},
		{"translated", "/v1/messages", `{"model":"gpt-6-sol","stream":true,"max_tokens":4000,"messages":[{"role":"user","content":"hi"}]}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newE2E(t, hangingSSEUpstream)
			h.budget.SetTokenHoldTail(200 * time.Millisecond)
			tokenKey, keyID := e2eTokenKey(t, h)
			addKeyBudget(t, h, keyID, 0, 1_000_000)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.server.URL+c.path, strings.NewReader(c.body))
			require.NoError(t, err)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+tokenKey)
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			buf := make([]byte, 1)
			_, err = resp.Body.Read(buf)
			require.NoError(t, err, "the stream started")
			require.Positive(t, heldTokens(t, h, keyID), "the stream holds tokens while it runs")
			cancel()
			_ = resp.Body.Close()

			require.Eventually(t, func() bool { return heldTokens(t, h, keyID) == 0 },
				5*time.Second, 20*time.Millisecond, "the cancelled stream's hold is released")
		})
	}
}
