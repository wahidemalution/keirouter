package gateway

import (
	"bytes"
	"encoding/json"

	"github.com/mydisha/keirouter/backend/internal/core"
)

// withBilledCost returns a shallow copy of resp whose usage carries the
// customer charge for rendering as usage.cost. The source is never modified:
// the pipeline may hand out the same pointer the response cache stores, and a
// cost written through it would replay to later requests, including token
// package keys. show is false for token package keys.
func withBilledCost(resp *core.ChatResponse, show bool, costNanos int64) *core.ChatResponse {
	out := *resp
	out.Usage.ShowBilledCost = show
	out.Usage.BilledCostNanos = 0
	if show {
		out.Usage.BilledCostNanos = costNanos
	}
	return &out
}

// upstreamCostKeys are the usage fields in which an upstream reports its own
// charge (OpenRouter and inferhub cost and cost_details, Surplus
// buyer_cost_micro). None of them may reach a client.
var upstreamCostKeys = []string{"cost", "cost_details", "buyer_cost_micro"}

// stripUpstreamCost removes upstreamCostKeys from every usage object in a raw
// SSE frame on the direct path, which otherwise copies upstream bytes through
// unchanged. Frames without a quoted "cost key are returned as they are, so
// content frames keep their zero-copy fast path.
func stripUpstreamCost(frame []byte) []byte {
	if !bytes.Contains(frame, []byte(`"usage"`)) ||
		(!bytes.Contains(frame, []byte(`"cost`)) && !bytes.Contains(frame, []byte(`"buyer_cost_micro"`))) {
		return frame
	}
	var out bytes.Buffer
	changed := false
	for _, line := range bytes.SplitAfter(frame, []byte("\n")) {
		body, eol := bytes.CutSuffix(line, []byte("\n"))
		payload, isData := bytes.CutPrefix(body, []byte("data:"))
		if isData {
			if cleaned, ok := withoutUsageCost(bytes.TrimSpace(payload)); ok {
				out.WriteString("data: ")
				out.Write(cleaned)
				if eol {
					out.WriteByte('\n')
				}
				changed = true
				continue
			}
		}
		out.Write(line)
	}
	if !changed {
		return frame
	}
	return out.Bytes()
}

// withoutUsageCost re-encodes a JSON payload with upstreamCostKeys dropped
// from every object under a "usage" key. ok is false when the payload is not
// JSON or carried no such key.
func withoutUsageCost(payload []byte) ([]byte, bool) {
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	if !dropUsageCost(v, false) {
		return nil, false
	}
	out, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}
	return out, true
}

func dropUsageCost(v any, inUsage bool) bool {
	dropped := false
	switch x := v.(type) {
	case map[string]any:
		if inUsage {
			for _, k := range upstreamCostKeys {
				if _, ok := x[k]; ok {
					delete(x, k)
					dropped = true
				}
			}
		}
		for k, child := range x {
			if dropUsageCost(child, k == "usage") {
				dropped = true
			}
		}
	case []any:
		for _, child := range x {
			if dropUsageCost(child, false) {
				dropped = true
			}
		}
	}
	return dropped
}
