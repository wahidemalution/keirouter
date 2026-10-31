package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUsageRecordRoundTripsReconcileFields(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	require.NoError(t, db.Usage().Record(ctx, UsageRecord{
		ID: "u1", TenantID: DefaultTenantID, Provider: "custom-openai-subrouter", Model: "deepseek-v4.1-flash",
		Status: "success", UpstreamRequestID: "20261031145028472218319QeMWIGBK", ReconcileStatus: "pending",
		CreatedAt: time.Now().UTC(),
	}))

	var id, status string
	q := db.rebind(`SELECT upstream_request_id, reconcile_status FROM usage_records WHERE id = ?`)
	require.NoError(t, db.sql.QueryRowContext(ctx, q, "u1").Scan(&id, &status))
	require.Equal(t, "20261031145028472218319QeMWIGBK", id)
	require.Equal(t, "pending", status)
}

func seedPending(t *testing.T, db *DB, id string, at time.Time, status string) {
	t.Helper()
	require.NoError(t, db.Usage().Record(context.Background(), UsageRecord{
		ID: id, TenantID: DefaultTenantID, APIKeyID: "k1", Provider: "custom-openai-subrouter",
		Model: "deepseek-v4.1-flash", Status: "success", ReconcileStatus: status, ChainID: "c1",
		PromptTokens: 872, CompletionTokens: 515, CostNanos: 150000, UpstreamCostNanos: 100000,
		OutputCostNanos: 150000, CreatedAt: at,
	}))
}

func TestListPendingReconcileOldestFirstWithinWindow(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 31, 15, 0, 0, 0, time.UTC)
	seedPending(t, db, "old", now.Add(-2*time.Hour), "pending")
	seedPending(t, db, "b", now.Add(-time.Minute), "pending")
	seedPending(t, db, "a", now.Add(-30*time.Minute), "pending")
	seedPending(t, db, "done", now.Add(-5*time.Minute), "reconciled")

	rows, err := db.Usage().ListPendingReconcile(ctx, "custom-openai-subrouter", now.Add(-time.Hour), 10)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, "a", rows[0].ID)
	require.Equal(t, "b", rows[1].ID)
	require.Equal(t, int64(150000), rows[0].CostNanos)
	require.Equal(t, int64(100000), rows[0].UpstreamCostNanos)
	require.Equal(t, 872, rows[0].PromptTokens)
	require.Equal(t, "c1", rows[0].ChainID)
	require.Equal(t, "k1", rows[0].APIKeyID)

	limited, err := db.Usage().ListPendingReconcile(ctx, "custom-openai-subrouter", now.Add(-time.Hour), 1)
	require.NoError(t, err)
	require.Len(t, limited, 1)
}

func TestSettleReconciledOnlyPending(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedPending(t, db, "r1", time.Now().UTC(), "pending")
	s := ReconcileSettlement{ID: "r1", Status: "reconciled", CostNanos: 51000, UpstreamCostNanos: 34000, OutputCostNanos: 51000}

	applied, err := db.Usage().SettleReconciled(ctx, []ReconcileSettlement{s})
	require.NoError(t, err)
	require.Equal(t, []string{"r1"}, applied)

	s.CostNanos = 999999
	applied, err = db.Usage().SettleReconciled(ctx, []ReconcileSettlement{s})
	require.NoError(t, err)
	require.Empty(t, applied)

	var cost, costMicros, up, upMicros int64
	var status, source, kind string
	q := db.rebind(`SELECT cost_nanos, cost_micros, upstream_cost_nanos, upstream_cost_micros, reconcile_status, pricing_source, pricing_match_kind FROM usage_records WHERE id = ?`)
	require.NoError(t, db.sql.QueryRowContext(ctx, q, "r1").Scan(&cost, &costMicros, &up, &upMicros, &status, &source, &kind))
	require.Equal(t, int64(51000), cost)
	require.Equal(t, int64(51), costMicros)
	require.Equal(t, int64(34000), up)
	require.Equal(t, int64(34), upMicros)
	require.Equal(t, "reconciled", status)
	require.Equal(t, "upstream_log", source)
	require.Equal(t, "upstream_log", kind)
}

