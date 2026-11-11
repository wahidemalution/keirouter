package store

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// roundTripJSON pushes a backup through its file encoding, as a real
// download/upload would.
func roundTripJSON(t *testing.T, b Backup) Backup {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, json.NewEncoder(&buf).Encode(b))
	out, err := DecodeBackup(&buf)
	require.NoError(t, err)
	return out
}

func seedBackupSource(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	markup := 25.0
	require.NoError(t, db.Chains().Create(ctx, Chain{
		ID: "c1", TenantID: DefaultTenantID, Name: "luna", Strategy: "priority",
		InputPerM: 0.0027445, OutputPerM: 0.0164659, MarketSlugs: []string{"surplus-trusted:gpt-5.6-luna"},
		MarkupPercent: &markup, CreatedAt: now, UpdatedAt: now,
		Steps: []ChainStep{{ID: "s1", ChainID: "c1", Provider: "custom-openai-surplus", Model: "gpt-5.6-luna", MarketSlug: "surplus-trusted:gpt-5.6-luna", CreatedAt: now}},
	}))
	require.NoError(t, db.APIKeys().Create(ctx, APIKey{
		ID: "k1", TenantID: DefaultTenantID, Name: "user", KeyHash: "argon-hash", LookupHash: "lookup-1",
		Display: "kr_ab…cd", CreatedAt: now,
	}))
	require.NoError(t, db.Usage().Record(ctx, UsageRecord{
		ID: "u1", TenantID: DefaultTenantID, APIKeyID: "k1", Provider: "custom-openai-surplus", Model: "gpt-5.6-luna",
		Status: "success", PromptTokens: 18, CompletionTokens: 120, CostNanos: 2500, UpstreamCostNanos: 2000, CreatedAt: now,
	}))
	require.NoError(t, db.Settings().Set(ctx, "market_pricing_settings", `{"markup_percent":10}`))
}

func TestBackupRestoreRoundTripReplacesTarget(t *testing.T) {
	ctx := context.Background()
	src := newTestDB(t)
	seedBackupSource(t, src)

	b, err := src.ExportBackup(ctx)
	require.NoError(t, err)
	require.Equal(t, BackupFormat, b.Format)
	require.NotEmpty(t, b.SchemaVersion)
	b = roundTripJSON(t, b)

	dst := newTestDB(t)
	now := time.Now().UTC()
	require.NoError(t, dst.Chains().Create(ctx, Chain{ID: "stale", TenantID: DefaultTenantID, Name: "stale", Strategy: "priority", CreatedAt: now, UpdatedAt: now,
		Steps: []ChainStep{{ID: "x", ChainID: "stale", Provider: "openai", Model: "gpt-4o", CreatedAt: now}}}))

	stats, err := dst.RestoreBackup(ctx, b)
	require.NoError(t, err)
	require.Greater(t, stats.Rows, 0)

	chains, err := dst.Chains().ListByTenant(ctx, DefaultTenantID)
	require.NoError(t, err)
	require.Len(t, chains, 1, "restore must replace, not merge")
	c := chains[0]
	require.Equal(t, "luna", c.Name)
	require.Equal(t, 0.0027445, c.InputPerM)
	require.Equal(t, []string{"surplus-trusted:gpt-5.6-luna"}, c.MarketSlugs)
	require.Equal(t, 25.0, *c.MarkupPercent)
	require.Equal(t, "surplus-trusted:gpt-5.6-luna", c.Steps[0].MarketSlug)

	k, err := dst.APIKeys().FindByLookup(ctx, "lookup-1")
	require.NoError(t, err)
	require.Equal(t, "argon-hash", k.KeyHash, "key hash must survive so the user's key keeps working")

	var nanos, upstream int64
	require.NoError(t, dst.SQL().QueryRowContext(ctx, `SELECT cost_nanos, upstream_cost_nanos FROM usage_records WHERE id = 'u1'`).Scan(&nanos, &upstream))
	require.Equal(t, int64(2500), nanos, "spent balance comes from usage; it must be exact")
	require.Equal(t, int64(2000), upstream)

	v, err := dst.Settings().Get(ctx, "market_pricing_settings")
	require.NoError(t, err)
	require.Equal(t, `{"markup_percent":10}`, v)
}

func TestRestoreRefusesNewerSchema(t *testing.T) {
	ctx := context.Background()
	src := newTestDB(t)
	b, err := src.ExportBackup(ctx)
	require.NoError(t, err)
	b.SchemaVersion = "9999_future"
	_, err = newTestDB(t).RestoreBackup(ctx, b)
	require.ErrorContains(t, err, "newer")
}

func TestRestoreRollsBackOnBadRow(t *testing.T) {
	ctx := context.Background()
	src := newTestDB(t)
	seedBackupSource(t, src)
	b, err := src.ExportBackup(ctx)
	require.NoError(t, err)
	b = roundTripJSON(t, b)
	for i := range b.Tables {
		if b.Tables[i].Name == "chains" {
			b.Tables[i].Columns = append(b.Tables[i].Columns, "no_such_column")
			for r := range b.Tables[i].Rows {
				b.Tables[i].Rows[r] = append(b.Tables[i].Rows[r], "x")
			}
		}
	}
	dst := newTestDB(t)
	now := time.Now().UTC()
	require.NoError(t, dst.Chains().Create(ctx, Chain{ID: "keep", TenantID: DefaultTenantID, Name: "keep", Strategy: "priority", CreatedAt: now, UpdatedAt: now,
		Steps: []ChainStep{{ID: "x", ChainID: "keep", Provider: "openai", Model: "gpt-4o", CreatedAt: now}}}))
	_, err = dst.RestoreBackup(ctx, b)
	require.Error(t, err)
	chains, err := dst.Chains().ListByTenant(ctx, DefaultTenantID)
	require.NoError(t, err)
	require.Len(t, chains, 1)
	require.Equal(t, "keep", chains[0].Name, "a failed restore must leave the target untouched")
}

