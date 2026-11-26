package transform

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/core"
)

// runStream renders chunks then the terminal frames, as the gateway does.
func runStream(t *testing.T, codec StreamCodec, show bool, chunks ...core.StreamChunk) string {
	t.Helper()
	state := &StreamState{Model: "m", MessageID: "id1", ShowBilledCost: show}
	var b strings.Builder
	for _, c := range chunks {
		events, err := codec.RenderStreamChunk(c, state)
		require.NoError(t, err)
		for _, ev := range events {
			b.Write(ev)
		}
	}
	for _, ev := range codec.RenderStreamDone(state) {
		b.Write(ev)
	}
	return b.String()
}

func textChunk(s string) core.StreamChunk { return core.StreamChunk{Type: core.ChunkText, Delta: s} }
func finishChunk() core.StreamChunk {
	return core.StreamChunk{Type: core.ChunkFinish, FinishReason: core.FinishStop}
}
func usageChunk(u core.Usage) core.StreamChunk {
	return core.StreamChunk{Type: core.ChunkUsage, Usage: &u}
}
func costChunk(nanos int64) core.StreamChunk {
	return core.StreamChunk{Type: core.ChunkBilledCost, Usage: &core.Usage{BilledCostNanos: nanos}}
}

var streamUsage = core.Usage{PromptTokens: 1200, CompletionTokens: 85, TotalTokens: 1285}

func TestStreamCost_OpenAI(t *testing.T) {
	out := runStream(t, OpenAICodec{}, true,
		textChunk("hi"), finishChunk(), usageChunk(streamUsage), costChunk(1234500))

	finishAt := strings.Index(out, `"finish_reason":"stop"`)
	usageAt := strings.Index(out, `"usage":`)
	doneAt := strings.Index(out, "data: [DONE]")
	require.Positive(t, finishAt)
	require.Greater(t, usageAt, finishAt, "usage follows the finish chunk")
	require.Greater(t, doneAt, usageAt, "[DONE] closes the stream after usage")
	require.Equal(t, 1, strings.Count(out, `"usage":`))
	require.Contains(t, out, `"prompt_tokens":1200,"completion_tokens":85,"total_tokens":1285`)
	require.Contains(t, out, `"cost":0.0012345`)
}

func TestStreamCost_OpenAI_Off(t *testing.T) {
	chunks := []core.StreamChunk{textChunk("hi"), finishChunk(), usageChunk(streamUsage), costChunk(1234500)}
	out := runStream(t, OpenAICodec{}, false, chunks...)
	require.NotContains(t, out, `"cost"`)

	// Without the flag the usage event leaves at once, as before this change.
	state := &StreamState{Model: "m", MessageID: "id1"}
	events, err := OpenAICodec{}.RenderStreamChunk(usageChunk(streamUsage), state)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Contains(t, string(events[0]), `"usage":`)
}

func TestStreamCost_OpenAI_NoUsage(t *testing.T) {
	out := runStream(t, OpenAICodec{}, true, textChunk("hi"), finishChunk())
	require.Equal(t, 1, strings.Count(out, "data: [DONE]"))
	require.NotContains(t, out, `"usage"`)
}

func TestStreamCost_OpenAI_TwoUsageChunks(t *testing.T) {
	out := runStream(t, OpenAICodec{}, true, textChunk("hi"),
		usageChunk(core.Usage{PromptTokens: 1200, CachedTokens: 1000, OutputPending: true}),
		finishChunk(),
		usageChunk(core.Usage{CompletionTokens: 85}),
		costChunk(1234500))
	require.Equal(t, 1, strings.Count(out, `"usage":`))
	require.Contains(t, out, `"prompt_tokens":1200`)
	require.Contains(t, out, `"completion_tokens":85`)
	require.Contains(t, out, `"total_tokens":1285`)
	require.Contains(t, out, `"cached_tokens":1000`)
	require.Contains(t, out, `"cost":0.0012345`)
}

func TestStreamCost_OpenAI_NoCostArrived(t *testing.T) {
	out := runStream(t, OpenAICodec{}, true, textChunk("hi"), finishChunk(), usageChunk(streamUsage))
	require.Contains(t, out, `"usage":`)
	require.NotContains(t, out, `"cost"`)
}

func TestStreamCost_OpenAI_ZeroCost(t *testing.T) {
	out := runStream(t, OpenAICodec{}, true, textChunk("hi"), finishChunk(), usageChunk(streamUsage), costChunk(0))
	require.Contains(t, out, `"cost":0`)
}

func TestStreamCost_Responses(t *testing.T) {
	out := runStream(t, OpenAIResponsesCodec{}, true,
		textChunk("hi"), finishChunk(), usageChunk(streamUsage), costChunk(1234500))
	require.Equal(t, 1, strings.Count(out, "event: response.completed"))
	require.Greater(t, strings.Index(out, "event: response.completed"), strings.Index(out, "response.output_text.delta"))
	completed := out[strings.Index(out, "event: response.completed"):]
	require.Contains(t, completed, `"input_tokens":1200`)
	require.Contains(t, completed, `"output_tokens":85`)
	require.Contains(t, completed, `"cost":0.0012345`)
}

