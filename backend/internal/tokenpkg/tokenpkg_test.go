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
		{"none + tokens", State{}, pkg100, Result{ExpiresAt: now.Add(15 * day), LimitTokens: 100 * m}, nil},
		{"active + tokens", active, pkg100, Result{ExpiresAt: now.Add(5 * day), LimitTokens: 200 * m}, nil},
		{"expired + tokens drops leftover", expired, pkg100, Result{ExpiresAt: now.Add(15 * day), LimitTokens: 130 * m}, nil},
		{"active + days", active, days15, Result{ExpiresAt: now.Add(20 * day), LimitTokens: 100 * m}, nil},
		{"expired + days", expired, days15, Result{}, ErrNoActivePackage},
		{"none + days", State{}, days15, Result{}, ErrNoActivePackage},
		{"boundary is expired", State{Exists: true, ExpiresAt: now, UsedTokens: 10 * m, LimitTokens: 100 * m}, pkg100, Result{ExpiresAt: now.Add(15 * day), LimitTokens: 110 * m}, nil},
		{"unknown kind", State{}, store.TokenPackage{Kind: "x"}, Result{}, errUnknownKind},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Apply(tt.st, tt.pkg, PlanLimits{RPM: 60, Concurrency: 3}, now)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestApplyLimits(t *testing.T) {
	now := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	p := func(n int64) *int64 { return &n }
	lim := func(rpm, conc *int64) Limits { return Limits{RPM: rpm, Concurrency: conc} }
	tokens := func(l Limits) store.TokenPackage {
		return store.TokenPackage{Kind: store.TokenPackageTokens, Tokens: 1_000_000, Days: 15, RPMLimit: l.RPM, ConcurrencyLimit: l.Concurrency}
	}
	days := store.TokenPackage{Kind: store.TokenPackageDays, Days: 15, RPMLimit: p(500), ConcurrencyLimit: p(50)}
	active := func(l Limits) State {
		return State{Exists: true, ExpiresAt: now.Add(5 * day), LimitTokens: 1_000_000, Limits: l}
	}
	expired := func(l Limits) State {
		return State{Exists: true, ExpiresAt: now.Add(-time.Hour), LimitTokens: 1_000_000, Limits: l}
	}
	plan := PlanLimits{RPM: 60, Concurrency: 3}

	tests := []struct {
		name string
		st   State
		pkg  store.TokenPackage
		plan PlanLimits
		want Limits
	}{
		{"active: package raises plan-following key", active(lim(nil, nil)), tokens(lim(p(120), p(10))), plan, lim(p(120), p(10))},
		{"active: smaller package keeps highest", active(lim(p(120), p(10))), tokens(lim(p(30), p(2))), plan, lim(p(120), p(10))},
		{"active: plan-following package keeps highest", active(lim(p(120), p(10))), tokens(lim(nil, nil)), plan, lim(p(120), p(10))},
		{"active: general wins stores nil", active(lim(p(30), p(2))), tokens(lim(nil, nil)), plan, lim(nil, nil)},
		{"active: tie with plan stores nil", active(lim(p(60), p(3))), tokens(lim(nil, nil)), plan, lim(nil, nil)},
		{"active: fields stack independently", active(lim(p(120), nil)), tokens(lim(p(30), p(10))), plan, lim(p(120), p(10))},
		{"expired: new period takes package limits", expired(lim(p(120), p(10))), tokens(lim(p(30), p(2))), plan, lim(p(30), p(2))},
		{"expired: plan-following package resets to plan", expired(lim(p(120), p(10))), tokens(lim(nil, nil)), plan, lim(nil, nil)},
		{"none: package limits taken as-is", State{}, tokens(lim(p(30), nil)), plan, lim(p(30), nil)},
		{"active: days package leaves limits", active(lim(p(120), p(10))), days, plan, lim(p(120), p(10))},
		{"active: unlimited plan beats package", active(lim(nil, nil)), tokens(lim(p(120), p(10))), PlanLimits{}, lim(nil, nil)},
		{"active: unlimited plan beats stored limit", active(lim(p(120), p(10))), tokens(lim(nil, nil)), PlanLimits{}, lim(nil, nil)},
		{"active: two below-plan limits keep their max", active(lim(p(30), p(2))), tokens(lim(p(30), p(1))), plan, lim(p(30), p(2))},
		{"active: below-plan stored beats lower package", active(lim(p(30), p(2))), tokens(lim(p(20), p(1))), plan, lim(p(30), p(2))},
		{"active: unlimited plan keeps own limits when both set", active(lim(p(120), p(10))), tokens(lim(p(30), p(2))), PlanLimits{}, lim(p(120), p(10))},
		{"active: plan beats a lone below-plan package", active(lim(nil, nil)), tokens(lim(p(30), p(2))), plan, lim(nil, nil)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Apply(tt.st, tt.pkg, tt.plan, now)
			require.NoError(t, err)
			require.Equal(t, tt.want, got.Limits)
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
}

func TestSaveLoadRoundTrip(t *testing.T) {
	ctx := context.Background()
	repo := newSettingsRepo(t)
	want := Settings{Enabled: true, PlanID: DefaultPlanID}
	require.NoError(t, Save(ctx, repo, want))
	got, err := Load(ctx, repo)
	require.NoError(t, err)
	require.Equal(t, want, got)
}

// Settings saved before models moved to the plan still load.
func TestLoadIgnoresLegacyModels(t *testing.T) {
	ctx := context.Background()
	repo := newSettingsRepo(t)
	require.NoError(t, repo.Set(ctx, SettingsKey, `{"enabled":true,"allowed_models":["x"],"plan_id":""}`))
	got, err := Load(ctx, repo)
	require.NoError(t, err)
	require.Equal(t, Settings{Enabled: true, PlanID: DefaultPlanID}, got)
}

func TestLoadDefaultsPlanID(t *testing.T) {
	s, err := Load(context.Background(), newSettingsRepo(t))
	require.NoError(t, err)
	require.Equal(t, DefaultPlanID, s.PlanID)
}
