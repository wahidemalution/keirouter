# Billed cost in model responses (`usage.cost`) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every model response to a non-token-package key carries `usage.cost`, the USD amount KeiRouter billed for that request.

**Architecture:** `core.Usage` gains two router-internal fields (`ShowBilledCost`, `BilledCostMicros`) that the transform renderers turn into a `cost` number. Non-streaming: the gateway stamps them from `pipeline.Result.CostMicros` before rendering. Streaming: `pumpStream` emits a new `core.ChunkBilledCost` after recording the cost; the OpenAI and Responses codecs hold their usage frame until `RenderStreamDone` so the cost can join it; Anthropic already renders usage there. PAYG streams skip the direct zero-copy path.

**Tech Stack:** Go (backend/), testify `require`.

**Spec:** `docs/superpowers/specs/2026-11-26-usage-cost-in-response-design.md`

## Global Constraints

- Field name `cost`, JSON number, USD, value = `BilledCostMicros / 1_000_000`.
- Shown when the key is not a token-package key (`TokenMetered == false`). Never shown when `TokenMetered == true`.
- Never emit upstream cost: `UpstreamCostMicro`, `ProviderCostNanos`, Surplus `buyer_cost_micro`, OpenRouter/inferhub upstream `usage.cost`.
- No response header. No `cost_details` breakdown.
- Response-cache hits: `cost` is `0` (present, not omitted) for PAYG keys.
- Formats: OpenAI Chat, Responses, Anthropic. Gemini and Ollama unchanged and must ignore `ChunkBilledCost`.
- Every other usage field keeps its current shape and order.
- Go commands on this machine: `export PATH=/home/whd/sdk/go/bin:$PATH GOMAXPROCS=2 GOMEMLIMIT=1500MiB GOFLAGS=-p=1`, run from `backend/`, one test command at a time, no `-race`.

## Review Focus

1. Bansos key (notice set) on PAYG, OpenAI stream: order must stay `…notice text → finish chunk → usage chunk with cost → [DONE]`. Test in Task 4.
2. OpenAI stream where upstream sends no usage (client did not ask `include_usage`): no usage chunk is invented and `[DONE]` still ends the stream. Test in Task 3.
3. Stream error after usage was held: error event, then held usage (without cost), then terminal frame; no panic. Test in Task 3.
4. Anthropic upstream translated to an OpenAI client sends two usage chunks (input, then output): the single held usage frame carries both counts. Test in Task 3.
5. Upstream OpenRouter `usage.cost: 9.99` on a PAYG stream and unary response: client sees the billed cost, never 9.99. Test in Task 4.

---

### Task 1: Usage fields and non-stream renderers

**Files:**
- Modify: `backend/internal/core/response.go` (struct `Usage`, ~line 27; chunk type consts ~line 81)
- Create: `backend/internal/transform/usage_cost.go`
- Modify: `backend/internal/transform/openai.go:1294` (`renderOAIUsage`)
- Modify: `backend/internal/transform/anthropic.go:847` (`renderAntUsage`)
- Modify: `backend/internal/transform/openai_responses.go:~740` (non-stream usage map)
- Test: `backend/internal/transform/usage_cost_test.go`

**Interfaces:**
- Produces:
  - `core.Usage.ShowBilledCost bool` and `core.Usage.BilledCostMicros int64`, both tagged `json:"-"` (so the response cache never stores them).
  - `core.ChunkBilledCost ChunkType = "billed_cost"`: carries `Usage` with only `BilledCostMicros` set.
  - `func billedCostUSD(u core.Usage) (float64, bool)` in `transform/usage_cost.go`: returns `(float64(u.BilledCostMicros)/1e6, true)` when `u.ShowBilledCost`, else `(0, false)`.
  - `renderAntUsage` returns `map[string]any` (was `map[string]int`).

- [ ] **Step 1: Write the failing tests** in `usage_cost_test.go`:

