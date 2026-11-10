package tokenpkg

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/config"
	"github.com/mydisha/keirouter/backend/internal/store"
)

func TestApply(t *testing.T) {
	now := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	const m = int64(1_000_000)
	pkg100 := store.TokenPackage{Kind: store.TokenPackageTokens, Tokens: 100 * m, Days: 15}
	days15 := store.TokenPackage{Kind: store.TokenPackageDays, Days: 15}
	active := State{Exists: true, ExpiresAt: now.Add(5 * day), UsedTokens: 30 * m, LimitTokens: 100 * m}
	expired := State{Exists: true, ExpiresAt: now.Add(-time.Hour), UsedTokens: 30 * m, LimitTokens: 100 * m}

	tests := []struct {
		name    string
		st      State
		pkg     store.TokenPackage
		want    Result
		wantErr error
	}{
		{"none + tokens", State{}, pkg100, Result{now.Add(15 * day), 100 * m}, nil},
		{"active + tokens", active, pkg100, Result{now.Add(5 * day), 200 * m}, nil},
		{"expired + tokens drops leftover", expired, pkg100, Result{now.Add(15 * day), 130 * m}, nil},
		{"active + days", active, days15, Result{now.Add(20 * day), 100 * m}, nil},
		{"expired + days", expired, days15, Result{}, ErrNoActivePackage},
		{"none + days", State{}, days15, Result{}, ErrNoActivePackage},
		{"boundary is expired", State{Exists: true, ExpiresAt: now, UsedTokens: 10 * m, LimitTokens: 100 * m}, pkg100, Result{now.Add(15 * day), 110 * m}, nil},
		{"unknown kind", State{}, store.TokenPackage{Kind: "x"}, Result{}, errUnknownKind},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Apply(tt.st, tt.pkg, now)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func newSettingsRepo(t *testing.T) *store.SettingsRepo {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: ":memory:"}, t.TempDir())
	require.NoError(t, err)
	require.NoError(t, db.Migrate(ctx))
	t.Cleanup(func() { _ = db.Close() })
	return db.Settings()
}

func TestLoadDefaults(t *testing.T) {
	s, err := Load(context.Background(), newSettingsRepo(t))
	require.NoError(t, err)
	require.False(t, s.Enabled)
	require.Equal(t, DefaultModels, s.AllowedModels)
}

func TestSaveLoadRoundTrip(t *testing.T) {
	ctx := context.Background()
	repo := newSettingsRepo(t)
	want := Settings{Enabled: true, AllowedModels: []string{"gpt-6-sol"}}
	require.NoError(t, Save(ctx, repo, want))
	got, err := Load(ctx, repo)
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestSaveRejectsEmptyModels(t *testing.T) {
	require.Error(t, Save(context.Background(), newSettingsRepo(t), Settings{Enabled: true}))
}
