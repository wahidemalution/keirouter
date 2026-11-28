package budget

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/meter"
	"github.com/mydisha/keirouter/backend/internal/store"
)

// A metered image event counts toward its key's budget like any chat row.
func TestRecordedImageEventCountsTowardBudget(t *testing.T) {
	db := openE2EStore(t)
	ctx := context.Background()
	now := time.Now()
	require.NoError(t, db.Budgets().Create(ctx, store.Budget{
		ID: "bk", TenantID: store.DefaultTenantID, ScopeKind: store.ScopeAPIKey, ScopeID: "k1",
		Period: "total", LimitMicros: 1 << 40, AlertPct: 80, HardCutoff: true, CreatedAt: now, UpdatedAt: now,
	}))
	eng := New(db.Budgets(), db.Usage())
	t.Cleanup(eng.Close)
	m := meter.New(db.Usage(), nil, nil)
	m.AddUsageTracker(eng)

	scope := Scope{TenantID: store.DefaultTenantID, ProjectID: "p1", APIKeyID: "k1"}
	before, err := eng.Check(ctx, scope)
	require.NoError(t, err)
	require.Zero(t, before.BudgetUsage[0].CostMicros)

	billed, err := m.Record(ctx, meter.Event{
		RequestID: "img-1", TenantID: store.DefaultTenantID, ProjectID: "p1", APIKeyID: "k1",
		Provider: "openai", Model: "gpt-image-1", ChainID: "c1",
		ImageCount: 2, ImageBillMicro: 8400, ImageCostMicro: 4200,
	})
	require.NoError(t, err)
	require.Equal(t, int64(16_800), billed)

	after, err := eng.Check(ctx, scope)
	require.NoError(t, err)
	require.Equal(t, int64(16_800), after.BudgetUsage[0].CostMicros)
	spent, _, err := db.Usage().SpendAndTokens(ctx, store.ScopeAPIKey, "k1", PeriodStart("total", now))
	require.NoError(t, err)
	require.Equal(t, int64(16_800), spent, "the usage table holds the image bill")
}