func TestBackupCoversEveryTable(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	b, err := db.ExportBackup(ctx)
	require.NoError(t, err)
	got := map[string]bool{}
	for _, tb := range b.Tables {
		got[tb.Name] = true
	}
	tables, err := db.backupTables(ctx)
	require.NoError(t, err)
	for _, name := range tables {
		require.True(t, got[name], "table %s missing from backup", name)
	}
	require.False(t, got["schema_migrations"])
	require.True(t, got["api_keys"] && got["chains"] && got["usage_records"] && got["portal_users"] && got["key_topups"])
	require.True(t, got["upstream_accounts"] && got["upstream_account_providers"] && got["upstream_topups"], "finance tables must be in the backup")
}

func TestBackupRoundTripKeyRouting(t *testing.T) {
	ctx := context.Background()
	src := newTestDB(t)
	seedBackupSource(t, src)
	repo := src.APIKeys()
	require.NoError(t, repo.SetAllowedProviders(ctx, "k1", []string{"custom-openai-surplus", "openai"}))
	require.NoError(t, repo.SetModelProviderEnabled(ctx, "k1", "luna", "openai", false))
	require.NoError(t, repo.SetModelFinalFallback(ctx, "k1", "luna", true))
	want, err := repo.KeyRouting(ctx, "k1")
	require.NoError(t, err)
	require.NotEmpty(t, want.AllowedProviders)

	b, err := src.ExportBackup(ctx)
	require.NoError(t, err)
	b = roundTripJSON(t, b)

	dst := newTestDB(t)
	_, err = dst.RestoreBackup(ctx, b)
	require.NoError(t, err)
	got, err := dst.APIKeys().KeyRouting(ctx, "k1")
	require.NoError(t, err)
	require.Equal(t, want, got)
	require.Equal(t, map[string]int{"luna": FinalFallbackChosen}, got.FinalFallback, "a backup at 0061 or later restores as-is, no reseed")
}

func TestRestorePre0061BackupSeedsKeptFallback(t *testing.T) {
	ctx := context.Background()
	src := newTestDB(t)
	seedBackupSource(t, src)
	now := time.Now().UTC()
	require.NoError(t, src.Chains().Create(ctx, Chain{
		ID: "c2", TenantID: DefaultTenantID, Name: "Glm-Fast", Strategy: "priority",
		FallbackProvider: "openai", FallbackModel: "gpt-4o-mini", CreatedAt: now, UpdatedAt: now,
		Steps: []ChainStep{{ID: "s2", ChainID: "c2", Provider: "openai", Model: "gpt-4o", CreatedAt: now}},
	}))

	b, err := src.ExportBackup(ctx)
	require.NoError(t, err)
	b.SchemaVersion = "0060_chain_step_static_price"
	kept := b.Tables[:0]
	for _, tb := range b.Tables {
		switch tb.Name {
		case "api_key_allowed_providers", "api_key_model_disabled_providers", "api_key_model_options":
		default:
			kept = append(kept, tb)
		}
	}
	b.Tables = kept
	b = roundTripJSON(t, b)

	dst := newTestDB(t)
	_, err = dst.RestoreBackup(ctx, b)
	require.NoError(t, err)
	got, err := dst.APIKeys().KeyRouting(ctx, "k1")
	require.NoError(t, err)
	require.Equal(t, map[string]int{"glm-fast": FinalFallbackKept}, got.FinalFallback,
		"the chain with a fallback is kept as before; the chain without one gets no row")
	require.Empty(t, got.AllowedProviders)
	require.Empty(t, got.DisabledProviders)
}

func TestBackupRoundTripCostPayments(t *testing.T) {
	ctx := context.Background()
	src := newTestDB(t)
	paid := time.Date(2026, 11, 5, 3, 4, 5, 0, time.UTC)
	want := CostPayment{
		ID: "p1", Name: "OpenRouter credit", Category: "upstream_ai", Provider: "OpenRouter",
		Currency: CurrencyUSD, AmountUSDCents: 1250, USDIDRRate: 16234.5, AmountIDR: 202931,
		PaymentMethod: "crypto", PaidAt: paid, Note: "top-up", CreatedAt: paid, UpdatedAt: paid,
	}
	require.NoError(t, src.CostPayments().Create(ctx, want))

	b, err := src.ExportBackup(ctx)
	require.NoError(t, err)
	dst := newTestDB(t)
	_, err = dst.RestoreBackup(ctx, roundTripJSON(t, b))
	require.NoError(t, err)
	got, err := dst.CostPayments().Get(ctx, "p1")
	require.NoError(t, err)
	require.Equal(t, want, got, "the saved rate and rupiah amount survive a restore unchanged")
}
