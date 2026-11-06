package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/budget"
	"github.com/mydisha/keirouter/backend/internal/meter"
	"github.com/mydisha/keirouter/backend/internal/store"
)

// gpt-4o at $10/M input and $30/M output. Every upstream reply below reports
// 1,000 prompt + 100 completion tokens: $0.010 + $0.003 = $0.013.
const replyCostMicros = 13_000

var pricedGPT4o = func(o *e2eOptions) {
	o.modelPrices = map[string]meter.Price{"openai/gpt-4o": {InputPerM: 10, OutputPerM: 30}}
}

// billedUpstream answers chat completions, unary or streamed, and counts the
// calls that reached it. delay holds each reply to keep requests in flight.
func billedUpstream(calls *atomic.Int64, delay time.Duration, status int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		time.Sleep(delay)
		if status != http.StatusOK {
			http.Error(w, `{"error":{"message":"upstream exploded"}}`, status)
			return
		}
		if strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			flush, _ := w.(http.Flusher)
			for _, l := range []string{
				`data: {"choices":[{"delta":{"role":"assistant","content":"ok"}}]}`,
				`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`,
				`data: {"choices":[],"usage":{"prompt_tokens":1000,"completion_tokens":100,"total_tokens":1100}}`,
				`data: [DONE]`,
			} {
				fmt.Fprintf(w, "%s\n\n", l)
				if flush != nil {
					flush.Flush()
				}
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"up1","model":"gpt-4o","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1000,"completion_tokens":100,"total_tokens":1100}}`)
	}
}

// fundKey gives the harness key a hard-cutoff balance and returns its scope.
func fundKey(t *testing.T, h *e2eHarness, limitMicros int64) budget.Scope {
	t.Helper()
	ctx := context.Background()
	key, err := h.gateway.identity.Authenticate(ctx, h.apiKey)
	require.NoError(t, err)
	require.NoError(t, h.db.Budgets().Create(ctx, store.Budget{
		ID: "bal-" + key.ID, TenantID: key.TenantID, ScopeKind: store.ScopeAPIKey, ScopeID: key.ID,
		LimitMicros: limitMicros, Period: "total", HardCutoff: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}))
	return budget.Scope{TenantID: key.TenantID, APIKeyID: key.ID}
}

// balance reports recorded spend and the amount still reserved for the key.
func balance(t *testing.T, h *e2eHarness, scope budget.Scope) (spent, reserved int64) {
	t.Helper()
	dec, err := h.budget.Check(context.Background(), scope)
	require.NoError(t, err)
	require.Len(t, dec.BudgetUsage, 1)
	bu := dec.BudgetUsage[0]
	return bu.SpentMicros, bu.CostMicros - bu.SpentMicros
}

func chatBody(content string, stream bool) string {
	return fmt.Sprintf(`{"model":"openai/gpt-4o","stream":%t,"messages":[{"role":"user","content":%q}]}`, stream, content)
}

func drain(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(b)
}

// A prompt whose input alone costs more than the remaining balance is refused
// before it reaches the upstream (production key b163513a: $0.62 left, a $1.82
// prompt went through).
func TestE2E_Budget_RefusesPromptLargerThanBalance(t *testing.T) {
	var calls atomic.Int64
	h := newE2E(t, billedUpstream(&calls, 0, http.StatusOK), pricedGPT4o)
	scope := fundKey(t, h, 100_000) // $0.10

	// ~20k words ≈ 25k tokens with margin ≈ $0.25 of input at $10/M.
	big := strings.Repeat("lorem ", 20_000)
	for _, stream := range []bool{false, true} {
		resp := h.post(t, "/v1/chat/completions", chatBody(big, stream), h.apiKey)
		body := drain(t, resp)
		require.NotEqual(t, http.StatusOK, resp.StatusCode, "stream=%v: %s", stream, body)
		require.Contains(t, body, "budget")
	}
	require.Zero(t, calls.Load(), "a refused request must never reach the upstream")
	_, reserved := balance(t, h, scope)
	require.Zero(t, reserved, "a refused request leaves no reservation")

	// A small prompt on the same balance still goes through.
	resp := h.post(t, "/v1/chat/completions", chatBody("hi", false), h.apiKey)
	require.Equal(t, http.StatusOK, resp.StatusCode, drain(t, resp))
	require.Equal(t, int64(1), calls.Load())
}

// Once less than $0.05 remains the key is refused, even though its limit is
// far above $0.05 (production key 9a565488).
func TestE2E_Budget_MinimumBalanceIsRemainingBalance(t *testing.T) {
	var calls atomic.Int64
	h := newE2E(t, billedUpstream(&calls, 0, http.StatusOK), pricedGPT4o)
	scope := fundKey(t, h, 60_000) // $0.06: one $0.013 reply leaves $0.047

	// max_tokens keeps the output allowance ($0.003) inside the $0.06.
	short := `{"model":"openai/gpt-4o","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`
	resp := h.post(t, "/v1/chat/completions", short, h.apiKey)
	require.Equal(t, http.StatusOK, resp.StatusCode, drain(t, resp))

	resp = h.post(t, "/v1/chat/completions", short, h.apiKey)
	body := drain(t, resp)
	require.Equal(t, http.StatusPaymentRequired, resp.StatusCode, body)
	require.Equal(t, int64(1), calls.Load())

	spent, _ := balance(t, h, scope)
	require.Equal(t, int64(replyCostMicros), spent)
}

