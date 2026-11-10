package pipeline

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/budget"
	"github.com/mydisha/keirouter/backend/internal/config"
	"github.com/mydisha/keirouter/backend/internal/core"
	"github.com/mydisha/keirouter/backend/internal/dispatch"
	"github.com/mydisha/keirouter/backend/internal/store"
)

// burningConnector fails its first call after spending firstUsage tokens and
// asks for a repair retry, then answers. It records each call's output cap.
type burningConnector struct {
	firstUsage int
	mu         sync.Mutex
	maxTokens  []int
}

func (*burningConnector) ID() string            { return "openai" }
func (*burningConnector) Dialect() core.Dialect { return core.DialectOpenAI }
func (c *burningConnector) Chat(_ context.Context, req *core.ChatRequest, _ core.Credentials) (*core.ChatResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.maxTokens = append(c.maxTokens, *req.MaxTokens)
	if len(c.maxTokens) == 1 {
		return nil, &core.ProviderError{
			Kind: core.ErrUpstream, Scope: core.FailureScopeRequest, Message: "incomplete response",
			RetrySystemInstruction: "complete the previous response",
			AttemptUsage: &core.Usage{
				PromptTokens: 0, CompletionTokens: c.firstUsage, TotalTokens: c.firstUsage,
				Source: core.UsageSourceProvider,
			},
		}
	}
	return &core.ChatResponse{
		Model:        req.Model,
		Message:      core.Message{Role: core.RoleAssistant, Content: []core.ContentPart{{Type: core.PartText, Text: "done"}}},
		FinishReason: core.FinishStop,
		Usage:        core.Usage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7, Source: core.UsageSourceProvider},
	}, nil
}
func (*burningConnector) Stream(context.Context, *core.ChatRequest, core.Credentials, core.StreamConfig) (<-chan core.StreamChunk, error) {
	ch := make(chan core.StreamChunk)
	close(ch)
	return ch, nil
}

func tokenBudgetEngine(t *testing.T, keyID string, limit int64) *budget.Engine {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: ":memory:"}, t.TempDir())
	require.NoError(t, err)
	require.NoError(t, db.Migrate(ctx))
	require.NoError(t, db.Tenants().EnsureDefault(ctx))
	t.Cleanup(func() { _ = db.Close() })
	now := time.Now()
	require.NoError(t, db.Budgets().Create(ctx, store.Budget{
		ID: "b-" + keyID, TenantID: store.DefaultTenantID, ScopeKind: store.ScopeAPIKey, ScopeID: keyID,
		LimitTokens: limit, Period: "total", HardCutoff: true, CreatedAt: now, UpdatedAt: now,
	}))
	return budget.New(db.Budgets(), db.Usage())
}

func runBurningChat(t *testing.T, firstUsage int) (*burningConnector, error) {
	t.Helper()
	conn := &burningConnector{firstUsage: firstUsage}
	d := newPlannerDispatcherWithConnector(t, conn, "acc-1", "acc-2")
	p := New(Deps{Dispatcher: d, Budget: tokenBudgetEngine(t, "key-tok", 1_000_000)})
	maxTokens := 1_000
	req := &core.ChatRequest{
		Model:     "gpt-4o",
		MaxTokens: &maxTokens,
		Messages:  []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{{Type: core.PartText, Text: "hello"}}}},
		Metadata:  core.RequestMetadata{TenantID: store.DefaultTenantID, APIKeyID: "key-tok"},
	}
	_, err := p.Chat(context.Background(), req, Options{
		Targets:      []dispatch.Target{{Provider: "openai", Model: "gpt-4o"}},
		TokenMetered: true,
	})
	return conn, err
}

func TestTokenRetryOutputCapShrinksByEarlierAttempts(t *testing.T) {
	conn, err := runBurningChat(t, 600)
	require.NoError(t, err)
	conn.mu.Lock()
	defer conn.mu.Unlock()
	require.Equal(t, []int{1_000, 400}, conn.maxTokens,
		"the retry may spend only what the hold has left after the first attempt")
}

func TestTokenRetryStopsWhenHoldIsSpent(t *testing.T) {
	conn, err := runBurningChat(t, 1_000)
	require.Error(t, err)
	conn.mu.Lock()
	defer conn.mu.Unlock()
	require.Len(t, conn.maxTokens, 1, "no retry once the hold has no output left")
}
