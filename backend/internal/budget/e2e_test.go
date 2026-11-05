package budget

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand/v2"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/config"
	"github.com/mydisha/keirouter/backend/internal/core"
	"github.com/mydisha/keirouter/backend/internal/meter"
	"github.com/mydisha/keirouter/backend/internal/store"
)

// openE2EStore opens SQLite in memory, or Postgres in a fresh schema when
// KEIROUTER_AUDIT_PG_DSN is set, with the spend rollup on.
func openE2EStore(t *testing.T) *store.DB {
	t.Helper()
	ctx := context.Background()
	cfg := config.DatabaseConfig{Driver: "sqlite", DSN: ":memory:"}
	if base := os.Getenv("KEIROUTER_AUDIT_PG_DSN"); base != "" {
		schema := fmt.Sprintf("e2e_%d", time.Now().UnixNano())
		admin, err := sql.Open("pgx", base)
		require.NoError(t, err)
		_, err = admin.Exec("CREATE SCHEMA " + schema)
		require.NoError(t, err)
		_ = admin.Close()
		cfg = config.DatabaseConfig{Driver: "postgres", DSN: base + "&search_path=" + schema}
	}
	db, err := store.Open(ctx, cfg, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.Migrate(ctx))
	require.NoError(t, db.Tenants().EnsureDefault(ctx))
	_, err = db.EnsureSpendRollup(ctx, true)
	require.NoError(t, err)
	return db
}

// TestSharedCounterNeverBelowUsageTable runs the real meter (async writer),
// store (rollup + triggers) and redis counters together. Counters expire
// constantly while requests are metered and checked, so seeds race with
// writes. Whenever traffic pauses, every budget's spend from the counters must
// be at least the usage table's, and equal it once counters re-seed.
func TestSharedCounterNeverBelowUsageTable(t *testing.T) {
	db := openE2EStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	now := time.Now()

	scope := Scope{TenantID: store.DefaultTenantID, ProjectID: "p1", APIKeyID: "k1"}
	for _, b := range []store.Budget{
		{ID: "bk", ScopeKind: store.ScopeAPIKey, ScopeID: "k1", Period: "total"},
		{ID: "bp", ScopeKind: store.ScopeProject, ScopeID: "p1", Period: "monthly"},
		{ID: "bt", ScopeKind: store.ScopeTenant, ScopeID: store.DefaultTenantID, Period: "daily"},
	} {
		b.TenantID, b.LimitMicros, b.AlertPct, b.HardCutoff, b.CreatedAt, b.UpdatedAt = store.DefaultTenantID, 1<<60, 80, true, now, now
		require.NoError(t, db.Budgets().Create(ctx, b))
	}

	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })
	eng := New(db.Budgets(), db.Usage())
	eng.EnableSharedCounter(NewRedisCounter(client, time.Second, nil), time.Minute, nil)
	t.Cleanup(eng.Close)

	m := meter.New(db.Usage(), map[string]meter.Price{"openai": {InputPerM: 3, OutputPerM: 15}}, nil)
	m.EnableAsync(meter.AsyncConfig{Enabled: true, BatchSize: 7, FlushInterval: 10 * time.Millisecond, QueueSize: 10_000})
	m.AddUsageTracker(eng)
	m.StartAsync(ctx)
	defer m.Close(5 * time.Second)

	var recorded atomic.Int64
	var maxOver int64
	for round := 0; round < 8; round++ {
		stop := make(chan struct{})
		var bg sync.WaitGroup
		// Expire every counter, and any half-done seed, every few milliseconds.
		bg.Add(1)
		go func() {
			defer bg.Done()
			for {
				select {
				case <-stop:
					return
				case <-time.After(3 * time.Millisecond):
					mr.FastForward(61 * time.Second)
				}
			}
		}()
		for c := 0; c < 2; c++ {
			bg.Add(1)
			go func() {
				defer bg.Done()
				for {
					select {
					case <-stop:
						return
					default:
						if _, err := eng.Check(ctx, scope); err != nil {
							t.Error(err)
							return
						}
					}
				}
			}()
		}
		var writers sync.WaitGroup
		for w := 0; w < 4; w++ {
			writers.Add(1)
			go func(w int) {
				defer writers.Done()
				rng := rand.New(rand.NewPCG(uint64(round), uint64(w)))
				for i := 0; i < 50; i++ {
					_, err := m.Record(ctx, meter.Event{
						RequestID: fmt.Sprintf("r%d-%d-%d", round, w, i), TenantID: scope.TenantID,
						ProjectID: scope.ProjectID, APIKeyID: scope.APIKeyID, Provider: "openai", Model: "gpt-x",
						Usage: core.Usage{PromptTokens: 1 + rng.IntN(20_000), CompletionTokens: rng.IntN(4_000)},
					})
					if err != nil {
						t.Error(err)
						return
					}
					recorded.Add(1)
					if rng.IntN(4) == 0 {
						time.Sleep(time.Millisecond)
					}
				}
			}(w)
		}
		writers.Wait()
		close(stop)
		bg.Wait()

		// Quiesce: every row written, every increment applied to redis.
		require.Eventually(t, func() bool {
			var n int64
			_ = db.SQL().QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_records").Scan(&n)
			return n == recorded.Load()
		}, 5*time.Second, 2*time.Millisecond)
		require.Eventually(t, func() bool { return len(eng.deltas) == 0 }, 2*time.Second, time.Millisecond)
		time.Sleep(30 * time.Millisecond)
		require.Eventually(t, func() bool {
			eng.pendMu.Lock()
			defer eng.pendMu.Unlock()
			return len(eng.pending) == 0
		}, 2*time.Second, time.Millisecond, "pending spend is released once rows are written")

		dec, err := eng.Check(ctx, scope)
		require.NoError(t, err)
		require.Len(t, dec.BudgetUsage, 3)
		for _, bu := range dec.BudgetUsage {
			wantCost, wantTokens, err := db.Usage().SpendAndTokens(ctx, bu.Budget.ScopeKind, bu.Budget.ScopeID,
				PeriodStart(bu.Budget.Period, time.Now()))
			require.NoError(t, err)
			require.Positive(t, wantCost)
			require.GreaterOrEqual(t, bu.CostMicros, wantCost, "round %d %s: counter below the usage table", round, bu.Budget.ID)
			require.GreaterOrEqual(t, bu.TokenCount, wantTokens, "round %d %s: tokens below the usage table", round, bu.Budget.ID)
			maxOver = max(maxOver, bu.CostMicros-wantCost)
		}
	}
	require.Zero(t, eng.Stats().Dropped)

	// Counters re-seeded from the table match it exactly.
	mr.FastForward(2 * time.Minute)
	dec, err := eng.Check(ctx, scope)
	require.NoError(t, err)
	for _, bu := range dec.BudgetUsage {
		wantCost, wantTokens, err := db.Usage().SpendAndTokens(ctx, bu.Budget.ScopeKind, bu.Budget.ScopeID,
			PeriodStart(bu.Budget.Period, time.Now()))
		require.NoError(t, err)
		require.Equal(t, wantCost, bu.CostMicros, bu.Budget.ID)
		require.Equal(t, wantTokens, bu.TokenCount, bu.Budget.ID)
	}
	bad, err := db.CheckSpendRollup(ctx)
	require.NoError(t, err)
	require.Zero(t, bad)
	t.Logf("rows=%d max transient overcount=%d micros", recorded.Load(), maxOver)
}

