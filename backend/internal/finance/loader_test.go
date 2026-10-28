package finance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/config"
	"github.com/mydisha/keirouter/backend/internal/store"
)

func newLoaderDB(t *testing.T) *store.DB {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: ":memory:"}, t.TempDir())
	require.NoError(t, err)
	require.NoError(t, db.Migrate(ctx))
	require.NoError(t, db.Tenants().EnsureDefault(ctx))
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestLoader_ReportProfit(t *testing.T) {
	db := newLoaderDB(t)
	ctx := context.Background()
	now := time.Now()
	require.NoError(t, db.Usage().RecordBatch(ctx, []store.UsageRecord{{
		ID: "u1", TenantID: store.DefaultTenantID, APIKeyID: "k1", Provider: "p1", Model: "m",
		Status: "success", CostNanos: 15e9, UpstreamCostNanos: 10e9,
		PricingStatus: "priced", PricingSource: "official", CreatedAt: now.Add(-time.Minute),
	}}))

	l := &Loader{DB: db, Settings: db.Settings().Get, InFlight: func() map[string]int64 { return map[string]int64{"p1": 2} }}
	rep, err := l.Report(ctx, "all")
	require.NoError(t, err)
	require.Equal(t, "all", rep.Range)
	require.InDelta(t, 5.0, rep.Overview.ProfitUSD, 1e-9)
	require.EqualValues(t, 2, rep.Overview.InFlight)

	_, err = l.Report(ctx, "x")
	require.True(t, errors.Is(err, ErrBadRange), "got %v", err)
}

func TestLoader_MarkupsGlobalFromSettings(t *testing.T) {
	db := newLoaderDB(t)
	ctx := context.Background()
	require.NoError(t, db.Settings().Set(ctx, "market_pricing_settings", `{"markup_percent":25}`))
	l := &Loader{DB: db, Settings: db.Settings().Get}
	m, err := l.Markups(ctx)
	require.NoError(t, err)
	require.Equal(t, 25.0, m.Global)
}
