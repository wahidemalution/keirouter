package transform

import "github.com/mydisha/keirouter/backend/internal/core"

// billedCostUSD returns the customer charge to render as usage.cost, in USD.
// ok is false when the cost must not be shown (token package keys).
func billedCostUSD(u core.Usage) (cost float64, ok bool) {
	if !u.ShowBilledCost {
		return 0, false
	}
	return float64(u.BilledCostMicros) / 1e6, true
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
