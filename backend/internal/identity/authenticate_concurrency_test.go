package identity

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/config"
	"github.com/mydisha/keirouter/backend/internal/store"
)

func TestAuthenticateConcurrentSameKey(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: ":memory:"}, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.Migrate(ctx))
	require.NoError(t, db.Tenants().EnsureDefault(ctx))

	svc := New(db.APIKeys())
	issued, err := svc.Generate(store.DefaultTenantID, "", "burst")
	require.NoError(t, err)
	require.NoError(t, db.APIKeys().Create(ctx, issued.Record))

	const n = 100
	var wg sync.WaitGroup
	errs := make([]error, n)
	ids := make([]string, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec, err := svc.Authenticate(ctx, issued.Plaintext)
			errs[i], ids[i] = err, rec.ID
		}()
	}
	wg.Wait()
	for i := range n {
		require.NoError(t, errs[i])
		require.Equal(t, issued.Record.ID, ids[i])
	}

	_, err = svc.Authenticate(ctx, issued.Plaintext+"x")
	require.ErrorIs(t, err, ErrUnauthorized)

	require.NoError(t, svc.SetDisabled(ctx, issued.Record.ID, true))
	_, err = svc.Authenticate(ctx, issued.Plaintext)
	require.ErrorIs(t, err, ErrUnauthorized, "disable must bypass the 2 minute cache")
}
