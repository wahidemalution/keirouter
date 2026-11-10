package pipeline

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/core"
	"github.com/mydisha/keirouter/backend/internal/transform"
)

func tokenReq(maxTokens int) *core.ChatRequest {
	return &core.ChatRequest{
		MaxTokens: &maxTokens,
		Messages:  []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{{Type: core.PartText, Text: "hi"}}}},
		Metadata:  core.RequestMetadata{TokenMetered: true, BudgetPromptTokens: 7},
	}
}

const (
	antStart     = `{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","content":[],"usage":{"input_tokens":200,"output_tokens":1}}}`
	antDeltaOne  = `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`
	antTextDelta = `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`
)

// The cut decision rests on whether message_delta's usage arrived, not on
// the size of the output count.
func TestTokenCutUsageAnthropicPlaceholder(t *testing.T) {
	codec := transform.AnthropicCodec{}
	channel := func(lines ...string) core.Usage {
		var u core.Usage
		for _, l := range lines {
			chunks, err := codec.ParseStreamLine([]byte(l), "m")
			require.NoError(t, err)
			for _, c := range chunks {
				if c.Type == core.ChunkUsage {
					u = mergeUsage(u, *c.Usage)
				}
			}
		}
		return u
	}
	direct := func(lines ...string) core.Usage {
		raw := ""
		for _, l := range lines {
			raw += "data: " + l + "\n\n"
		}
		return extractUsageFromStream([]byte(raw))
	}
	for name, parse := range map[string]func(...string) core.Usage{"channel": channel, "direct": direct} {
		t.Run(name+"/placeholder_only", func(t *testing.T) {
			got := tokenCutStreamUsage(tokenReq(4000), parse(antStart, antTextDelta), 2)
			require.Equal(t, 4000, got.CompletionTokens, "message_start's output count is a placeholder")
			require.Equal(t, 200, got.PromptTokens)
		})
		t.Run(name+"/message_delta_of_one", func(t *testing.T) {
			got := tokenCutStreamUsage(tokenReq(4000), parse(antStart, antTextDelta, antDeltaOne), 2)
			require.Equal(t, 1, got.CompletionTokens, "a reported count of 1 is real")
		})
	}
}

// M6: the visible output may exceed the cap (a provider ignoring max_tokens).
func TestTokenCutUsageVisibleEstimateAboveCap(t *testing.T) {
	got := tokenCutStreamUsage(tokenReq(5), core.Usage{}, 700)
	require.Equal(t, 200, got.CompletionTokens, "700 chars at 3.5 per token beat a cap of 5")
}

func TestDirectStreamFinishMatchesFramingOnly(t *testing.T) {
	notFinished := []string{
		// Marker words inside model text.
		`data: {"choices":[{"index":0,"delta":{"content":"[DONE] message_stop response.completed finishReason"}}]}`,
		`data: {"choices":[{"index":0,"delta":{"content":"\"finish_reason\":\"stop\" \"type\":\"message_stop\""},"finish_reason":null}]}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"event: message_stop"}}`,
	}
	for _, raw := range notFinished {
		finished, _ := scanStreamFraming([]byte(raw + "\n\n"))
		require.False(t, finished, raw)
	}
	finishedStreams := []string{
		"data: [DONE]",
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		"event: message_stop\ndata: {\"type\":\"message_stop\"}",
		`data: {"type": "response.completed","response":{}}`,
		`data: {"type":"response.incomplete","response":{}}`,
		`data: {"candidates":[{"content":{"parts":[{"text":"hi"}]},"finishReason":"STOP"}]}`,
		`{"model":"m","done":true}`,
	}
	for _, raw := range finishedStreams {
		finished, _ := scanStreamFraming([]byte(raw + "\n\n"))
		require.True(t, finished, raw)
	}
	_, tool := scanStreamFraming([]byte(`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c","function":{"name":"f","arguments":""}}]}}]}` + "\n\n"))
	require.True(t, tool)
	_, tool = scanStreamFraming([]byte(`data: {"choices":[{"index":0,"delta":{"content":"tool_calls tool_use"}}]}` + "\n\n"))
	require.False(t, tool)
}

// A finished token stream is billed from reported output only; without it
// the hidden reasoning is unknown, so it is charged as cut.
func TestTokenStreamEndUsageFinishedWithoutReportedOutput(t *testing.T) {
	for name, usage := range map[string]core.Usage{
		"no_usage":    {},
		"prompt_only": {PromptTokens: 20},
		"pending":     {PromptTokens: 20, CompletionTokens: 1, OutputPending: true},
	} {
		t.Run(name, func(t *testing.T) {
			got := tokenStreamEndUsage(tokenReq(4000), usage, 40, false)
			require.Equal(t, 4000, got.CompletionTokens)
			require.Equal(t, max(usage.PromptTokens, 7), got.PromptTokens)
		})
	}
	reported := core.Usage{PromptTokens: 20, CompletionTokens: 30, TotalTokens: 50}
	require.Equal(t, reported, tokenStreamEndUsage(tokenReq(4000), reported, 40, false))
}
