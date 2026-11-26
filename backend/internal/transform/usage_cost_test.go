package transform

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/core"
)

func decodeObj(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

func costTestResponse() core.ChatResponse {
	return core.ChatResponse{ID: "r1", Model: "m", FinishReason: core.FinishStop,
		Message: core.Message{Role: "assistant", Content: []core.ContentPart{{Type: core.PartText, Text: "hi"}}},
		Usage:   core.Usage{PromptTokens: 1200, CompletionTokens: 85, TotalTokens: 1285, CachedTokens: 1000}}
}

func TestRenderResponseCost(t *testing.T) {
	for _, tc := range []struct {
		name  string
		codec Codec
	}{
		{"openai", OpenAICodec{}},
		{"anthropic", AnthropicCodec{}},
		{"responses", OpenAIResponsesCodec{}},
	} {
		t.Run(tc.name+"/payg", func(t *testing.T) {
			r := costTestResponse()
			r.Usage.ShowBilledCost, r.Usage.BilledCostNanos = true, 1234500
			out, err := tc.codec.RenderResponse(&r)
			require.NoError(t, err)
			usage := decodeObj(t, out)["usage"].(map[string]any)
			require.Equal(t, 0.0012345, usage["cost"])
		})
		t.Run(tc.name+"/token package hides cost", func(t *testing.T) {
			r := costTestResponse()
			r.Usage.BilledCostNanos = 1234500
			out, err := tc.codec.RenderResponse(&r)
			require.NoError(t, err)
			_, has := decodeObj(t, out)["usage"].(map[string]any)["cost"]
			require.False(t, has)
		})
		t.Run(tc.name+"/zero cost still rendered", func(t *testing.T) {
			r := costTestResponse()
			r.Usage.ShowBilledCost = true
			out, err := tc.codec.RenderResponse(&r)
			require.NoError(t, err)
			cost, has := decodeObj(t, out)["usage"].(map[string]any)["cost"]
			require.True(t, has)
			require.Equal(t, 0.0, cost)
		})
	}
}

func TestRenderResponseCostKeepsTokenFields(t *testing.T) {
	r := costTestResponse()
	r.Usage.ShowBilledCost, r.Usage.BilledCostNanos = true, 1234500
	out, err := AnthropicCodec{}.RenderResponse(&r)
	require.NoError(t, err)
	usage := decodeObj(t, out)["usage"].(map[string]any)
	require.Equal(t, 200.0, usage["input_tokens"])
	require.Equal(t, 1000.0, usage["cache_read_input_tokens"])
	require.Equal(t, 85.0, usage["output_tokens"])
}

func TestBilledCostUSD(t *testing.T) {
	v, ok := billedCostUSD(core.Usage{ShowBilledCost: true, BilledCostNanos: 2850})
	require.True(t, ok)
	require.Equal(t, 0.00000285, v)
	_, ok = billedCostUSD(core.Usage{BilledCostNanos: 15900})
	require.False(t, ok)
}

// The balance is debited in nanos, so usage.cost must not round to micros, and
// a tiny charge must read as a plain decimal.
func TestRenderResponseCostKeepsNanos(t *testing.T) {
	for _, codec := range []Codec{OpenAICodec{}, AnthropicCodec{}, OpenAIResponsesCodec{}} {
		r := costTestResponse()
		r.Usage.ShowBilledCost, r.Usage.BilledCostNanos = true, 2850
		out, err := codec.RenderResponse(&r)
		require.NoError(t, err)
		require.Contains(t, string(out), `"cost":0.00000285`)
	}
}
