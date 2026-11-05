package store

import (
	"context"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var rollupBase = time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)

// seedRollupUsage writes varied usage rows across five days: several keys,
// projects and tenants, missing ids, zero costs, zero tokens and backfilled
// rows, through both the batch and the single-row insert paths.
func seedRollupUsage(t *testing.T, db *DB, rng *rand.Rand, n int, prefix string) []UsageRecord {
	t.Helper()
	keys := []string{"key-a", "key-b", "key-c", ""}
	projects := []string{"proj-1", "proj-2", ""}
	tenants := []string{DefaultTenantID, "tenant-2"}
	recs := make([]UsageRecord, n)
	for i := range recs {
		cost := rng.Int64N(5_000_000_000)
		if rng.IntN(5) == 0 {
			cost = 0
		}
		prompt, completion := int(rng.Int64N(50_000)), int(rng.Int64N(5_000))
		if rng.IntN(7) == 0 {
			prompt, completion = 0, 0
		}
		recs[i] = UsageRecord{
			ID: fmt.Sprintf("%s-%d", prefix, i), TenantID: tenants[rng.IntN(len(tenants))],
			ProjectID: projects[rng.IntN(len(projects))], APIKeyID: keys[rng.IntN(len(keys))],
			Provider: "openai", Model: "m", Status: "success",
			PromptTokens: prompt, CompletionTokens: completion, CostNanos: cost,
			PricingStatus: "priced", PricingBackfilled: rng.IntN(6) == 0,
			CreatedAt: rollupBase.Add(time.Duration(rng.Int64N(int64(5 * 24 * time.Hour)))),
		}
	}
	ctx := context.Background()
	half := n / 2
	require.NoError(t, db.Usage().RecordBatch(ctx, recs[:half]))
	for _, r := range recs[half:] {
		require.NoError(t, db.Usage().Record(ctx, r))
	}
	return recs
}

func rollupSinces() []time.Time {
	return []time.Time{
		{},
		rollupBase.AddDate(0, 0, -1),
		rollupBase,
		rollupBase.Add(3*time.Hour + 17*time.Minute + 5*time.Second),
		rollupBase.AddDate(0, 0, 2),
		rollupBase.AddDate(0, 0, 2).Add(-time.Second),
		rollupBase.AddDate(0, 0, 4).Add(23*time.Hour + 59*time.Minute + 59*time.Second),
		rollupBase.AddDate(0, 0, 9),
	}
}

func rollupScopes() []SpendScope {
	var out []SpendScope
	ids := map[BudgetScope][]string{
		ScopeAPIKey:  {"key-a", "key-b", "key-c", "key-none"},
		ScopeProject: {"proj-1", "proj-2"},
		ScopeTenant:  {DefaultTenantID, "tenant-2"},
	}
	for kind, list := range ids {
		for _, id := range list {
			for _, since := range rollupSinces() {
				out = append(out, SpendScope{Kind: kind, ScopeID: id, Since: since})
			}
		}
	}
	return out
}

// requireRollupMatchesRaw checks every spend query against the plain
// usage_records sum for every scope and since.
func requireRollupMatchesRaw(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()
	require.True(t, db.SpendRollupEnabled())
	scopes := rollupScopes()

	db.spendRollup.Store(false)
	want, err := db.Usage().SpendAndTokensBatch(ctx, scopes)
	db.spendRollup.Store(true)
	require.NoError(t, err)

	got, err := db.Usage().SpendAndTokensBatch(ctx, scopes)
	require.NoError(t, err)
	var nonZero int
	for i, s := range scopes {
		require.Equal(t, want[i], got[i], "batch %s/%s since %v", s.Kind, s.ScopeID, s.Since)
		cost, tokens, err := db.Usage().SpendAndTokens(ctx, s.Kind, s.ScopeID, s.Since)
		require.NoError(t, err)
		require.Equal(t, want[i].CostMicros, cost, "single %s/%s since %v", s.Kind, s.ScopeID, s.Since)
		require.Equal(t, want[i].Tokens, tokens, "single %s/%s since %v", s.Kind, s.ScopeID, s.Since)
		if want[i].CostNanos != 0 {
			nonZero++
		}
	}
	require.Greater(t, nonZero, len(scopes)/2, "the fixture must exercise real sums")

	bad, err := db.CheckSpendRollup(ctx)
	require.NoError(t, err)
	require.Zero(t, bad)
}

func TestSpendRollupMatchesUsageRows(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	rng := rand.New(rand.NewPCG(1, 2))
	recs := seedRollupUsage(t, db, rng, 600, "u")
	requireRollupMatchesRaw(t, db)

	t.Run("repricing updates", func(t *testing.T) {
		for i := 0; i < len(recs); i += 7 {
			require.NoError(t, db.Usage().UpdateUsagePricing(ctx, recs[i].ID, UsagePricingUpdate{
				CostNanos: rng.Int64N(9_000_000_000), PricingStatus: "estimated", PricingBackfilled: i%2 == 0,
			}))
		}
		requireRollupMatchesRaw(t, db)
	})

	t.Run("deletes", func(t *testing.T) {
		_, err := db.SQL().ExecContext(ctx, db.rebind(`DELETE FROM usage_records WHERE id LIKE ?`), "u-1%")
		require.NoError(t, err)
		requireRollupMatchesRaw(t, db)
	})

	t.Run("direct column updates", func(t *testing.T) {
		_, err := db.SQL().ExecContext(ctx, db.rebind(`UPDATE usage_records SET api_key_id = ?, prompt_tokens = prompt_tokens + 3 WHERE id LIKE ?`), "key-c", "u-2%")
		require.NoError(t, err)
		_, err = db.SQL().ExecContext(ctx, db.rebind(`UPDATE usage_records SET created_at = ? WHERE id LIKE ?`),
			formatTime(rollupBase.Add(26*time.Hour)), "u-3%")
		require.NoError(t, err)
		requireRollupMatchesRaw(t, db)
	})

	t.Run("more inserts", func(t *testing.T) {
		seedRollupUsage(t, db, rng, 200, "v")
		requireRollupMatchesRaw(t, db)
	})
}

