package pipeline

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/config"
	"github.com/mydisha/keirouter/backend/internal/core"
	"github.com/mydisha/keirouter/backend/internal/crypto"
	"github.com/mydisha/keirouter/backend/internal/dispatch"
	"github.com/mydisha/keirouter/backend/internal/store"
	"github.com/mydisha/keirouter/backend/internal/vault"
)

// imageThenOptionsConnector rejects an image, then stream_options, then
// streams. It keeps every stream request.
type imageThenOptionsConnector struct {
	dialect core.Dialect
	mu      sync.Mutex
	reqs    []*core.ChatRequest
}

func (*imageThenOptionsConnector) ID() string { return "cloudflare-ai" }
func (c *imageThenOptionsConnector) Dialect() core.Dialect {
	return c.dialect
}
func (*imageThenOptionsConnector) Chat(context.Context, *core.ChatRequest, core.Credentials) (*core.ChatResponse, error) {
	return nil, nil
}
func (c *imageThenOptionsConnector) Stream(_ context.Context, req *core.ChatRequest, _ core.Credentials, _ core.StreamConfig) (<-chan core.StreamChunk, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reqs = append(c.reqs, req)
	for _, m := range req.Messages {
		for _, p := range m.Content {
			if p.Type == core.PartImage {
				return nil, &core.ProviderError{Kind: core.ErrBadRequest, StatusCode: 400, Message: "image_url is not supported"}
			}
		}
	}
	if req.UpstreamUsage {
		return nil, &core.ProviderError{Kind: core.ErrBadRequest, StatusCode: 400, Message: "stream_options is not supported"}
	}
	ch := make(chan core.StreamChunk, 2)
	ch <- core.StreamChunk{Type: core.ChunkText, Delta: "ok"}
	ch <- core.StreamChunk{Type: core.ChunkFinish, FinishReason: core.FinishStop}
	close(ch)
	return ch, nil
}

func dispatcherFor(t *testing.T, provider string, conn core.Connector) *dispatch.Dispatcher {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: ":memory:"}, t.TempDir())
	require.NoError(t, err)
	require.NoError(t, db.Migrate(ctx))
	require.NoError(t, db.Tenants().EnsureDefault(ctx))
	t.Cleanup(func() { _ = db.Close() })
	key, err := crypto.GenerateMasterKey()
	require.NoError(t, err)
	sealer, err := crypto.NewSealer(key)
	require.NoError(t, err)
	secrets := vault.New(sealer)
	now := time.Now()
	account := store.Account{
		ID: "acc-1", TenantID: store.DefaultTenantID, Provider: provider, Label: "acc-1",
		AuthKind: store.AuthAPIKey, Priority: 10, Metadata: "{}", CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, secrets.Seal(&account, vault.NewSecret{APIKey: "sk-test"}))
	require.NoError(t, db.Accounts().Create(ctx, account))
	d := dispatch.New(plannerConnectorSource{conn: conn}, db.Accounts(), secrets)
	d.SetRoutingSource(db.Routing())
	return d
}

// The stream_options retry resends the request the image retry stripped.
func TestTokenStreamOptionsRetryKeepsStrippedImages(t *testing.T) {
	conn := &imageThenOptionsConnector{dialect: core.DialectOpenAI}
	p := New(Deps{Dispatcher: dispatcherFor(t, "cloudflare-ai", conn), Budget: tokenBudgetEngine(t, "key-tok", 1_000_000)})
	maxTokens := 1000
	req := &core.ChatRequest{
		Model: "@cf/example/new-model", Stream: true, MaxTokens: &maxTokens,
		Messages: []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{
			{Type: core.PartText, Text: "what is this"},
			{Type: core.PartImage, Media: &core.MediaPayload{URL: "https://example.com/a.png"}},
		}}},
		Metadata: core.RequestMetadata{TenantID: store.DefaultTenantID, APIKeyID: "key-tok"},
	}
	sr, err := p.Stream(context.Background(), req, Options{
		Targets:      []dispatch.Target{{Provider: "cloudflare-ai", Model: "@cf/example/new-model"}},
		TokenMetered: true,
	})
	require.NoError(t, err)
	for range sr.Chunks {
	}

	conn.mu.Lock()
	defer conn.mu.Unlock()
	require.Len(t, conn.reqs, 3, "image retry, then stream_options retry")
	last := conn.reqs[2]
	require.False(t, last.UpstreamUsage)
	for _, part := range last.Messages[0].Content {
		require.NotEqual(t, core.PartImage, part.Type, "the stripped request is resent")
	}
}

// A connector that injects stream_options itself gains nothing from a resend.
func TestTokenStreamOptionsRetrySkipsSelfInjectingConnectors(t *testing.T) {
	req := &core.ChatRequest{UpstreamUsage: true, Metadata: core.RequestMetadata{TokenMetered: true}}
	bad := &core.ProviderError{Kind: core.ErrBadRequest, StatusCode: 400}
	openai := dispatch.Attempt{Conn: &imageThenOptionsConnector{dialect: core.DialectOpenAI}}
	sagemaker := dispatch.Attempt{Conn: &imageThenOptionsConnector{dialect: core.DialectSageMaker}}
	require.True(t, retryWithoutStreamUsage(bad, req, openai))
	require.False(t, retryWithoutStreamUsage(bad, req, sagemaker))
}

func TestCompletionCharsCountReasoningFields(t *testing.T) {
	for _, delta := range []string{
		`{"reasoning_content":"abcd"}`,
		`{"reasoning":"abcd"}`,
		`{"reasoning_details":[{"type":"reasoning.text","text":"ab"},{"type":"reasoning.text","text":"cd"}]}`,
	} {
		raw := `data: {"choices":[{"index":0,"delta":` + delta + `}]}` + "\n\n"
		require.Equal(t, 4, completionCharsFromStream([]byte(raw)), delta)
	}
}
