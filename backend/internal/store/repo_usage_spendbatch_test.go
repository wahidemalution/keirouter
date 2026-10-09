package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestSpendAndTokensBatchMapsByScopeIndex guards against a regression where
// the UNION ALL batch query mapped results by row position. SQL does not
// guarantee UNION ALL preserves branch order, so a scope could receive another
// scope's spend. Each scope here has a distinct cost, and the result slice must
// preserve the input scope order.
func TestSpendAndTokensBatchMapsByScopeIndex(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	records := []UsageRecord{
		{ID: "b1", TenantID: DefaultTenantID, APIKeyID: "key-a", Provider: "openai", Model: "m", Status: "success", PromptTokens: 10, CompletionTokens: 5, CostNanos: 1_000_000, PricingStatus: "priced", PricingSource: "chain", CreatedAt: now},
		{ID: "b2", TenantID: DefaultTenantID, APIKeyID: "key-b", Provider: "openai", Model: "m", Status: "success", PromptTokens: 20, CompletionTokens: 5, CostNanos: 2_000_000, PricingStatus: "priced", PricingSource: "chain", CreatedAt: now},
		{ID: "b3", TenantID: DefaultTenantID, APIKeyID: "key-c", Provider: "openai", Model: "m", Status: "success", PromptTokens: 30, CompletionTokens: 5, CostNanos: 3_000_000, PricingStatus: "priced", PricingSource: "chain", CreatedAt: now},
	}
	require.NoError(t, db.Usage().RecordBatch(ctx, records))

	scopes := []SpendScope{
		{Kind: ScopeAPIKey, ScopeID: "key-a", Since: now.Add(-time.Hour)},
		{Kind: ScopeAPIKey, ScopeID: "key-b", Since: now.Add(-time.Hour)},
		{Kind: ScopeAPIKey, ScopeID: "key-c", Since: now.Add(-time.Hour)},
	}
	results, err := db.Usage().SpendAndTokensBatch(ctx, scopes)
	require.NoError(t, err)
	require.Len(t, results, 3)

	// Each batch result must match the known-correct single-scope query for the
	// same scope. A positional-mapping bug assigns another scope's values.
	for i, s := range scopes {
		wantCost, wantTokens, err := db.Usage().SpendAndTokens(ctx, s.Kind, s.ScopeID, s.Since)
		require.NoError(t, err)
		require.Equal(t, wantCost, results[i].CostMicros, "scope %s", s.ScopeID)
		require.Equal(t, wantTokens, results[i].Tokens, "scope %s", s.ScopeID)
	}

	// 1_000_000 nanos = 1000 micros; input order must be preserved.
	require.Equal(t, int64(1000), results[0].CostMicros)
	require.Equal(t, int64(2000), results[1].CostMicros)
	require.Equal(t, int64(3000), results[2].CostMicros)
	require.Equal(t, int64(15), results[0].Tokens)
	require.Equal(t, int64(25), results[1].Tokens)
	require.Equal(t, int64(35), results[2].Tokens)
}