func TestStreamCost_Responses_Off(t *testing.T) {
	out := runStream(t, OpenAIResponsesCodec{}, false,
		textChunk("hi"), finishChunk(), usageChunk(streamUsage), costChunk(1234500))
	require.Equal(t, 1, strings.Count(out, "event: response.completed"))
	require.Contains(t, out, `"input_tokens":1200`)
	require.NotContains(t, out, `"cost"`)
}

func TestStreamCost_Anthropic(t *testing.T) {
	out := runStream(t, AnthropicCodec{}, true, textChunk("hi"), finishChunk(),
		usageChunk(core.Usage{PromptTokens: 10500, CachedTokens: 8000, CacheWriteTokens: 2000, CompletionTokens: 300}),
		costChunk(15900000))
	delta := out[strings.Index(out, "event: message_delta"):]
	require.Contains(t, delta, `"input_tokens":500`)
	require.Contains(t, delta, `"cache_read_input_tokens":8000`)
	require.Contains(t, delta, `"cache_creation_input_tokens":2000`)
	require.Contains(t, delta, `"output_tokens":300`)
	require.Contains(t, delta, `"cost":0.0159`)
	require.Equal(t, 1, strings.Count(out, `"cost"`), "only the terminal usage carries cost")
}

func TestStreamCost_Anthropic_Off(t *testing.T) {
	out := runStream(t, AnthropicCodec{}, false, textChunk("hi"), finishChunk(), usageChunk(streamUsage), costChunk(1234500))
	require.NotContains(t, out, `"cost"`)
}

func TestStreamCost_OtherCodecsIgnoreChunk(t *testing.T) {
	for _, codec := range []StreamCodec{GeminiCodec{}, OllamaCodec{}} {
		for _, show := range []bool{true, false} {
			events, err := codec.RenderStreamChunk(costChunk(1234500), &StreamState{Model: "m", ShowBilledCost: show})
			require.NoError(t, err)
			require.Empty(t, events)
		}
	}
}

func TestMergeStreamUsage(t *testing.T) {
	got := mergeStreamUsage(core.Usage{PromptTokens: 1200, CachedTokens: 1000, CacheWriteTokens: 50, CompletionTokens: 1},
		core.Usage{CompletionTokens: 85})
	require.Equal(t, 1200, got.PromptTokens)
	require.Equal(t, 1000, got.CachedTokens)
	require.Equal(t, 50, got.CacheWriteTokens)
	require.Equal(t, 85, got.CompletionTokens)
}

// billedChunk is the cost chunk as pumpStream sends it: the recorded usage
// with the charge attached.
func billedChunk(u core.Usage, nanos int64) core.StreamChunk {
	u.BilledCostNanos = nanos
	return core.StreamChunk{Type: core.ChunkBilledCost, Usage: &u}
}

// When the upstream sent no usage the request is still billed (on an
// estimate); Anthropic and Responses show that recorded usage and its cost.
func TestStreamCost_Anthropic_NoUpstreamUsage(t *testing.T) {
	out := runStream(t, AnthropicCodec{}, true, textChunk("hi"), finishChunk(),
		billedChunk(core.Usage{PromptTokens: 12, CompletionTokens: 3, TotalTokens: 15}, 50000))
	delta := out[strings.Index(out, "event: message_delta"):]
	require.Contains(t, delta, `"input_tokens":12`)
	require.Contains(t, delta, `"output_tokens":3`)
	require.Contains(t, delta, `"cost":0.00005`)
}

func TestStreamCost_Anthropic_UpstreamUsageWins(t *testing.T) {
	out := runStream(t, AnthropicCodec{}, true, textChunk("hi"), finishChunk(), usageChunk(streamUsage),
		billedChunk(core.Usage{PromptTokens: 12, CompletionTokens: 3}, 1234500))
	delta := out[strings.Index(out, "event: message_delta"):]
	require.Contains(t, delta, `"input_tokens":1200`)
	require.Contains(t, delta, `"output_tokens":85`)
	require.Contains(t, delta, `"cost":0.0012345`)
}

func TestStreamCost_Responses_NoUpstreamUsage(t *testing.T) {
	out := runStream(t, OpenAIResponsesCodec{}, true, textChunk("hi"), finishChunk(),
		billedChunk(core.Usage{PromptTokens: 12, CompletionTokens: 3, TotalTokens: 15}, 50000))
	completed := out[strings.Index(out, "event: response.completed"):]
	require.Contains(t, completed, `"input_tokens":12`)
	require.Contains(t, completed, `"cost":0.00005`)
}

func TestStreamCost_OpenAI_NoUpstreamUsageInventsNothing(t *testing.T) {
	out := runStream(t, OpenAICodec{}, true, textChunk("hi"), finishChunk(),
		billedChunk(core.Usage{PromptTokens: 12, CompletionTokens: 3}, 50000))
	require.NotContains(t, out, `"usage"`)
	require.Equal(t, 1, strings.Count(out, "data: [DONE]"))
}

func TestStreamCost_TokenStateIgnoresBilledUsage(t *testing.T) {
	out := runStream(t, AnthropicCodec{}, false, textChunk("hi"), finishChunk(),
		billedChunk(core.Usage{PromptTokens: 12, CompletionTokens: 3}, 50000))
	require.NotContains(t, out, `"cost"`)
	require.NotContains(t, out, `"input_tokens":12`, "without the flag the codec behaves as before")
}