```go
func TestRenderResponseCost(t *testing.T) {
	base := core.ChatResponse{ID: "r1", Model: "m", FinishReason: core.FinishStop,
		Message: core.Message{Role: "assistant", Content: []core.ContentPart{{Type: core.PartText, Text: "hi"}}},
		Usage:   core.Usage{PromptTokens: 1200, CompletionTokens: 85, TotalTokens: 1285, CachedTokens: 1000}}
	for _, tc := range []struct {
		name  string
		codec Codec
		path  string // gjson-style path to the usage object
	}{
		{"openai", OpenAICodec{}, "usage"},
		{"anthropic", AnthropicCodec{}, "usage"},
		{"responses", OpenAIResponsesCodec{}, "usage"},
	} {
		t.Run(tc.name+"/payg", func(t *testing.T) {
			r := base
			r.Usage.ShowBilledCost, r.Usage.BilledCostMicros = true, 1234
			out, err := tc.codec.RenderResponse(&r)
			require.NoError(t, err)
			usage := decodeObj(t, out)[tc.path].(map[string]any)
			require.Equal(t, 0.001234, usage["cost"])
		})
		t.Run(tc.name+"/token", func(t *testing.T) {
			r := base
			r.Usage.BilledCostMicros = 1234 // ShowBilledCost false
			out, err := tc.codec.RenderResponse(&r)
			require.NoError(t, err)
			_, has := decodeObj(t, out)[tc.path].(map[string]any)["cost"]
			require.False(t, has)
		})
	}
	t.Run("cache hit shows zero", func(t *testing.T) {
		r := base
		r.Usage.ShowBilledCost = true
		out, _ := OpenAICodec{}.RenderResponse(&r)
		require.Equal(t, 0.0, decodeObj(t, out)["usage"].(map[string]any)["cost"])
	})
}
```

`decodeObj` unmarshals into `map[string]any` with `require.NoError`. Adjust the `Content` literal to the real `core.Message` shape if it differs.

- [ ] **Step 2: Run, expect FAIL** (`ShowBilledCost` undefined)

Run: `go test ./internal/transform -run TestRenderResponseCost`

- [ ] **Step 3: Implement** the core fields, the chunk const, `billedCostUSD`, and add `"cost"` in each of the three non-stream renderers when `billedCostUSD` returns true. `renderAntUsage` changes to `map[string]any`; fix its callers.

- [ ] **Step 4: Run, expect PASS**, then the package: `go test ./internal/transform ./internal/core`

- [ ] **Step 5: Commit**

```bash
git add backend/internal/core/response.go backend/internal/transform/
git commit -m "feat(usage): render billed cost as usage.cost in non-stream responses"
```

---

### Task 2: Stamp cost on non-stream gateway responses

**Files:**
- Modify: `backend/internal/gateway/handlers.go:296-332` (`unaryChat`)
- Test: `backend/internal/gateway/usage_cost_response_test.go`

**Interfaces:**
- Consumes: `core.Usage.ShowBilledCost`, `core.Usage.BilledCostMicros` (Task 1); `pipeline.Result.CostMicros`; `opts.TokenMetered`.

- [ ] **Step 1: Write the failing tests** in `usage_cost_response_test.go`, using the existing gateway test harness (see `bansos_notice_test.go` / `token_accounting_test.go` for how a server, key and fake upstream are wired):
  - `TestUnaryCost_PAYG`: PAYG key, chain priced so the request costs 1234 micros; response `usage.cost == 0.001234`.
  - `TestUnaryCost_TokenPackage`: token-package key, same request; `usage` has no `cost` key.
  - `TestUnaryCost_CacheHit`: second identical PAYG request served from response cache; `usage.cost == 0`.
  - `TestUnaryCost_NoHeader`: PAYG response has no header whose name contains `Cost`.

- [ ] **Step 2: Run, expect FAIL**

Run: `go test ./internal/gateway -run TestUnaryCost`

- [ ] **Step 3: Implement** in `unaryChat`, after the `notice` block and before `RenderResponse`: when `!opts.TokenMetered`, set `result.Response.Usage.ShowBilledCost = true` and `result.Response.Usage.BilledCostMicros = result.CostMicros`.

- [ ] **Step 4: Run, expect PASS**

- [ ] **Step 5: Commit**

```bash
git add backend/internal/gateway/handlers.go backend/internal/gateway/usage_cost_response_test.go
git commit -m "feat(usage): return billed cost on non-stream responses for PAYG keys"
```

---

### Task 3: Stream codecs hold usage and attach cost

**Files:**
- Modify: `backend/internal/transform/transform.go:71` (`StreamState`)
- Modify: `backend/internal/transform/openai_stream.go` (`RenderStreamChunk` ChunkUsage case ~line 386, `RenderStreamDone` ~line 395, `oaiUsageOut` ~line 319)
- Modify: `backend/internal/transform/openai_responses_stream.go` (ChunkUsage case ~line 354, `RenderStreamDone` ~line 380)
- Modify: `backend/internal/transform/anthropic_stream.go` (`antUsageOut` ~line 179, ChunkUsage merge ~line 322, terminal `message_delta` in `RenderStreamDone`)
- Modify: `backend/internal/transform/usage_cost.go`
- Test: `backend/internal/transform/usage_cost_stream_test.go`

