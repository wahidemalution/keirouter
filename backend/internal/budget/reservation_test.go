package budget

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/core"
	"github.com/mydisha/keirouter/backend/internal/store"
)

func keyBudget(t *testing.T, db *store.DB, keyID string, limitMicros int64) Scope {
	t.Helper()
	createBudget(t, db, store.Budget{
		ID: "b-" + keyID, ScopeKind: store.ScopeAPIKey, ScopeID: keyID,
		LimitMicros: limitMicros, HardCutoff: true,
	})
	return Scope{TenantID: store.DefaultTenantID, APIKeyID: keyID}
}

var usageSeq atomic.Int64

func recordSpend(t *testing.T, db *store.DB, keyID string, micros int64) {
	t.Helper()
	require.NoError(t, db.Usage().Record(context.Background(), store.UsageRecord{
		ID:         fmt.Sprintf("u-%s-%d", keyID, usageSeq.Add(1)),
		TenantID:   store.DefaultTenantID,
		APIKeyID:   keyID,
		Provider:   "openai",
		Model:      "gpt-4o",
		Status:     "success",
		CostMicros: micros,
		CostNanos:  micros * 1000,
		CreatedAt:  time.Now().UTC(),
	}))
}

func requireBudgetBlocked(t *testing.T, err error, contains string) {
	t.Helper()
	require.Error(t, err)
	var pe *core.ProviderError
	require.ErrorAs(t, err, &pe)
	require.Equal(t, core.ErrBudgetBlocked, pe.Kind)
	require.Contains(t, pe.Message, contains)
}

// Production key 9a565488 had a $1.66 limit and $0.0007 left, yet started a
// request because the gate compared the limit, not the remaining balance.
func TestMinBalanceGateUsesRemainingBalance(t *testing.T) {
	eng, db := newTestEngine(t)
	ctx := context.Background()

	low := keyBudget(t, db, "key-nearly-empty", 1_663_000)
	recordSpend(t, db, "key-nearly-empty", 1_662_300) // $0.0007 left
	_, err := eng.Reserve(ctx, low, 1)
	requireBudgetBlocked(t, err, "minimum API key balance")

	ok := keyBudget(t, db, "key-enough", 5_000_000)
	recordSpend(t, db, "key-enough", 4_900_000) // $0.10 left
	_, err = eng.Reserve(ctx, ok, 1)
	require.NoError(t, err)
}

// Production key b163513a had $0.62 left and sent a 442k-token Opus prompt
// billed at $1.82. The estimate must refuse it before dispatch.
func TestReserveRefusesEstimateAboveRemaining(t *testing.T) {
	eng, db := newTestEngine(t)
	ctx := context.Background()

	scope := keyBudget(t, db, "key-opus", 9_948_500)
	recordSpend(t, db, "key-opus", 9_330_400) // $0.6181 left

	_, err := eng.Reserve(ctx, scope, 1_823_100)
	requireBudgetBlocked(t, err, "insufficient balance")
	require.Zero(t, eng.getReserved(store.ScopeAPIKey, "key-opus"), "a refused request reserves nothing")

	id, err := eng.Reserve(ctx, scope, 500_000)
	require.NoError(t, err)
	require.NotZero(t, id)
	require.Equal(t, int64(500_000), eng.getReserved(store.ScopeAPIKey, "key-opus"))
}

