package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/core"
	"github.com/mydisha/keirouter/backend/internal/meter"
)

// pricedE2E prices the gpt-6-sol chain's upstream model so requests cost
// something measurable.
func pricedE2E(o *e2eOptions) {
	o.modelPrices = map[string]meter.Price{"openai/gpt-4o": {InputPerM: 10, OutputPerM: 30}}
}

// lastCostNanos waits for the key's latest usage row and returns what it was
// billed.
func lastCostNanos(t *testing.T, h *e2eHarness, keyID string) int64 {
	t.Helper()
	var cost int64
	require.Eventually(t, func() bool {
		err := h.gateway.db.SQL().QueryRowContext(context.Background(),
			`SELECT cost_nanos FROM usage_records WHERE api_key_id = ? ORDER BY created_at DESC LIMIT 1`, keyID).
			Scan(&cost)
		return err == nil
	}, 5*time.Second, 20*time.Millisecond, "a usage row is recorded")
	return cost
}

func usageObject(t *testing.T, body string) map[string]any {
	t.Helper()
	var out map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &out), body)
	usage, ok := out["usage"].(map[string]any)
	require.True(t, ok, body)
	return usage
}

const pricedUsage = `{"prompt_tokens":1200,"completion_tokens":85,"total_tokens":1285}`

func TestUnaryCost_PAYG(t *testing.T) {
	h := newE2E(t, unaryUpstream(`{"role":"assistant","content":"hello"}`, pricedUsage), pricedE2E)
	_, _ = e2eTokenKey(t, h) // the gpt-6-sol chain
	keyID := paygKeyID(t, h)

	resp := h.post(t, "/v1/chat/completions", tokenChatBody, h.apiKey)
	body := readAllString(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)

	billed := lastCostNanos(t, h, keyID)
	require.Positive(t, billed)
	require.Equal(t, float64(billed)/1e9, usageObject(t, body)["cost"])
}

func TestUnaryCost_PAYGAnthropic(t *testing.T) {
	h := newE2E(t, unaryUpstream(`{"role":"assistant","content":"hello"}`, pricedUsage), pricedE2E)
	_, _ = e2eTokenKey(t, h)
	keyID := paygKeyID(t, h)

	resp := h.post(t, "/v1/messages", `{"model":"gpt-6-sol","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`, h.apiKey)
	body := readAllString(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)

	billed := lastCostNanos(t, h, keyID)
	require.Positive(t, billed)
	require.Equal(t, float64(billed)/1e9, usageObject(t, body)["cost"])
}

func TestUnaryCost_TokenPackage(t *testing.T) {
	h := newE2E(t, unaryUpstream(`{"role":"assistant","content":"hello"}`, pricedUsage), pricedE2E)
	tokenKey, _ := e2eTokenKey(t, h)

	resp := h.post(t, "/v1/chat/completions", tokenChatBody, tokenKey)
	body := readAllString(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)

	_, has := usageObject(t, body)["cost"]
	require.False(t, has, body)
	require.NotContains(t, body, `"cost"`)
}

func TestUnaryCost_NoHeader(t *testing.T) {
	h := newE2E(t, unaryUpstream(`{"role":"assistant","content":"hello"}`, pricedUsage), pricedE2E)
	_, _ = e2eTokenKey(t, h)

	resp := h.post(t, "/v1/chat/completions", tokenChatBody, h.apiKey)
	_ = readAllString(t, resp)
	for name := range resp.Header {
		require.NotContains(t, strings.ToLower(name), "cost", name)
	}
}

// The pipeline hands the gateway the same *ChatResponse it stores in (and
// replays from) the response cache, so stamping the cost must not write
// through to it: a later token package hit would otherwise show USD.
func TestWithBilledCostLeavesSourceUntouched(t *testing.T) {
	src := &core.ChatResponse{Usage: core.Usage{PromptTokens: 10}}

	out := withBilledCost(src, true, 1234500)
	require.True(t, out.Usage.ShowBilledCost)
	require.Equal(t, int64(1234500), out.Usage.BilledCostNanos)
	require.Equal(t, 10, out.Usage.PromptTokens)

	require.False(t, src.Usage.ShowBilledCost)
	require.Zero(t, src.Usage.BilledCostNanos)

	hidden := withBilledCost(&core.ChatResponse{Usage: core.Usage{ShowBilledCost: true, BilledCostNanos: 9}}, false, 1234)
	require.False(t, hidden.Usage.ShowBilledCost, "a token package key never shows cost, whatever the source carried")
}