// TestBackToBackRequestsStopAtLimit meters requests one after another faster
// than the usage writer flushes. Each must see the spend of the ones before
// it, so exactly the requests the limit can fund get through.
func TestBackToBackRequestsStopAtLimit(t *testing.T) {
	for _, mode := range []string{"usage table", "redis counters"} {
		t.Run(mode, func(t *testing.T) {
			db := openE2EStore(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			now := time.Now()
			scope := Scope{TenantID: store.DefaultTenantID, APIKeyID: "k1"}
			require.NoError(t, db.Budgets().Create(ctx, store.Budget{ID: "bk", TenantID: store.DefaultTenantID,
				ScopeKind: store.ScopeAPIKey, ScopeID: "k1", Period: "total", LimitMicros: 1_000_000,
				AlertPct: 80, HardCutoff: true, CreatedAt: now, UpdatedAt: now}))

			eng := New(db.Budgets(), db.Usage())
			if mode == "redis counters" {
				mr := miniredis.RunT(t)
				client := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
				t.Cleanup(func() { _ = client.Close() })
				eng.EnableSharedCounter(NewRedisCounter(client, time.Second, nil), time.Minute, nil)
				t.Cleanup(eng.Close)
			}
			m := meter.New(db.Usage(), map[string]meter.Price{"openai": {InputPerM: 3}}, nil)
			m.EnableAsync(meter.AsyncConfig{Enabled: true, BatchSize: 100, FlushInterval: time.Second, QueueSize: 10_000})
			m.AddUsageTracker(eng)
			m.StartAsync(ctx)

			// Each request costs $0.03 (10k input tokens at $3/M); $1 funds
			// 34: the 34th starts at $0.99 spent.
			allowed := 0
			for i := 0; i < 60; i++ {
				if err := eng.Reserve(ctx, scope, 0); err != nil {
					break
				}
				cost, err := m.Record(ctx, meter.Event{RequestID: fmt.Sprint(i), TenantID: scope.TenantID,
					APIKeyID: "k1", Provider: "openai", Model: "m", Usage: core.Usage{PromptTokens: 10_000}})
				require.NoError(t, err)
				require.Equal(t, int64(30_000), cost)
				eng.Confirm(scope, cost)
				allowed++
			}
			require.Equal(t, 34, allowed)

			m.Close(5 * time.Second)
			spent, _, err := db.Usage().SpendAndTokens(ctx, store.ScopeAPIKey, "k1", time.Time{})
			require.NoError(t, err)
			require.Equal(t, int64(1_020_000), spent)
			require.Eventually(t, func() bool {
				dec, err := eng.Check(ctx, scope)
				return err == nil && !dec.Allowed && dec.BudgetUsage[0].CostMicros == 1_020_000
			}, 2*time.Second, 5*time.Millisecond, "pending released into the written spend, not double counted")
		})
	}
}
