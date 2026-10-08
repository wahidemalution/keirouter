package budget

import (
	"context"
	"testing"
	"time"

	"github.com/mydisha/keirouter/backend/internal/config"
	"github.com/mydisha/keirouter/backend/internal/store"
	"github.com/stretchr/testify/require"
)

func newTestEngine(t *testing.T) (*Engine, *store.DB) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: ":memory:"}, t.TempDir())
	require.NoError(t, err)
	require.NoError(t, db.Migrate(ctx))
	require.NoError(t, db.Tenants().EnsureDefault(ctx))
	t.Cleanup(func() { _ = db.Close() })
	return New(db.Budgets(), db.Usage()), db
}

func createBudget(t *testing.T, db *store.DB, b store.Budget) {
	t.Helper()
	if b.TenantID == "" {
		b.TenantID = store.DefaultTenantID
	}
	if b.Period == "" {
		b.Period = "total"
	}
	if b.CreatedAt.IsZero() {
		b.CreatedAt = time.Now()
	}
	if b.UpdatedAt.IsZero() {
		b.UpdatedAt = time.Now()
	}
	require.NoError(t, db.Budgets().Create(context.Background(), b))
}

func TestMinKeyBalanceGate(t *testing.T) {
	eng, db := newTestEngine(t)
	ctx := context.Background()

	cases := []struct {
		name        string
		scopeKind   store.BudgetScope
		scopeID     string
		limitMicros int64
		hardCutoff  bool
		wantAllowed bool
	}{
		{"starter 0.01 key blocked", store.ScopeAPIKey, "key-low", 10_000, true, false},
		{"exactly 0.05 key allowed", store.ScopeAPIKey, "key-ok", minKeyBalanceMicros, true, true},
		{"above 0.05 key allowed", store.ScopeAPIKey, "key-high", 100_000, true, true},
		{"advisory low key not gated", store.ScopeAPIKey, "key-adv", 10_000, false, true},
		{"low project budget not gated", store.ScopeProject, "proj-low", 10_000, true, true},
		{"low tenant budget not gated", store.ScopeTenant, "tenant-low", 10_000, true, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			createBudget(t, db, store.Budget{
				ID:          "b-" + tc.scopeID,
				ScopeKind:   tc.scopeKind,
				ScopeID:     tc.scopeID,
				LimitMicros: tc.limitMicros,
				HardCutoff:  tc.hardCutoff,
			})

			scope := Scope{}
			switch tc.scopeKind {
			case store.ScopeAPIKey:
				scope.APIKeyID = tc.scopeID
			case store.ScopeProject:
				scope.ProjectID = tc.scopeID
			case store.ScopeTenant:
				scope.TenantID = tc.scopeID
			}

			err := eng.Reserve(ctx, scope, 1)
			if !tc.wantAllowed {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestNoBudgetMeansUnlimited(t *testing.T) {
	eng, _ := newTestEngine(t)
	dec, err := eng.Check(context.Background(), Scope{APIKeyID: "key-no-budget"})
	require.NoError(t, err)
	require.True(t, dec.Allowed)
}