func TestSpendRollupRepairsDrift(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedRollupUsage(t, db, rand.New(rand.NewPCG(3, 4)), 300, "u")

	_, err := db.SQL().ExecContext(ctx, `UPDATE usage_spend_daily SET cost_nanos = cost_nanos + 1 WHERE scope_kind = 'api_key'`)
	require.NoError(t, err)
	_, err = db.SQL().ExecContext(ctx, db.rebind(`DELETE FROM usage_spend_daily WHERE scope_kind = ?`), "project")
	require.NoError(t, err)

	bad, err := db.CheckSpendRollup(ctx)
	require.NoError(t, err)
	require.Positive(t, bad, "drift is detected")
	requireRollupMatchesRaw(t, db)
}

func TestSpendRollupStartupBuildsExistingRows(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// Rows written before the rollup existed: no triggers, empty table.
	_, err := db.EnsureSpendRollup(ctx, false)
	require.NoError(t, err)
	require.False(t, db.SpendRollupEnabled())
	seedRollupUsage(t, db, rand.New(rand.NewPCG(5, 6)), 300, "u")
	var n int
	require.NoError(t, db.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_spend_daily`).Scan(&n))
	require.Zero(t, n, "disabled: triggers are gone")

	// Disabled spend queries sum usage_records directly.
	cost, _, err := db.Usage().SpendAndTokens(ctx, ScopeAPIKey, "key-a", time.Time{})
	require.NoError(t, err)
	require.Positive(t, cost)

	bad, err := db.EnsureSpendRollup(ctx, true)
	require.NoError(t, err)
	require.Positive(t, bad, "the empty table is rebuilt")
	requireRollupMatchesRaw(t, db)

	bad, err = db.EnsureSpendRollup(ctx, true)
	require.NoError(t, err)
	require.Zero(t, bad, "a second startup finds nothing to fix")
}

func TestSpendRollupBackupAndRestore(t *testing.T) {
	src := newTestDB(t)
	ctx := context.Background()
	seedRollupUsage(t, src, rand.New(rand.NewPCG(7, 8)), 300, "u")

	b, err := src.ExportBackup(ctx)
	require.NoError(t, err)
	for _, tbl := range b.Tables {
		require.NotEqual(t, spendRollupTable, tbl.Name, "the rollup is derived, not backed up")
	}

	dst := newTestDB(t)
	seedRollupUsage(t, dst, rand.New(rand.NewPCG(9, 10)), 100, "old")
	_, err = dst.RestoreBackup(ctx, b)
	require.NoError(t, err)
	requireRollupMatchesRaw(t, dst)

	scopes := rollupScopes()
	want, err := src.Usage().SpendAndTokensBatch(ctx, scopes)
	require.NoError(t, err)
	got, err := dst.Usage().SpendAndTokensBatch(ctx, scopes)
	require.NoError(t, err)
	require.Equal(t, want, got, "restored spend equals the source")
}

func TestSpendRollupOutstandingPortalCredit(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedRollupUsage(t, db, rand.New(rand.NewPCG(11, 12)), 300, "u")
	for _, key := range []string{"key-a", "key-b", "key-c"} {
		require.NoError(t, db.APIKeys().Create(ctx, APIKey{ID: key, TenantID: DefaultTenantID, Name: key,
			KeyHash: "h" + key, LookupHash: "l" + key, Display: "d", CreatedAt: rollupBase}))
		require.NoError(t, db.PortalUsers().Upsert(ctx, PortalUser{GoogleSub: "s" + key, Email: key + "@x.io",
			KeyID: key, CreatedAt: rollupBase, UpdatedAt: rollupBase}))
		require.NoError(t, db.Budgets().Create(ctx, Budget{ID: "b" + key, TenantID: DefaultTenantID,
			ScopeKind: ScopeAPIKey, ScopeID: key, LimitMicros: 400_000_000, Period: "total",
			AlertPct: 80, HardCutoff: true, CreatedAt: rollupBase, UpdatedAt: rollupBase}))
	}

	db.spendRollup.Store(false)
	want, err := db.Usage().OutstandingPortalCredit(ctx, DefaultTenantID)
	db.spendRollup.Store(true)
	require.NoError(t, err)
	got, err := db.Usage().OutstandingPortalCredit(ctx, DefaultTenantID)
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestSpendRollupTruncate(t *testing.T) {
	db := newTestDB(t)
	if db.Dialect() != DialectPostgres {
		t.Skip("TRUNCATE is Postgres only")
	}
	ctx := context.Background()
	seedRollupUsage(t, db, rand.New(rand.NewPCG(13, 14)), 50, "u")
	_, err := db.SQL().ExecContext(ctx, `TRUNCATE usage_records`)
	require.NoError(t, err)
	var n int
	require.NoError(t, db.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_spend_daily`).Scan(&n))
	require.Zero(t, n)
	seedRollupUsage(t, db, rand.New(rand.NewPCG(15, 16)), 300, "v")
	requireRollupMatchesRaw(t, db)
}
