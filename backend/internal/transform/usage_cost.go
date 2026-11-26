package transform

import "github.com/mydisha/keirouter/backend/internal/core"

// billedCostUSD returns the customer charge to render as usage.cost, in USD.
// ok is false when the cost must not be shown (token package keys).
func billedCostUSD(u core.Usage) (cost float64, ok bool) {
	if !u.ShowBilledCost {
		return 0, false
	}
	return float64(u.BilledCostNanos) / 1e9, true
}

// renderResponsesUsage renders usage in the Responses API shape, shared by the
// unary response and the streamed response.completed event.
func renderResponsesUsage(u core.Usage) map[string]any {
	out := map[string]any{
		"input_tokens":  u.PromptTokens,
		"output_tokens": u.CompletionTokens,
		"total_tokens":  u.TotalTokens,
	}
	if cost, ok := billedCostUSD(u); ok {
		out["cost"] = cost
	}
	return out
}

// mergeStreamUsage folds a later usage chunk into an earlier one. An
// Anthropic upstream splits usage across message_start (input) and
// message_delta (output), so a zero count in next keeps prev's, and the total
// is raised to cover both halves.
func mergeStreamUsage(prev, next core.Usage) core.Usage {
	if next.PromptTokens == 0 {
		next.PromptTokens, next.CachedTokens, next.CacheWriteTokens = prev.PromptTokens, prev.CachedTokens, prev.CacheWriteTokens
	}
	if next.CompletionTokens == 0 {
		next.CompletionTokens = prev.CompletionTokens
	}
	if next.ReasoningTokens == 0 {
		next.ReasoningTokens = prev.ReasoningTokens
	}
	if sum := next.PromptTokens + next.CompletionTokens; next.TotalTokens < sum {
		next.TotalTokens = sum
	}
	return next
}

// Keys under StreamState.Custom for the held usage frame and the billed cost.
const (
	heldUsageKey   = "billed_held_usage"
	billedCostKey  = "billed_cost_micros"
	billedUsageKey = "billed_usage"
)

// holdStreamUsage merges a usage chunk into the frame held until
// RenderStreamDone.
func holdStreamUsage(state *StreamState, u core.Usage) {
	if state.Custom == nil {
		state.Custom = map[string]any{}
	}
	if prev, ok := state.Custom[heldUsageKey].(core.Usage); ok {
		u = mergeStreamUsage(prev, u)
	}
	state.Custom[heldUsageKey] = u
}

// heldStreamUsage returns the held usage frame with the billed cost attached
// when one arrived. ok is false when no usage was held.
func heldStreamUsage(state *StreamState) (core.Usage, bool) {
	u, ok := state.Custom[heldUsageKey].(core.Usage)
	if !ok {
		return core.Usage{}, false
	}
	return withStreamCost(state, u), true
}

// storeBilledCost records a ChunkBilledCost on the stream state.
func storeBilledCost(state *StreamState, chunk core.StreamChunk) {
	if chunk.Usage == nil {
		return
	}
	if state.Custom == nil {
		state.Custom = map[string]any{}
	}
	state.Custom[billedCostKey] = chunk.Usage.BilledCostNanos
	state.Custom[billedUsageKey] = *chunk.Usage
}

// billedOrHeld returns held when present; otherwise, for a stream that shows
// cost, the usage the request was billed on (an estimate when the upstream
// reported none), so the client still sees what it was charged. OpenAI does
// not use it: it never invents a usage frame the upstream did not send.
func billedOrHeld(state *StreamState, held core.Usage, ok bool) (core.Usage, bool) {
	if ok || !state.ShowBilledCost {
		return held, ok
	}
	billed, ok := state.Custom[billedUsageKey].(core.Usage)
	return billed, ok
}

// withStreamCost marks u for usage.cost rendering when the stream shows cost
// and the cost has arrived. A stream that ended before its cost was recorded
// renders usage without it rather than a wrong 0.
func withStreamCost(state *StreamState, u core.Usage) core.Usage {
	u.ShowBilledCost, u.BilledCostNanos = false, 0
	if !state.ShowBilledCost {
		return u
	}
	if cost, ok := state.Custom[billedCostKey].(int64); ok {
		u.ShowBilledCost, u.BilledCostNanos = true, cost
	}
	return u
}

// streamCostField is usage.cost for a streamed usage struct: nil when the
// cost must not be shown.
func streamCostField(u core.Usage) *float64 {
	if cost, ok := billedCostUSD(u); ok {
		return &cost
	}
	return nil
}
