package pipeline

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/core"
	"github.com/mydisha/keirouter/backend/internal/dispatch"
	"github.com/mydisha/keirouter/backend/internal/meter"
	"github.com/mydisha/keirouter/backend/internal/store"
)

// postingConnector sends every call to url over HTTP and waits for the
// answer, so the request really reaches the upstream.
type postingConnector struct {
	plannerConnector
	url string
}

func (c postingConnector) post(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, strings.NewReader("{}"))
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return &core.ProviderError{Kind: core.ErrTimeout, Message: "upstream hung", Cause: err}
	}
	_ = resp.Body.Close()
	return &core.ProviderError{Kind: core.ErrUpstream, Message: "unexpected answer"}
}

func (c postingConnector) Chat(ctx context.Context, _ *core.ChatRequest, _ core.Credentials) (*core.ChatResponse, error) {
	return nil, c.post(ctx)
}

func (c postingConnector) Stream(ctx context.Context, _ *core.ChatRequest, _ core.Credentials, _ core.StreamConfig) (<-chan core.StreamChunk, error) {
	return nil, c.post(ctx)
}

// blockedConnector never reaches the upstream: it waits until its context
// ends, like a dial that hangs.
type blockedConnector struct{ plannerConnector }

func (blockedConnector) Chat(ctx context.Context, _ *core.ChatRequest, _ core.Credentials) (*core.ChatResponse, error) {
	<-ctx.Done()
	return nil, &core.ProviderError{Kind: core.ErrTimeout, Message: "dial hung", Cause: ctx.Err()}
}

func (blockedConnector) Stream(ctx context.Context, _ *core.ChatRequest, _ core.Credentials, _ core.StreamConfig) (<-chan core.StreamChunk, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// silentUpstream reads the request and never answers.
func silentUpstream(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func abandonedPipeline(t *testing.T, conn core.Connector) (*Pipeline, *[]store.UsageRecord) {
	t.Helper()
	m := meter.New(newPipelineTestDB(t).Usage(), nil, nil)
	var recs []store.UsageRecord
	m.SetRecordObserver(func(r store.UsageRecord) { recs = append(recs, r) })
	p := New(Deps{Dispatcher: newPlannerDispatcherWithConnector(t, conn, "acc-1"), Meter: m,
		RequestTimeout: 150 * time.Millisecond})
	return p, &recs
}

func abandonedRequest(stream bool) *core.ChatRequest {
	req := guardrailStreamRequest("hello")
	maxTokens := 500
	req.MaxTokens = &maxTokens
	req.Stream = stream
	return req
}

var abandonedTargets = []dispatch.Target{{Provider: "openai", Model: "gpt-4o"}}

func TestTokenUnaryDeadlineCharge(t *testing.T) {
	for name, c := range map[string]struct {
		conn       core.Connector
		completion int
	}{
		"sent":     {postingConnector{url: silentUpstream(t)}, 500},
		"not_sent": {blockedConnector{}, 0},
	} {
		t.Run(name, func(t *testing.T) {
			p, recs := abandonedPipeline(t, c.conn)
			_, err := p.Chat(context.Background(), abandonedRequest(false), Options{Targets: abandonedTargets, TokenMetered: true})
			require.Error(t, err)
			require.Len(t, *recs, 1)
			require.Equal(t, c.completion, (*recs)[0].CompletionTokens)
		})
	}
}

func TestTokenStreamCancelBeforeHeadersCharge(t *testing.T) {
	for name, c := range map[string]struct {
		conn       core.Connector
		completion int
	}{
		"sent":     {postingConnector{url: silentUpstream(t)}, 500},
		"not_sent": {blockedConnector{}, 0},
	} {
		t.Run(name, func(t *testing.T) {
			p, recs := abandonedPipeline(t, c.conn)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			time.AfterFunc(150*time.Millisecond, cancel)
			_, err := p.Stream(ctx, abandonedRequest(true), Options{Targets: abandonedTargets, TokenMetered: true})
			require.Error(t, err)
			require.Len(t, *recs, 1)
			require.Equal(t, "cancelled", (*recs)[0].Status)
			require.Equal(t, c.completion, (*recs)[0].CompletionTokens)
		})
	}
}
