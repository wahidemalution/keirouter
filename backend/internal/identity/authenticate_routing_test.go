package identity

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/config"
	"github.com/mydisha/keirouter/backend/internal/store"
)

func TestAuthenticateLoadsKeyRouting(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: ":memory:"}, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.Migrate(ctx))
	require.NoError(t, db.Tenants().EnsureDefault(ctx))

	svc := New(db.APIKeys())
	issued, err := svc.Generate(store.DefaultTenantID, "", "routing")
	require.NoError(t, err)
	require.NoError(t, db.APIKeys().Create(ctx, issued.Record))

	keys := db.APIKeys()
	require.NoError(t, keys.SetAllowedProviders(ctx, issued.Record.ID, []string{"p1", "p2"}))
	require.NoError(t, keys.SetModelProviderEnabled(ctx, issued.Record.ID, "DeepSeek-V4", "p1", false))
	require.NoError(t, keys.SetModelFinalFallback(ctx, issued.Record.ID, "deepseek-v4", true))

	rec, err := svc.Authenticate(ctx, issued.Plaintext)
	require.NoError(t, err)
	require.Equal(t, []string{"p1", "p2"}, rec.AllowedProviders)
	require.False(t, rec.ProviderEnabled("chain:deepseek-v4", "p1"))
	require.True(t, rec.ProviderEnabled("chain:deepseek-v4", "p2"))
	require.True(t, rec.FinalFallbackOn("DeepSeek-V4"))
}
