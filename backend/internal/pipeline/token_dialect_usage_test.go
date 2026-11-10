package pipeline

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/core"
	"github.com/mydisha/keirouter/backend/internal/transform"
)

// Every fixture reports 1000 prompt tokens (800 of them cached) and 1200
// output tokens (1000 of them reasoning), in its dialect's own shape.
type dialectUsageFixture struct {
	name    string
	dialect core.Dialect
	unary   string
	stream  []string // SSE data payloads, in order
	// reportsReasoning is false for dialects whose usage has no separate
	// reasoning count (Anthropic folds thinking into output_tokens).
	reportsReasoning bool
}

var dialectUsageFixtures = []dialectUsageFixture{
	{
		name: "openai_chat", dialect: core.DialectOpenAI, reportsReasoning: true,
		unary: `{"id":"x","model":"m","choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":1000,"completion_tokens":1200,"total_tokens":2200,` +
			`"prompt_tokens_details":{"cached_tokens":800},"completion_tokens_details":{"reasoning_tokens":1000}}}`,
		stream: []string{
			`{"id":"x","model":"m","choices":[{"index":0,"delta":{"content":"hi"}}]}`,
			`{"id":"x","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			`{"id":"x","model":"m","choices":[],"usage":{"prompt_tokens":1000,"completion_tokens":1200,"total_tokens":2200,` +
				`"prompt_tokens_details":{"cached_tokens":800},"completion_tokens_details":{"reasoning_tokens":1000}}}`,
		},
	},
	{
		name: "responses_completed", dialect: core.DialectOpenAIResponses, reportsReasoning: true,
		unary: `{"id":"r","object":"response","status":"completed","output":[{"type":"message","role":"assistant",` +
			`"content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":1000,"input_tokens_details":{"cached_tokens":800},` +
			`"output_tokens":1200,"output_tokens_details":{"reasoning_tokens":1000},"total_tokens":2200}}`,
		stream: []string{
			`{"type":"response.output_text.delta","output_index":0,"delta":"hi"}`,
			`{"type":"response.completed","response":{"id":"r","status":"completed","usage":{"input_tokens":1000,` +
				`"input_tokens_details":{"cached_tokens":800},"output_tokens":1200,"output_tokens_details":{"reasoning_tokens":1000},"total_tokens":2200}}}`,
		},
	},
	{
		// A token key's output cap makes max_output_tokens endings common.
		name: "responses_incomplete", dialect: core.DialectOpenAIResponses, reportsReasoning: true,
		unary: `{"id":"r","object":"response","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},` +
			`"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}],` +
			`"usage":{"input_tokens":1000,"input_tokens_details":{"cached_tokens":800},"output_tokens":1200,` +
			`"output_tokens_details":{"reasoning_tokens":1000},"total_tokens":2200}}`,
		stream: []string{
			`{"type":"response.output_text.delta","output_index":0,"delta":"hi"}`,
			`{"type":"response.incomplete","response":{"id":"r","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},` +
				`"usage":{"input_tokens":1000,"input_tokens_details":{"cached_tokens":800},"output_tokens":1200,` +
				`"output_tokens_details":{"reasoning_tokens":1000},"total_tokens":2200}}}`,
		},
	},
	{
		name: "anthropic", dialect: core.DialectAnthropic,
		unary: `{"id":"m","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn",` +
			`"usage":{"input_tokens":200,"cache_read_input_tokens":800,"output_tokens":1200}}`,
		stream: []string{
			`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","content":[],` +
				`"usage":{"input_tokens":200,"cache_read_input_tokens":800,"output_tokens":1}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1200}}`,
			`{"type":"message_stop"}`,
		},
	},
	{
		name: "gemini", dialect: core.DialectGemini, reportsReasoning: true,
		unary: `{"candidates":[{"content":{"parts":[{"text":"hi"}],"role":"model"},"finishReason":"STOP"}],` +
			`"usageMetadata":{"promptTokenCount":1000,"cachedContentTokenCount":800,"candidatesTokenCount":200,` +
			`"thoughtsTokenCount":1000,"totalTokenCount":2200}}`,
		stream: []string{
			`{"candidates":[{"content":{"parts":[{"text":"hi"}],"role":"model"},"finishReason":"STOP"}],` +
				`"usageMetadata":{"promptTokenCount":1000,"cachedContentTokenCount":800,"candidatesTokenCount":200,` +
				`"thoughtsTokenCount":1000,"totalTokenCount":2200}}`,
		},
	},
}

// recordedUsages returns the usage each pipeline path would record for f:
// a unary response, the channel stream (codec chunks merged as pumpStream
// does) and the direct stream (raw bytes scanned as the direct path does).
func recordedUsages(t *testing.T, f dialectUsageFixture) map[string]core.Usage {
	t.Helper()
	reg := transform.DefaultRegistry()
	codec, err := reg.Codec(f.dialect)
	require.NoError(t, err)
	resp, err := codec.ParseResponse([]byte(f.unary), "m")
	require.NoError(t, err)

	sc, err := reg.StreamCodec(f.dialect)
	require.NoError(t, err)
	var channel core.Usage
	var raw strings.Builder
	for _, line := range f.stream {
		chunks, err := sc.ParseStreamLine([]byte(line), "m")
		require.NoError(t, err)
		for _, c := range chunks {
			if c.Type == core.ChunkUsage && c.Usage != nil {
				channel = mergeUsage(channel, *c.Usage)
			}
		}
		raw.WriteString("data: " + line + "\n\n")
	}
	return map[string]core.Usage{
		"unary":   resp.Usage,
		"channel": channel,
		"direct":  extractUsageFromStream([]byte(raw.String())),
	}
}

func TestTokenReasoningTokensCounted(t *testing.T) {
	for _, f := range dialectUsageFixtures {
		for path, u := range recordedUsages(t, f) {
			t.Run(f.name+"/"+path, func(t *testing.T) {
				require.Equal(t, 1200, u.CompletionTokens, "completion includes reasoning tokens")
				if f.reportsReasoning {
					require.Equal(t, 1000, u.ReasoningTokens)
				}
			})
		}
	}
}

func TestTokenCachedTokensCounted(t *testing.T) {
	for _, f := range dialectUsageFixtures {
		for path, u := range recordedUsages(t, f) {
			t.Run(f.name+"/"+path, func(t *testing.T) {
				require.Equal(t, 1000, u.PromptTokens, "prompt includes cached tokens")
				require.Equal(t, 800, u.CachedTokens)
			})
		}
	}
}