func TestExpirePendingReconcile(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	seedPending(t, db, "stale", now.Add(-61*time.Minute), "pending")
	seedPending(t, db, "fresh", now.Add(-time.Minute), "pending")

	n, err := db.Usage().ExpirePendingReconcile(ctx, "custom-openai-subrouter", now.Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, int64(1), n)

	var stale, fresh string
	require.NoError(t, db.sql.QueryRowContext(ctx, db.rebind(`SELECT reconcile_status FROM usage_records WHERE id = ?`), "stale").Scan(&stale))
	require.NoError(t, db.sql.QueryRowContext(ctx, db.rebind(`SELECT reconcile_status FROM usage_records WHERE id = ?`), "fresh").Scan(&fresh))
	require.Equal(t, "unverified", stale)
	require.Equal(t, "pending", fresh)
}

func TestInsertCostLookupOrphansIdempotent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	o := CostLookupOrphan{Provider: "custom-openai-subrouter", RequestID: "20261031145059384885689jMgnd0ql",
		Model: "deepseek-v4.1-flash", PromptTokens: 872, CompletionTokens: 515, Quota: 17, CreatedAt: time.Now().UTC()}

	n, err := db.Usage().InsertCostLookupOrphans(ctx, []CostLookupOrphan{o})
	require.NoError(t, err)
	require.Equal(t, 1, n)
	n, err = db.Usage().InsertCostLookupOrphans(ctx, []CostLookupOrphan{o})
	require.NoError(t, err)
	require.Equal(t, 0, n)
}

func TestTryAdvisoryLockSQLiteAlwaysGranted(t *testing.T) {
	db := newTestDB(t)
	release, ok, err := db.TryAdvisoryLock(context.Background(), 0x4b52434c)
	require.NoError(t, err)
	require.True(t, ok)
	release()
}

func TestSettleStoresLogRequestIDAndKnownIDs(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedPending(t, db, "r1", time.Now().UTC(), "pending")

	_, err := db.Usage().SettleReconciled(ctx, []ReconcileSettlement{{ID: "r1", Status: "reconciled",
		UpstreamRequestID: "20261031145059384885689jMgnd0ql", CostNanos: 51000, UpstreamCostNanos: 34000}})
	require.NoError(t, err)

	known, err := db.Usage().KnownUpstreamRequestIDs(ctx, "custom-openai-subrouter",
		[]string{"20261031145059384885689jMgnd0ql", "unseen"})
	require.NoError(t, err)
	require.Equal(t, map[string]bool{"20261031145059384885689jMgnd0ql": true}, known)

	known, err = db.Usage().KnownUpstreamRequestIDs(ctx, "other-provider", []string{"20261031145059384885689jMgnd0ql"})
	require.NoError(t, err)
	require.Empty(t, known)
}

func TestReconcileDaySummary(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	day := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	rec := func(id, status, chain string, upstream int64, at time.Time) {
		require.NoError(t, db.Usage().Record(ctx, UsageRecord{ID: id, TenantID: DefaultTenantID, Provider: "sub",
			Model: "deepseek-v4.1-flash", Status: "success", ReconcileStatus: status, ChainID: chain,
			UpstreamCostNanos: upstream, CreatedAt: at}))
	}
	rec("a", "reconciled", "c1", 34000, day.Add(time.Hour))
	rec("b", "over_ceiling", "c1", 2000000, day.Add(2*time.Hour))
	rec("c", "over_ceiling", "c2", 1000, day.Add(3*time.Hour))
	rec("d", "unverified", "c1", 100000, day.Add(4*time.Hour))
	rec("e", "pending", "c1", 100000, day.Add(5*time.Hour))
	rec("next-day", "reconciled", "c1", 999, day.Add(25*time.Hour))
	_, err := db.Usage().InsertCostLookupOrphans(ctx, []CostLookupOrphan{
		{Provider: "sub", RequestID: "o1", Quota: 17, CreatedAt: day.Add(time.Hour)},
		{Provider: "sub", RequestID: "o2", Quota: 3, CreatedAt: day.Add(-time.Hour)},
	})
	require.NoError(t, err)

	got, err := db.Usage().ReconcileDaySummary(ctx, "sub", day, day.Add(24*time.Hour))
	require.NoError(t, err)
	require.Equal(t, int64(34000+2000000+1000+100000+100000), got.UpstreamCostNanos)
	require.Equal(t, 1, got.Pending)
	require.Equal(t, 3, got.Reconciled, "over_ceiling rows are reconciled too")
	require.Equal(t, 1, got.Unverified)
	require.Equal(t, []OverCeilingGroup{{ChainID: "c1", Model: "deepseek-v4.1-flash", Count: 1}, {ChainID: "c2", Model: "deepseek-v4.1-flash", Count: 1}}, got.OverCeiling)
	require.Equal(t, 1, got.OrphanCount)
	require.Equal(t, int64(17), got.OrphanQuota)
}
