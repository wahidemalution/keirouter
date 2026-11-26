# Billed cost in model responses (`usage.cost`)

Date: 2026-11-26
Status: approved 2026-11-26 (single `cost` number, no breakdown; response
cache hits stay free, so `usage.cost` is 0 for them)

## Intent

PAYG customers who resell KeiRouter want the price KeiRouter charged for each
request, so they can apply their own markup when billing their customers.
Today responses carry token counts only; the billed cost is recorded in
`usage_records.cost_nanos` but never returned.

## Goals

- Every model response to a PAYG key carries `usage.cost`: the USD amount
  KeiRouter billed for that request (the same value stored as `CostNanos`).
- Works for streaming and non-streaming.
- Field name and unit follow OpenRouter's `usage.cost` (USD, number), so
  existing reseller tooling and SDKs read it without changes.

## Non-goals

- No response header (decided: body only).
- No per-request lookup endpoint (possible follow-up for settled Subrouter
  cost).
- Real-time model price API and the Surplus quoted-vs-charged gap (separate
  project, "B").
- Gemini and Ollama output formats (claude's call; add later if resellers
  use them).

## Anti-goals

- Never expose upstream cost (`UpstreamCostNanos`, Surplus
  `buyer_cost_micro`, inferhub/OpenRouter upstream `usage.cost`) or the
  markup. Only the customer-facing billed price leaves the router.
- Never emit `usage.cost` to token-package keys (`TokenMetered`), which pay
  in token quota and must not see USD figures.

## Constraints (from the code today)

- Billed cost: `meter.Record` → `CostNanos` (internal/meter/meter.go:356).
  Unary result carries `Result.CostMicros`; streams carry
  `DirectStats.CostMicros`.
- Non-streaming: cost is known before the body is written
  (internal/gateway/handlers.go:299-332).
- Streaming, translated path: cost is recorded in `pumpStream` before
  `close(out)` (internal/pipeline/pipeline.go:1453), before the gateway
  writes terminal frames (handlers.go:643-684).
  - Anthropic: terminal `message_delta` with usage is already emitted in
    `RenderStreamDone`, after cost is known.
  - OpenAI Chat and Responses: the usage chunk / `response.completed` is
    emitted when upstream usage arrives, before cost is known. It must be
    held back until the cost is recorded.
- Streaming, direct zero-copy path (pipeline.go:954-1089): cost is computed
  after the copy. The terminal frame must be held back, priced, and
  rewritten, following the `noticeInjectingWriter` precedent
  (handlers.go:812-878).
- Upstream `usage.cost` from OpenRouter/inferhub may already be present in
  raw upstream bytes on the direct path; it must be replaced by the billed
  cost, never passed through.
- Subrouter (cost-lookup) steps: the request is billed at the ceiling price
  and reconciled by the sweeper within seconds to an hour. `usage.cost` is
  that provisional ceiling, usually at or above the settled amount.
- Response-cache hits are billed 0, so `usage.cost` is 0.

## Approaches considered

1. **`usage.cost` in body for stream and non-stream (picked).** One
   mechanism for every request, OpenRouter-compatible. Costs: hold back the
   final usage frame on OpenAI/Responses/direct streams.
2. Header `X-KeiRouter-Cost`. Rejected: headers are sent before streaming
   output, so streams (most client traffic) would get no cost. The user also
   chose body only.
3. Lookup endpoint by request ID. Deferred: extra call per request; its
   advantage is the settled Subrouter cost, which can be added later.

## Decisions

- Always on for PAYG keys; no per-key toggle (claude's call: the number is
  the customer's own bill, nothing is leaked).
- Unit USD, number, from the micros the pipeline already returns
  (`Result.CostMicros`, `recordWithTTFT`). Revised during planning from
  nanos: plumbing nanos would touch every budget path, and micros round by
  at most $0.0000005 per request (claude's call).
- PAYG streams skip the direct zero-copy path so the cost can ride on the
  re-rendered final usage frame and raw upstream `usage.cost` can never
  reach the client. Requests with tools already take the channel path, so
  most agent traffic is unaffected (claude's call).
- Streaming OpenAI Chat: cost rides on the existing usage chunk, so it
  appears only when that chunk is emitted today (client sent
  `stream_options.include_usage`, or the router already forces it)
  (claude's call; builder confirms current behavior).
- Anthropic format: `usage.cost` inside the terminal `message_delta` usage
  and the non-stream `usage` object.
- Responses format: `response.usage.cost` in `response.completed` and the
  non-stream response.

## Left to the builder

- How the held-back usage frame waits for the cost (result field vs. a new
  internal chunk type before `close(out)`).
- Whether the direct path holds back the terminal frame or PAYG streams
  that need cost fall back to the channel path, choosing whichever keeps
  latency and memory flat.
- Tests: per format, stream and non-stream, PAYG shows cost, token-package
  does not, upstream `usage.cost` never leaks, cache hit shows 0.
