package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/mydisha/keirouter/backend/internal/budget"
	"github.com/mydisha/keirouter/backend/internal/config"
	"github.com/mydisha/keirouter/backend/internal/core"
	"github.com/mydisha/keirouter/backend/internal/dispatch"
	"github.com/mydisha/keirouter/backend/internal/store"
	"github.com/stretchr/testify/require"
)

type chatOnlyVisionConnector struct {
	request *core.ChatRequest
}

func (c *chatOnlyVisionConnector) ID() string            { return "vision-chat" }
func (c *chatOnlyVisionConnector) Dialect() core.Dialect { return core.DialectAnthropic }
func (c *chatOnlyVisionConnector) Chat(_ context.Context, req *core.ChatRequest, _ core.Credentials) (*core.ChatResponse, error) {
	c.request = req
	return &core.ChatResponse{
		Model: req.Model,
		Message: core.Message{
			Role:    core.RoleAssistant,
			Content: []core.ContentPart{{Type: core.PartText, Text: "a cat"}},
		},
		Usage: core.Usage{TotalTokens: 12},
	}, nil
}
func (c *chatOnlyVisionConnector) Stream(context.Context, *core.ChatRequest, core.Credentials, core.StreamConfig) (<-chan core.StreamChunk, error) {
	ch := make(chan core.StreamChunk)
	close(ch)
	return ch, nil
}

func TestUnderstandImageFallsBackToCanonicalChat(t *testing.T) {
	conn := &chatOnlyVisionConnector{}
	resp, err := understandImage(context.Background(), dispatch.Attempt{
		Target: dispatch.Target{Provider: "anthropic", Model: "claude-vision"},
		Conn:   conn,
	}, &core.ImageUnderstandingRequest{
		Model:     "claude-vision",
		Prompt:    "describe",
		Images:    []string{"data:image/png;base64,abc123", "https://example.com/cat.jpg"},
		MaxTokens: 321,
	})
	require.NoError(t, err)
	require.Equal(t, "a cat", resp.Text)
	require.Equal(t, 12, resp.Usage.TotalTokens)
	require.NotNil(t, conn.request.MaxTokens)
	require.Equal(t, 321, *conn.request.MaxTokens)
	require.Len(t, conn.request.Messages[0].Content, 3)

	inline := conn.request.Messages[0].Content[1].Media
	require.Equal(t, "image/png", inline.MIMEType)
	require.Equal(t, "abc123", inline.Data)

	remote := conn.request.Messages[0].Content[2].Media
	require.Equal(t, "https://example.com/cat.jpg", remote.URL)
}

type imageOnlyConnector struct{ chatOnlyVisionConnector }

func (c *imageOnlyConnector) ID() string { return "openai" }
func (c *imageOnlyConnector) GenerateImage(context.Context, *core.ImageRequest, core.Credentials) (*core.ImageResponse, error) {
	return &core.ImageResponse{Data: []core.ImageData{{B64JSON: "aGk="}}}, nil
}

// A caller that already reserved the whole balance must not be refused by the
// preflight check, which counts that same reservation as spent.
func TestGenerateImageReservedSkipsBudgetCheck(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: ":memory:"}, t.TempDir())
	require.NoError(t, err)
	require.NoError(t, db.Migrate(ctx))
	require.NoError(t, db.Tenants().EnsureDefault(ctx))
	t.Cleanup(func() { _ = db.Close() })
	now := time.Now()
	require.NoError(t, db.Budgets().Create(ctx, store.Budget{
		ID: "b-img", TenantID: store.DefaultTenantID, ScopeKind: store.ScopeAPIKey, ScopeID: "key-img",
		LimitMicros: 100_000, Period: "total", HardCutoff: true, CreatedAt: now, UpdatedAt: now,
	}))
	eng := budget.New(db.Budgets(), db.Usage())
	t.Cleanup(eng.Close)
	scope := budget.Scope{TenantID: store.DefaultTenantID, APIKeyID: "key-img"}
	res, err := eng.Reserve(ctx, scope, budget.Estimate{Micros: 100_000})
	require.NoError(t, err)
	defer eng.Release(budget.Scope{TenantID: scope.TenantID, APIKeyID: scope.APIKeyID, Reservation: res.ID})

	p := New(Deps{Dispatcher: newPlannerDispatcherWithConnector(t, &imageOnlyConnector{}, "acc-1"), Budget: eng})
	opts := MediaOptions{
		Targets:  []dispatch.Target{{Provider: "openai", Model: "gpt-image-1"}},
		TenantID: store.DefaultTenantID, APIKeyID: "key-img",
	}
	_, _, err = p.GenerateImage(ctx, &core.ImageRequest{Prompt: "cat"}, opts)
	require.Error(t, err, "without Reserved the held balance blocks the request")

	opts.Reserved = true
	resp, provider, err := p.GenerateImage(ctx, &core.ImageRequest{Prompt: "cat"}, opts)
	require.NoError(t, err)
	require.Equal(t, "openai", provider)
	require.Len(t, resp.Data, 1)
}
