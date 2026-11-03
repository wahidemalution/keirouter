package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestKeyRoutingRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	repo := db.APIKeys()
	require.NoError(t, repo.Create(ctx, APIKey{
		ID: "k-kr", TenantID: DefaultTenantID, Name: "kr", KeyHash: "h",
		LookupHash: "lookup-kr", Display: "kr-...", CreatedAt: time.Now().UTC(),
	}))

	got, err := repo.KeyRouting(ctx, "k-kr")
	require.NoError(t, err)
	require.Empty(t, got.AllowedProviders)
	require.Empty(t, got.DisabledProviders)
	require.Empty(t, got.FinalFallback)

	require.NoError(t, repo.SetAllowedProviders(ctx, "k-kr", []string{" p1 ", "p1", "p2", ""}))
	got, err = repo.KeyRouting(ctx, "k-kr")
	require.NoError(t, err)
	require.Equal(t, []string{"p1", "p2"}, got.AllowedProviders)

	require.NoError(t, repo.SetAllowedProviders(ctx, "k-kr", []string{"p2", "p3"}))
	got, err = repo.KeyRouting(ctx, "k-kr")
	require.NoError(t, err)
	require.Equal(t, []string{"p2", "p3"}, got.AllowedProviders, "the set is replaced, not merged")

	require.NoError(t, repo.SetModelProviderEnabled(ctx, "k-kr", "DeepSeek-V4", "p1", false))
	require.NoError(t, repo.SetModelProviderEnabled(ctx, "k-kr", "chain:deepseek-v4", "p1", false), "disabling twice is idempotent")
	require.NoError(t, repo.SetModelProviderEnabled(ctx, "k-kr", "deepseek-v4", "p2", false))
	got, err = repo.KeyRouting(ctx, "k-kr")
	require.NoError(t, err)
	require.Equal(t, map[string][]string{"deepseek-v4": {"p1", "p2"}}, got.DisabledProviders)

	require.NoError(t, repo.SetModelProviderEnabled(ctx, "k-kr", "deepseek-v4", "p1", true))
	require.NoError(t, repo.SetModelProviderEnabled(ctx, "k-kr", "deepseek-v4", "never-off", true))
	got, err = repo.KeyRouting(ctx, "k-kr")
	require.NoError(t, err)
	require.Equal(t, map[string][]string{"deepseek-v4": {"p2"}}, got.DisabledProviders)

	require.NoError(t, repo.SetModelFinalFallback(ctx, "k-kr", "Glm-Fast", true))
	require.NoError(t, repo.SetModelFinalFallback(ctx, "k-kr", "other", true))
	require.NoError(t, repo.SetModelFinalFallback(ctx, "k-kr", "other", false))
	got, err = repo.KeyRouting(ctx, "k-kr")
	require.NoError(t, err)
	require.Equal(t, map[string]bool{"glm-fast": true, "other": false}, got.FinalFallback)

	require.NoError(t, repo.Delete(ctx, "k-kr"))
	for _, table := range []string{"api_key_allowed_providers", "api_key_model_disabled_providers", "api_key_model_options"} {
		var n int
		require.NoError(t, db.sql.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE api_key_id = 'k-kr'").Scan(&n))
		require.Zero(t, n, table)
	}
}

func TestMigration0061SeedsFallback(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	now := time.Now().UTC()

	// Rewind to the state before 0061 ran.
	for _, table := range []string{"api_key_model_options", "api_key_model_disabled_providers", "api_key_allowed_providers"} {
		_, err := db.sql.ExecContext(ctx, "DROP TABLE "+table)
		require.NoError(t, err)
	}
	_, err := db.sql.ExecContext(ctx, db.rebind("DELETE FROM schema_migrations WHERE version = ?"), "0061_key_provider_routing")
	require.NoError(t, err)

	require.NoError(t, db.APIKeys().Create(ctx, APIKey{
		ID: "k-old", TenantID: DefaultTenantID, Name: "old", KeyHash: "h",
		LookupHash: "lookup-old", Display: "kr-...", CreatedAt: now,
	}))
	step := func(id, chain string) []ChainStep {
		return []ChainStep{{ID: id, ChainID: chain, Position: 0, Provider: "openai", Model: "gpt-4o", CreatedAt: now}}
	}
	require.NoError(t, db.Chains().Create(ctx, Chain{
		ID: "c-glm", TenantID: DefaultTenantID, Name: "Glm-Fast", Strategy: "priority",
		FallbackProvider: "openai", FallbackModel: "gpt-4o-mini",
		CreatedAt: now, UpdatedAt: now, Steps: step("s-glm", "c-glm"),
	}))
	require.NoError(t, db.Chains().Create(ctx, Chain{
		ID: "c-plain", TenantID: DefaultTenantID, Name: "plain", Strategy: "priority",
		CreatedAt: now, UpdatedAt: now, Steps: step("s-plain", "c-plain"),
	}))

	require.NoError(t, db.Migrate(ctx))
	seeded := func() map[string]bool {
		got, err := db.APIKeys().KeyRouting(ctx, "k-old")
		require.NoError(t, err)
		return got.FinalFallback
	}
	require.Equal(t, map[string]bool{"glm-fast": true}, seeded())

	// A user switching it off must survive another startup.
	require.NoError(t, db.APIKeys().SetModelFinalFallback(ctx, "k-old", "glm-fast", false))
	require.NoError(t, db.Migrate(ctx))
	require.Equal(t, map[string]bool{"glm-fast": false}, seeded(), "a second Migrate is a no-op")

	require.NoError(t, db.APIKeys().Create(ctx, APIKey{
		ID: "k-new", TenantID: DefaultTenantID, Name: "new", KeyHash: "h",
		LookupHash: "lookup-new", Display: "kr-...", CreatedAt: now,
	}))
	got, err := db.APIKeys().KeyRouting(ctx, "k-new")
	require.NoError(t, err)
	require.Empty(t, got.FinalFallback, "keys created after the migration start with the fallback off")
}

func TestAPIKeyProviderEnabled(t *testing.T) {
	require.True(t, APIKey{}.ProviderAllowed("p2"))
	require.True(t, APIKey{}.ProviderEnabled("any", "p2"))
	require.False(t, APIKey{}.FinalFallbackOn("any"))

	k := APIKey{
		AllowedProviders:       []string{"p1"},
		ModelDisabledProviders: map[string][]string{"deepseek-v4": {"p1"}},
		ModelFinalFallback:     map[string]bool{"deepseek-v4": true},
	}
	require.True(t, k.ProviderAllowed("p1"))
	require.False(t, k.ProviderAllowed("p2"), "outside the ceiling")
	require.False(t, k.ProviderEnabled("other", "p2"))
	require.True(t, k.ProviderEnabled("other", "p1"))
	require.False(t, k.ProviderEnabled("chain:DeepSeek-V4", "p1"), "the disabled list wins over the ceiling")
	require.True(t, k.FinalFallbackOn("chain:DeepSeek-V4"))
	require.False(t, k.FinalFallbackOn("other"))

	open := APIKey{ModelDisabledProviders: map[string][]string{"m": {"p2"}}}
	require.False(t, open.ProviderEnabled("M", "p2"))
	require.True(t, open.ProviderEnabled("M", "p3"))
}