**Interfaces:**
- Consumes: `core.ChunkBilledCost`, `billedCostUSD` (Task 1).
- Produces:
  - `StreamState.ShowBilledCost bool`: set by the gateway (Task 4). When false every codec behaves exactly as today.
  - `func mergeStreamUsage(prev, next core.Usage) core.Usage` in `usage_cost.go`: the merge rule now inline in `anthropic_stream.go:327-335` (zero prompt fields take prev's prompt/cached/cache-write; zero completion takes prev's completion). Anthropic switches to it.
  - Codec behaviour when `state.ShowBilledCost`:
    - `ChunkUsage`: OpenAI and Responses merge into held state and emit nothing.
    - `ChunkBilledCost`: every codec stores `BilledCostMicros` in state and emits nothing. Gemini and Ollama ignore it (return `nil, nil`) regardless of the flag.
    - `RenderStreamDone`: OpenAI emits the held usage event (`choices: []`, usage + `cost` if a cost arrived) then `[DONE]`; no held usage means `[DONE]` only. Responses emits `response.completed` with the held usage + `cost`. Anthropic adds `cost` to the terminal `message_delta` usage.
  - `oaiUsageOut` and `antUsageOut` gain `Cost *float64 \`json:"cost,omitempty"\``, and the Responses usage maps add `"cost"`. Set only when a cost arrived; a stored cost of 0 still renders `0`.

- [ ] **Step 1: Write the failing tests** in `usage_cost_stream_test.go`. Drive each codec with `RenderStreamChunk` and `RenderStreamDone` and assert on the concatenated SSE text:
  - `TestStreamCost_OpenAI`: text, finish, usage{1200,85}, billed_cost{1234}, done → the usage event comes after the finish event, contains `"cost":0.001234`, and is followed by `data: [DONE]`.
  - `TestStreamCost_OpenAI_Off`: same chunks, `ShowBilledCost=false` → output identical to today's (usage event right after the usage chunk, no `cost`).
  - `TestStreamCost_OpenAI_NoUsage` (Review Focus 2): text, finish, done → exactly one `[DONE]`, no `"usage"`.
  - `TestStreamCost_OpenAI_TwoUsageChunks` (Review Focus 4): usage{Prompt:1200}, usage{Completion:85}, billed_cost, done → one usage event with `"prompt_tokens":1200` and `"completion_tokens":85`.
  - `TestStreamCost_OpenAI_NoCostArrived` (Review Focus 3): usage, done with no billed_cost → usage event without a `cost` key.
  - `TestStreamCost_Responses`: `response.completed` appears once, after the text deltas, with `"cost":0.001234`.
  - `TestStreamCost_Anthropic`: terminal `message_delta` usage has `cache_read_input_tokens`, `cache_creation_input_tokens` and `"cost":0.0159` for usage {Prompt:10500, Cached:8000, CacheWrite:2000, Completion:300} and billed_cost 15900.
  - `TestStreamCost_GeminiIgnoresChunk`: Gemini and Ollama codecs return no events for `ChunkBilledCost`.

- [ ] **Step 2: Run, expect FAIL**

Run: `go test ./internal/transform -run TestStreamCost`

- [ ] **Step 3: Implement** per the Interfaces block. Keep held state in `state.Custom` under codec-specific keys, following the existing `"usage"` key pattern.

- [ ] **Step 4: Run, expect PASS**, then `go test ./internal/transform` (existing stream tests must stay green)

- [ ] **Step 5: Commit**

```bash
git add backend/internal/transform/
git commit -m "feat(usage): hold stream usage frame and attach billed cost"
```

---

### Task 4: Pipeline emits the cost; gateway wires streams

**Files:**
- Modify: `backend/internal/pipeline/pipeline.go:954-956` (direct-path gate)
- Modify: `backend/internal/pipeline/pipeline.go:~1453` (`pumpStream`, after `p.budgetConfirm(scope, cost)`)
- Modify: `backend/internal/gateway/handlers.go:485` (stream state creation) and the stream loop `~571-610`
- Test: `backend/internal/pipeline/billed_cost_stream_test.go`, `backend/internal/gateway/usage_cost_stream_test.go`

**Interfaces:**
- Consumes: `core.ChunkBilledCost` (Task 1); `StreamState.ShowBilledCost` and codec behaviour (Task 3); `req.Metadata.TokenMetered`.