// After every request, unary or streamed, successful or failed, no
// reservation stays behind and the cost is counted once, from the usage
// table. Under the old code a forgotten release was masked by other requests
// wiping all reservations.
func TestE2E_Budget_ReservationsSettleAfterEachRequest(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		stream     bool
		wantCost   int64
		wantStatus int
	}{
		{"unary success", http.StatusOK, false, replyCostMicros, http.StatusOK},
		{"stream success", http.StatusOK, true, replyCostMicros, http.StatusOK},
		{"unary upstream failure", http.StatusInternalServerError, false, 0, 0},
		{"stream upstream failure", http.StatusInternalServerError, true, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int64
			h := newE2E(t, billedUpstream(&calls, 0, tc.status), pricedGPT4o)
			scope := fundKey(t, h, 1_000_000)

			for range 3 {
				resp := h.post(t, "/v1/chat/completions", chatBody("hi", tc.stream), h.apiKey)
				body := drain(t, resp)
				if tc.wantStatus != 0 {
					require.Equal(t, tc.wantStatus, resp.StatusCode, body)
				}
			}
			require.Positive(t, calls.Load())

			// Streams settle in a goroutine after the body is drained.
			require.Eventually(t, func() bool {
				spent, reserved := balance(t, h, scope)
				// The harness meter writes synchronously, so the cost is in
				// the usage table and nothing is reserved or pending.
				return spent == 3*tc.wantCost && reserved == 0
			}, 3*time.Second, 20*time.Millisecond)
		})
	}
}

// A burst of concurrent requests from one key, like the 63-in-a-second bursts
// seen in production, cannot spend past the balance.
func TestE2E_Budget_ConcurrentBurstDoesNotOverspend(t *testing.T) {
	var calls atomic.Int64
	h := newE2E(t, billedUpstream(&calls, 300*time.Millisecond, http.StatusOK), pricedGPT4o)
	const limit = 200_000 // $0.20
	scope := fundKey(t, h, limit)

	// Each request reserves its prompt plus 100 output tokens (max_tokens),
	// which covers the $0.013 the upstream bills.
	body := `{"model":"openai/gpt-4o","max_tokens":100,"messages":[{"role":"user","content":"` +
		strings.Repeat("lorem ", 790) + `"}]}`
	const burst = 40
	var wg sync.WaitGroup
	var ok atomic.Int64
	start := make(chan struct{})
	for range burst {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			resp := h.post(t, "/v1/chat/completions", body, h.apiKey)
			drain(t, resp)
			if resp.StatusCode == http.StatusOK {
				ok.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	spent, _ := balance(t, h, scope)
	require.Positive(t, ok.Load())
	require.Less(t, ok.Load(), int64(burst), "some of the burst must be refused")
	require.Equal(t, ok.Load(), calls.Load(), "only admitted requests reach the upstream")
	require.LessOrEqual(t, spent, int64(limit), "the burst overspent the balance")
}

// The pre-dispatch estimate is stored on the usage row, next to the billed
// usage, so production data can show how far estimates run from bills.
func TestE2E_Budget_EstimateStoredOnUsageRow(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			var calls atomic.Int64
			h := newE2E(t, billedUpstream(&calls, 0, http.StatusOK), pricedGPT4o)
			scope := fundKey(t, h, 1_000_000)

			body := `{"model":"openai/gpt-4o","max_tokens":100,"stream":` + fmt.Sprint(stream) +
				`,"messages":[{"role":"user","content":"` + strings.Repeat("hello world ", 200) + `"}]}`
			resp := h.post(t, "/v1/chat/completions", body, h.apiKey)
			require.Equal(t, http.StatusOK, resp.StatusCode, drain(t, resp))

			var estPrompt, estCost, prompt, cost int64
			require.Eventually(t, func() bool {
				err := h.db.SQL().QueryRowContext(context.Background(),
					`SELECT est_prompt_tokens, est_cost_micros, prompt_tokens, cost_micros FROM usage_records WHERE api_key_id = ?`,
					scope.APIKeyID).Scan(&estPrompt, &estCost, &prompt, &cost)
				return err == nil
			}, 3*time.Second, 20*time.Millisecond)

			// "hello world " x200 is about 400 tokens; +25% margin.
			require.Greater(t, estPrompt, int64(400))
			require.Less(t, estPrompt, int64(700))
			// The estimate prices that prompt plus the 100-token max_tokens.
			wantCost := estPrompt*10 + 100*30 // micros at $10/M in, $30/M out
			require.InDelta(t, float64(wantCost), float64(estCost), 2)
			require.Equal(t, int64(1000), prompt)
			require.Equal(t, int64(replyCostMicros), cost)
		})
	}
}
