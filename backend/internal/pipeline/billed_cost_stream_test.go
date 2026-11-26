package pipeline

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/core"
	"github.com/mydisha/keirouter/backend/internal/dispatch"
	"github.com/mydisha/keirouter/backend/internal/meter"
	"github.com/mydisha/keirouter/backend/internal/store"
)

// usageConnector streams text, a finish and a provider usage frame; it also
// offers the raw zero-copy stream.
type usageConnector struct{ plannerConnector }

func (usageConnector) Stream(context.Context, *core.ChatRequest, core.Credentials, core.StreamConfig) (<-chan core.StreamChunk, error) {
	return streamOf(
		core.StreamChunk{Type: core.ChunkText, Delta: "hello"},
		core.StreamChunk{Type: core.ChunkFinish, FinishReason: core.FinishStop},
		core.StreamChunk{Type: core.ChunkUsage, Usage: &core.Usage{PromptTokens: 1200, CompletionTokens: 85, TotalTokens: 1285}},
	), nil
}

func (usageConnector) StreamRaw(context.Context, *core.ChatRequest, core.Credentials, core.StreamConfig) (io.ReadCloser, http.Header, error) {
	body := `data: {"choices":[{"index":0,"delta":{"content":"hello"}}]}` + "\n\n" + "data: [DONE]\n\n"
	return io.NopCloser(strings.NewReader(body)), http.Header{"Content-Type": {"text/event-stream"}}, nil
}

func newBilledCostPipeline(t *testing.T) (*Pipeline, *[]store.UsageRecord) {
	t.Helper()
	m := meter.New(newPipelineTestDB(t).Usage(), nil,
		map[string]meter.Price{"openai/gpt-4o": {InputPerM: 10, OutputPerM: 30}})
	var recs []store.UsageRecord
	m.SetRecordObserver(func(r store.UsageRecord) { recs = append(recs, r) })
	d := newPlannerDispatcherWithConnector(t, usageConnector{}, "acc-1")
	return New(Deps{Dispatcher: d, Meter: m}), &recs
}

// translatedRequest takes the channel path whatever the key.
func translatedRequest() *core.ChatRequest {
	req := guardrailStreamRequest("hello")
	req.Metadata.SourceDialect = core.DialectAnthropic
	return req
}

// billedCostOpts routes to the priced target; the pipeline copies
// TokenMetered onto the request metadata.
func billedCostOpts(tokenMetered bool) Options {
	return Options{Targets: []dispatch.Target{{Provider: "openai", Model: "gpt-4o"}}, TokenMetered: tokenMetered}
}

func drain(t *testing.T, res *StreamResult) []core.StreamChunk {
	t.Helper()
	require.NotNil(t, res.Chunks)
	var out []core.StreamChunk
	for c := range res.Chunks {
		out = append(out, c)
	}
	return out
}

func TestPumpStream_EmitsBilledCost(t *testing.T) {
	p, recs := newBilledCostPipeline(t)
	res, err := p.Stream(context.Background(), translatedRequest(), billedCostOpts(false))
	require.NoError(t, err)
	chunks := drain(t, res)

	require.Len(t, *recs, 1)
	billed := (*recs)[0].CostMicros
	require.Positive(t, billed)

	last := chunks[len(chunks)-1]
	require.Equal(t, core.ChunkBilledCost, last.Type, "the cost is the last chunk before close")
	require.NotNil(t, last.Usage)
	require.Equal(t, billed, last.Usage.BilledCostMicros)
	require.Equal(t, (*recs)[0].PromptTokens, last.Usage.PromptTokens, "the chunk carries the recorded usage")
	require.Equal(t, (*recs)[0].CompletionTokens, last.Usage.CompletionTokens)
	for _, c := range chunks[:len(chunks)-1] {
		require.NotEqual(t, core.ChunkBilledCost, c.Type, "exactly one cost chunk")
	}
}

func TestPumpStream_TokenMeteredNoBilledCost(t *testing.T) {
	p, recs := newBilledCostPipeline(t)
	res, err := p.Stream(context.Background(), translatedRequest(), billedCostOpts(true))
	require.NoError(t, err)
	for _, c := range drain(t, res) {
		require.NotEqual(t, core.ChunkBilledCost, c.Type)
	}
	require.Len(t, *recs, 1)
}

func TestStream_PAYGSkipsDirectPath(t *testing.T) {
	p, _ := newBilledCostPipeline(t)
	req := guardrailStreamRequest("hello") // same dialect, no tools: direct-eligible
	res, err := p.Stream(context.Background(), req, billedCostOpts(false))
	require.NoError(t, err)
	require.Nil(t, res.DirectBody, "a PAYG stream needs the re-rendered usage frame for usage.cost")
	var sawCost bool
	for _, c := range drain(t, res) {
		sawCost = sawCost || c.Type == core.ChunkBilledCost
	}
	require.True(t, sawCost)
}

func TestStream_TokenMeteredKeepsDirectPath(t *testing.T) {
	p, _ := newBilledCostPipeline(t)
	res, err := p.Stream(context.Background(), guardrailStreamRequest("hello"), billedCostOpts(true))
	require.NoError(t, err)
	require.NotNil(t, res.DirectBody)
	_, _ = io.Copy(io.Discard, res.DirectBody)
	_ = res.DirectBody.Close()
	res.DirectCompleteFunc(nil)
}

// The channel path renders one choice and no logprobs, so a PAYG stream that
// asks for several choices or for logprobs keeps the direct path (and goes
// without usage.cost) rather than come back garbled.
func TestStream_PAYGRawOnlyParamsKeepDirectPath(t *testing.T) {
	for name, extra := range map[string]map[string]json.RawMessage{
		"n=2":          {"n": json.RawMessage(`2`)},
		"logprobs":     {"logprobs": json.RawMessage(`true`)},
		"top_logprobs": {"top_logprobs": json.RawMessage(`3`)},
	} {
		t.Run(name, func(t *testing.T) {
			p, _ := newBilledCostPipeline(t)
			req := guardrailStreamRequest("hello")
			req.Extra = extra
			res, err := p.Stream(context.Background(), req, billedCostOpts(false))
			require.NoError(t, err)
			require.NotNil(t, res.DirectBody)
			_, _ = io.Copy(io.Discard, res.DirectBody)
			_ = res.DirectBody.Close()
			res.DirectCompleteFunc(nil)
		})
	}
	t.Run("n=1 and logprobs=false stay on the channel path", func(t *testing.T) {
		p, _ := newBilledCostPipeline(t)
		req := guardrailStreamRequest("hello")
		req.Extra = map[string]json.RawMessage{"n": json.RawMessage(`1`), "logprobs": json.RawMessage(`false`)}
		res, err := p.Stream(context.Background(), req, billedCostOpts(false))
		require.NoError(t, err)
		require.Nil(t, res.DirectBody)
		drain(t, res)
	})
}