- [ ] **Step 1: Write the failing pipeline tests** in `billed_cost_stream_test.go`, using the fake stream connector pattern from `upstream_cost_test.go`:
  - `TestPumpStream_EmitsBilledCost`: PAYG request; the last chunk before the channel closes is `ChunkBilledCost` with `Usage.BilledCostMicros` equal to the recorded `usage_records.cost_micros`.
  - `TestPumpStream_TokenMeteredNoBilledCost`: `TokenMetered=true` → no `ChunkBilledCost`.
  - `TestStream_PAYGSkipsDirectPath`: PAYG, same-dialect, no tools, `DirectStreamable` connector → `StreamResult.DirectBody == nil`, `Chunks != nil`. With `TokenMetered=true` the direct path is still taken.

- [ ] **Step 2: Run, expect FAIL**

Run: `go test ./internal/pipeline -run 'TestPumpStream_|TestStream_PAYG'`

- [ ] **Step 3: Implement** in the pipeline:
  - Add `req.Metadata.TokenMetered &&` to the direct-path condition at line 954 so only token-package streams use it. Extend the comment block above it with one sentence on why (PAYG needs the re-rendered usage frame for `usage.cost`).
  - In `pumpStream`, after `p.budgetConfirm(scope, cost)` on the clean-completion branch, when `!req.Metadata.TokenMetered`, send `core.StreamChunk{Type: core.ChunkBilledCost, Usage: &core.Usage{BilledCostMicros: cost}}` using the same `select { case out <- …: case <-ctx.Done(): }` shape as the synthetic usage send above it.

- [ ] **Step 4: Run, expect PASS**

- [ ] **Step 5: Write the failing gateway tests** in `gateway/usage_cost_stream_test.go`, with a PAYG key and a fake streaming upstream:
  - `TestStreamCost_EndToEnd_OpenAI`: client sends `stream_options.include_usage`; the final usage event has `cost` equal to the recorded cost.
  - `TestStreamCost_EndToEnd_Anthropic`: `/v1/messages` stream; terminal `message_delta` has `cost`.
  - `TestStreamCost_EndToEnd_TokenPackage`: same stream on a token-package key; no `"cost"` anywhere in the body.
  - `TestStreamCost_Bansos` (Review Focus 1): bansos notice set; body order is notice text, `finish_reason`, usage with `cost`, `[DONE]`.
  - `TestCost_UpstreamCostNeverLeaks` (Review Focus 5): upstream returns `usage.cost: 9.99` (stream and unary); client `cost` equals the billed amount and the body never contains `9.99`.

- [ ] **Step 6: Run, expect FAIL**

Run: `go test ./internal/gateway -run 'TestStreamCost_|TestCost_Upstream'`

- [ ] **Step 7: Implement** in `streamChat`:
  - At `handlers.go:485` set `state.ShowBilledCost = !opts.TokenMetered`. If `opts` is not in scope there, pass the flag in the way `notice` is passed.
  - In the stream loop, before the bansos `deferredFinish` check, route `chunk.Type == core.ChunkBilledCost` straight to `streamCodec.RenderStreamChunk(chunk, state)` (it emits nothing) and `continue`, so it bypasses the sanitizer and does not count toward `chunkCount`.

- [ ] **Step 8: Run, expect PASS**, then the three packages one at a time: `go test ./internal/transform`, `go test ./internal/pipeline`, `go test ./internal/gateway`

- [ ] **Step 9: Commit**

```bash
git add backend/internal/pipeline/ backend/internal/gateway/
git commit -m "feat(usage): stream billed cost to PAYG clients"
```

---

### Task 5: Full suite and docs

**Files:**
- Modify: `docs/` page that documents the API response format (find with `grep -rl "prompt_tokens" docs/`), adding a short `usage.cost` section: USD, billed price, PAYG keys only, provisional for Subrouter-backed models, `0` on response-cache hits.
- Modify: `CHANGELOG.md` (Unreleased): one line.

- [ ] **Step 1: Run the full backend suite once**

Run: `go test -p 1 ./internal/...`
Expected: `ok` for every package. Fix any test that pinned the old `renderAntUsage` type or the old OpenAI stream order.

- [ ] **Step 2: Write the docs section and changelog line** (no em dash, per antislop R-02)

- [ ] **Step 3: Commit**

```bash
git add docs/ CHANGELOG.md
git commit -m "docs: document usage.cost in model responses"
```