// Production saw one key send 63 requests in the same second. Every request
// must see the reservations of the others, so the burst cannot overshoot.
func TestConcurrentBurstStaysWithinLimit(t *testing.T) {
	eng, db := newTestEngine(t)
	ctx := context.Background()
	scope := keyBudget(t, db, "key-burst", 1_000_000)

	const burst = 63
	const each = 100_000
	var wg sync.WaitGroup
	var admitted atomic.Int64
	start := make(chan struct{})
	for range burst {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := eng.Reserve(ctx, scope, each); err == nil {
				admitted.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	// $1.00 fits exactly ten $0.10 requests.
	require.Equal(t, int64(10), admitted.Load())
	require.LessOrEqual(t, eng.getReserved(store.ScopeAPIKey, "key-burst"), int64(1_000_000))
}

// The old releaseAll dropped every reservation of a key whenever any one of
// its requests finished, so concurrent requests stopped counting.
func TestReleaseFreesOnlyOwnReservation(t *testing.T) {
	eng, db := newTestEngine(t)
	ctx := context.Background()
	scope := keyBudget(t, db, "key-two", 1_000_000)

	first, err := eng.Reserve(ctx, scope, 400_000)
	require.NoError(t, err)
	second, err := eng.Reserve(ctx, scope, 400_000)
	require.NoError(t, err)

	s := scope
	s.Reservation = first
	eng.Release(s)
	eng.Release(s) // idempotent
	require.Equal(t, int64(400_000), eng.getReserved(store.ScopeAPIKey, "key-two"))

	// Only $0.60 is free while the second request is in flight.
	_, err = eng.Reserve(ctx, scope, 700_000)
	requireBudgetBlocked(t, err, "insufficient balance")

	s.Reservation = second
	eng.Release(s)
	require.Zero(t, eng.getReserved(store.ScopeAPIKey, "key-two"))
}

func pendingRow(keyID string, micros int64) store.UsageRecord {
	return store.UsageRecord{TenantID: store.DefaultTenantID, APIKeyID: keyID,
		CostMicros: micros, CostNanos: micros * 1000, CreatedAt: time.Now().UTC()}
}

// meter.Record marks the row pending before Confirm runs. Confirm then drops
// the reservation, and the cost keeps counting through pending until the row
// is written, without being counted twice.
func TestConfirmHandsCostToPending(t *testing.T) {
	eng, db := newTestEngine(t)
	ctx := context.Background()
	scope := keyBudget(t, db, "key-hold", 1_000_000)

	id, err := eng.Reserve(ctx, scope, 100_000)
	require.NoError(t, err)
	s := scope
	s.Reservation = id
	row := pendingRow("key-hold", 900_000) // estimate ran low; the real bill was $0.90
	eng.UsagePending(row)
	eng.Confirm(s, 900_000)
	eng.Release(s) // backstop release after Confirm is harmless
	require.Zero(t, eng.getReserved(store.ScopeAPIKey, "key-hold"))

	dec, err := eng.Check(ctx, scope)
	require.NoError(t, err)
	require.Equal(t, int64(900_000), dec.BudgetUsage[0].CostMicros, "counted once, through pending")
	_, err = eng.Reserve(ctx, scope, 200_000)
	requireBudgetBlocked(t, err, "insufficient balance")

	// The row is written: spend now comes from the table, still once.
	recordSpend(t, db, "key-hold", 900_000)
	eng.UsagePersisted(row)
	dec, err = eng.Check(ctx, scope)
	require.NoError(t, err)
	require.Equal(t, int64(900_000), dec.BudgetUsage[0].CostMicros)
	_, err = eng.Reserve(ctx, scope, 200_000)
	requireBudgetBlocked(t, err, "insufficient balance") // $0.10 left
	_, err = eng.Reserve(ctx, scope, 50_000)
	require.NoError(t, err)
}

// Several pipeline paths release the reservation and then confirm the cost of
// the failed attempt. The metered row still counts through pending.
func TestConfirmAfterReleaseStillCountsCost(t *testing.T) {
	eng, db := newTestEngine(t)
	ctx := context.Background()
	scope := keyBudget(t, db, "key-late", 1_000_000)

	id, err := eng.Reserve(ctx, scope, 100_000)
	require.NoError(t, err)
	s := scope
	s.Reservation = id
	eng.Release(s)
	eng.UsagePending(pendingRow("key-late", 300_000))
	eng.Confirm(s, 300_000)
	require.Zero(t, eng.getReserved(store.ScopeAPIKey, "key-late"))

	dec, err := eng.Check(ctx, scope)
	require.NoError(t, err)
	require.Equal(t, int64(300_000), dec.BudgetUsage[0].CostMicros)
}

// A row written right after Check's unlocked spend read is missing from that
// read and has already left pending. Reserve must still count it.
func TestReserveCountsRowWrittenDuringSpendRead(t *testing.T) {
	fs := &fakeStores{budgets: []store.Budget{monthlyBudget(1_000_000)}, spent: 400_000}
	hs := &seedHookStores{fakeStores: fs}
	eng := &Engine{budgets: fs, usage: hs, budgetCache: map[string]*budgetCacheEntry{}}

	row := store.UsageRecord{TenantID: "t1", APIKeyID: "k1", CostMicros: 500_000, CostNanos: 500_000_000}
	eng.UsagePending(row)
	hs.onRead = func() {
		hs.onRead = nil
		fs.mu.Lock()
		fs.spent += 500_000
		fs.mu.Unlock()
		eng.UsagePersisted(row)
	}

	// $0.10 is really left; the stale read alone would show $0.60.
	_, err := eng.Reserve(context.Background(), keyScope, 200_000)
	requireBudgetBlocked(t, err, "insufficient balance")
	_, err = eng.Reserve(context.Background(), keyScope, 50_000)
	require.NoError(t, err)
}

// A key with two budgets (daily and total) holds one reservation, not one per
// budget.
func TestReserveHoldsOncePerScope(t *testing.T) {
	eng, db := newTestEngine(t)
	ctx := context.Background()
	scope := keyBudget(t, db, "key-two-budgets", 1_000_000)
	createBudget(t, db, store.Budget{ID: "b-key-two-budgets-daily", ScopeKind: store.ScopeAPIKey,
		ScopeID: "key-two-budgets", Period: "daily", LimitMicros: 1_000_000, HardCutoff: true})

	id, err := eng.Reserve(ctx, scope, 300_000)
	require.NoError(t, err)
	require.Equal(t, int64(300_000), eng.getReserved(store.ScopeAPIKey, "key-two-budgets"))
	_, err = eng.Reserve(ctx, scope, 600_000)
	require.NoError(t, err, "$0.70 is free, not $0.40")
	s := scope
	s.Reservation = id
	eng.Release(s)
	require.Equal(t, int64(600_000), eng.getReserved(store.ScopeAPIKey, "key-two-budgets"))
}

// A request path that never releases must not block its key forever.
func TestUnreleasedReservationExpires(t *testing.T) {
	eng, db := newTestEngine(t)
	ctx := context.Background()
	scope := keyBudget(t, db, "key-leak", 1_000_000)

	_, err := eng.Reserve(ctx, scope, 960_000)
	require.NoError(t, err)
	_, err = eng.Reserve(ctx, scope, 100_000)
	requireBudgetBlocked(t, err, "minimum API key balance")

	expireAll(eng)
	_, err = eng.Reserve(ctx, scope, 100_000)
	require.NoError(t, err)
}

// An unknown estimate still reserves a marker and only the minimum-balance
// gate applies.
func TestUnknownEstimateUsesMinimumGateOnly(t *testing.T) {
	eng, db := newTestEngine(t)
	ctx := context.Background()
	scope := keyBudget(t, db, "key-unpriced", 1_000_000)
	recordSpend(t, db, "key-unpriced", 940_000) // $0.06 left

	id, err := eng.Reserve(ctx, scope, 0)
	require.NoError(t, err)
	require.NotZero(t, id)
	require.Equal(t, int64(1), eng.getReserved(store.ScopeAPIKey, "key-unpriced"))
}

// Project and tenant budgets refuse an unaffordable estimate too, but the
// $0.05 minimum applies to API keys only.
func TestEstimateCheckedAgainstTenantBudget(t *testing.T) {
	eng, db := newTestEngine(t)
	ctx := context.Background()
	createBudget(t, db, store.Budget{
		ID: "b-tenant", ScopeKind: store.ScopeTenant, ScopeID: "tenant-x",
		LimitMicros: 30_000, HardCutoff: true,
	})
	scope := Scope{TenantID: "tenant-x"}

	_, err := eng.Reserve(ctx, scope, 20_000)
	require.NoError(t, err)
	_, err = eng.Reserve(ctx, scope, 20_000)
	requireBudgetBlocked(t, err, "insufficient balance")
}

func expireAll(e *Engine) {
	e.mu.Lock()
	defer e.mu.Unlock()
	past := time.Now().Add(-time.Second)
	for _, r := range e.entries {
		r.expires = past
	}
	e.lastSweep = time.Time{}
}
