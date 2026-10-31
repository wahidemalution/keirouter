package costlookup

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/store"
)

func TestBuildReportGap(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	day := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	require.NoError(t, h.db.Usage().Record(ctx, store.UsageRecord{ID: "a", TenantID: store.DefaultTenantID, Provider: provID,
		Model: "deepseek-v4.1-flash", Status: "success", ReconcileStatus: "reconciled", UpstreamCostNanos: 98_000_000,
		CreatedAt: day.Add(time.Hour)}))
	var gotStart, gotEnd int64
	stat := func(_ context.Context, start, end int64) (int64, error) {
		gotStart, gotEnd = start, end
		return 50_000, nil // $0.10
	}

	r, err := BuildReport(ctx, provID, day, stat, h.db.Usage())
	require.NoError(t, err)
	require.Equal(t, "2026-10-30", r.Day)
	require.Equal(t, day.Unix(), gotStart)
	require.Equal(t, day.Add(24*time.Hour).Unix()-1, gotEnd)
	require.InDelta(t, 0.10, r.SubrouterUSD, 1e-12)
	require.InDelta(t, 0.098, r.KeirouterUpstreamUSD, 1e-12)
	require.InDelta(t, 2.0, r.GapPct, 1e-9)
	require.True(t, r.Warn)
	require.Equal(t, 1, r.Reconciled)
}

func TestBuildReportNoWarnWithinOnePercent(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	day := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	require.NoError(t, h.db.Usage().Record(ctx, store.UsageRecord{ID: "a", TenantID: store.DefaultTenantID, Provider: provID,
		Model: "m", Status: "success", ReconcileStatus: "reconciled", UpstreamCostNanos: 99_500_000, CreatedAt: day.Add(time.Hour)}))
	r, err := BuildReport(ctx, provID, day, func(context.Context, int64, int64) (int64, error) { return 50_000, nil }, h.db.Usage())
	require.NoError(t, err)
	require.False(t, r.Warn)

	r, err = BuildReport(ctx, provID, day.Add(-24*time.Hour), func(context.Context, int64, int64) (int64, error) { return 0, nil }, h.db.Usage())
	require.NoError(t, err)
	require.Equal(t, 0.0, r.GapPct, "no usage on either side")
	require.False(t, r.Warn)
}

func TestBuildReportCountsOrphans(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	day := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	_, err := h.db.Usage().InsertCostLookupOrphans(ctx, []store.CostLookupOrphan{{Provider: provID, RequestID: "o1", Quota: 500, CreatedAt: day.Add(time.Hour)}})
	require.NoError(t, err)
	r, err := BuildReport(ctx, provID, day, func(context.Context, int64, int64) (int64, error) { return 500, nil }, h.db.Usage())
	require.NoError(t, err)
	require.Equal(t, 1, r.OrphanCount)
	require.InDelta(t, 0.001, r.OrphanUSD, 1e-12)
	require.True(t, r.Warn, "all of the provider's cost is unaccounted for")
}
