package gateway

import "github.com/mydisha/keirouter/backend/internal/core"

// withBilledCost returns a shallow copy of resp whose usage carries the
// customer charge for rendering as usage.cost. The source is never modified:
// the pipeline may hand out the same pointer the response cache stores, and a
// cost written through it would replay to later requests, including token
// package keys. show is false for token package keys.
func withBilledCost(resp *core.ChatResponse, show bool, costMicros int64) *core.ChatResponse {
	out := *resp
	out.Usage.ShowBilledCost = show
	out.Usage.BilledCostMicros = 0
	if show {
		out.Usage.BilledCostMicros = costMicros
	}
	return &out
}
