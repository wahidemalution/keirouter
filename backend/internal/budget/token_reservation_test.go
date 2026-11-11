package budget

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/core"
	"github.com/mydisha/keirouter/backend/internal/store"
)

func tokenBudget(id, keyID string, limit int64) store.Budget {
	return store.Budget{ID: id, ScopeKind: store.ScopeAPIKey, ScopeID: keyID, LimitTokens: limit, HardCutoff: true}
}

func tokenEstimate(prompt, maxOut int64) Estimate {
	return Estimate{TokenMetered: true, PromptTokens: prompt, MaxOutputTokens: maxOut}
}

func reservedTokens(e *Engine, keyID string) int64 {
	return e.getReservedTokens(store.ScopeAPIKey, keyID)
}

// raceReservations fires n concurrent token reservations of prompt+maxOut
// against scope and returns the tokens granted in total.
func raceReservations(t *testing.T, e *Engine, scope Scope, n int, prompt, maxOut int64) (granted int64, ok int32) {
	t.Helper()
	var wg sync.WaitGroup
	results := make([]Reservation, n)
	errs := make([]error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results[i], errs[i] = e.Reserve(context.Background(), scope, tokenEstimate(prompt, maxOut))
		}()
	}
	close(start)
	wg.Wait()
	for i := range n {
		if errs[i] != nil {
			require.Equal(t, CodeInsufficientTokens, core.AsProviderError(errs[i]).Code)
			continue
		}
		require.Positive(t, results[i].OutputCap)
		granted += prompt + results[i].OutputCap
		ok++
	}
	return granted, ok
}

func TestTokenReservationBlocksConcurrentOvershoot(t *testing.T) {
	eng, db := newTestEngine(t)
	createBudget(t, db, tokenBudget("b-tok", "key-tok", 10_000))
	scope := Scope{APIKeyID: "key-tok"}

	granted, admitted := raceReservations(t, eng, scope, 64, 100, 900)
	require.LessOrEqual(t, granted, int64(10_000), "reserved tokens exceed the limit")
	require.Equal(t, granted, reservedTokens(eng, "key-tok"))
	require.Positive(t, admitted)
	require.Less(t, admitted, int32(64), "some requests must be refused")
}

func TestTokenReservationBlocksConcurrentOvershootSharedCounter(t *testing.T) {
	mr := miniredis.RunT(t)
	fs := &fakeStores{budgets: []store.Budget{{ID: "b1", TenantID: "t1", ScopeKind: store.ScopeAPIKey,
		ScopeID: "k1", LimitTokens: 10_000, Period: "total", HardCutoff: true}}, tokens: 4_000}
	e := sharedEngine(t, mr, fs)

	granted, _ := raceReservations(t, e, keyScope, 32, 100, 400)
	require.LessOrEqual(t, granted, int64(6_000))
	require.Equal(t, granted, reservedTokens(e, "k1"))
}

func TestTokenReservationReleasedPerRequest(t *testing.T) {
	eng, db := newTestEngine(t)
	eng.SetTokenHoldTail(0)
	createBudget(t, db, tokenBudget("b-tok", "key-tok", 1_000_000))
	ctx := context.Background()
	scope := Scope{APIKeyID: "key-tok"}

	a, err := eng.Reserve(ctx, scope, tokenEstimate(100, 1_000))
	require.NoError(t, err)
	b, err := eng.Reserve(ctx, scope, tokenEstimate(200, 2_000))
	require.NoError(t, err)
	require.NotEqual(t, a.ID, b.ID)
	require.Equal(t, int64(3_300), reservedTokens(eng, "key-tok"))

	eng.Confirm(Scope{APIKeyID: "key-tok", Reservation: a.ID}, 0)
	require.Equal(t, int64(2_200), reservedTokens(eng, "key-tok"), "only a's hold is released")
	eng.Release(Scope{APIKeyID: "key-tok", Reservation: a.ID})
	require.Equal(t, int64(2_200), reservedTokens(eng, "key-tok"), "a second release is a no-op")

	eng.Release(Scope{APIKeyID: "key-tok", Reservation: b.ID})
	require.Zero(t, reservedTokens(eng, "key-tok"))
}

func TestTokenReservationKeptForTailAfterConfirm(t *testing.T) {
	eng, db := newTestEngine(t)
	eng.SetTokenHoldTail(150 * time.Millisecond)
	createBudget(t, db, tokenBudget("b-tok", "key-tok", 1_000_000))
	scope := Scope{APIKeyID: "key-tok"}

	res, err := eng.Reserve(context.Background(), scope, tokenEstimate(100, 900))
	require.NoError(t, err)
	scope.Reservation = res.ID
	eng.Confirm(scope, 0)
	require.Equal(t, int64(1_000), reservedTokens(eng, "key-tok"), "held until the usage row is visible")
	eng.Release(scope)
	require.Equal(t, int64(1_000), reservedTokens(eng, "key-tok"), "release after confirm keeps the tail")
	require.Eventually(t, func() bool { return reservedTokens(eng, "key-tok") == 0 }, 2*time.Second, 10*time.Millisecond)
}

func TestTokenRequestRefusedWhenEstimateExceedsRemaining(t *testing.T) {
	eng, db := newTestEngine(t)
	createBudget(t, db, tokenBudget("b-tok", "key-tok", 10_000))
	ctx := context.Background()
	require.NoError(t, db.Usage().Record(ctx, store.UsageRecord{
		ID: "u1", TenantID: store.DefaultTenantID, APIKeyID: "key-tok", Provider: "p", Model: "m",
		PromptTokens: 9_000, CompletionTokens: 950, CreatedAt: time.Now(),
	}))
	scope := Scope{APIKeyID: "key-tok"}

	_, err := eng.Reserve(ctx, scope, tokenEstimate(100, 500))
	require.Error(t, err)
	pe := core.AsProviderError(err)
	require.Equal(t, core.ErrBudgetBlocked, pe.Kind)
	require.Equal(t, CodeInsufficientTokens, pe.Code)
	require.Contains(t, pe.Message, "50 token")
	require.Zero(t, reservedTokens(eng, "key-tok"), "a refused request holds nothing")

	res, err := eng.Reserve(ctx, scope, tokenEstimate(20, 500))
	require.NoError(t, err)
	require.Equal(t, int64(30), res.OutputCap, "output clamped to remaining - prompt")

	_, err = eng.Reserve(ctx, scope, tokenEstimate(1, 500))
	require.Error(t, err)
	require.Equal(t, CodeInsufficientTokens, core.AsProviderError(err).Code)
	require.Contains(t, core.AsProviderError(err).Message, "0 token")
}

func TestTokenOutputCapUnclampedWhenRoomRemains(t *testing.T) {
	eng, db := newTestEngine(t)
	createBudget(t, db, tokenBudget("b-tok", "key-tok", 1_000_000))
	res, err := eng.Reserve(context.Background(), Scope{APIKeyID: "key-tok"}, tokenEstimate(100, 4_096))
	require.NoError(t, err)
	require.Equal(t, int64(4_096), res.OutputCap)
}

func TestPAYGNoTokenReservationOrClamp(t *testing.T) {
	eng, db := newTestEngine(t)
	createBudget(t, db, store.Budget{ID: "b-payg", ScopeKind: store.ScopeAPIKey, ScopeID: "key-payg",
		LimitMicros: 5_000_000, LimitTokens: 1_000, HardCutoff: true})
	res, err := eng.Reserve(context.Background(), Scope{APIKeyID: "key-payg"}, Estimate{Micros: 10})
	require.NoError(t, err)
	require.Zero(t, res.OutputCap)
	require.Zero(t, reservedTokens(eng, "key-payg"))
	require.Equal(t, int64(10), eng.getReserved(store.ScopeAPIKey, "key-payg"))
}

func TestReleaseDoesNotClearOtherReservations(t *testing.T) {
	eng, db := newTestEngine(t)
	createBudget(t, db, store.Budget{ID: "b-payg", ScopeKind: store.ScopeAPIKey, ScopeID: "key-payg",
		LimitMicros: 5_000_000, HardCutoff: true})
	ctx := context.Background()
	scope := Scope{APIKeyID: "key-payg"}

	a, err := eng.Reserve(ctx, scope, Estimate{Micros: 1_000})
	require.NoError(t, err)
	b, err := eng.Reserve(ctx, scope, Estimate{Micros: 2_000})
	require.NoError(t, err)

	eng.Confirm(Scope{APIKeyID: "key-payg", Reservation: a.ID}, 900)
	require.Equal(t, int64(2_000), eng.getReserved(store.ScopeAPIKey, "key-payg"))
	eng.Release(Scope{APIKeyID: "key-payg"})
	require.Equal(t, int64(2_000), eng.getReserved(store.ScopeAPIKey, "key-payg"),
		"a scope without a hold releases nothing")
	eng.Release(Scope{APIKeyID: "key-payg", Reservation: b.ID})
	require.Zero(t, eng.getReserved(store.ScopeAPIKey, "key-payg"))
}

func TestReservedTokensCountInCheck(t *testing.T) {
	eng, db := newTestEngine(t)
	createBudget(t, db, tokenBudget("b-tok", "key-tok", 1_000))
	ctx := context.Background()
	_, err := eng.Reserve(ctx, Scope{APIKeyID: "key-tok"}, tokenEstimate(100, 900))
	require.NoError(t, err)
	dec, err := eng.Check(ctx, Scope{APIKeyID: "key-tok"})
	require.NoError(t, err)
	require.False(t, dec.Allowed, "in-flight tokens fill the budget")
}

func TestMinKeyBalanceGateTokenBudgetWithSmallUSDLimit(t *testing.T) {
	eng, db := newTestEngine(t)
	createBudget(t, db, store.Budget{ID: "b-mixed", ScopeKind: store.ScopeAPIKey, ScopeID: "key-mixed",
		LimitTokens: 10_000_000, LimitMicros: 10_000, HardCutoff: true})
	_, err := eng.Reserve(context.Background(), Scope{APIKeyID: "key-mixed"}, Estimate{Micros: 1})
	require.Error(t, err, "only token-only budgets skip the minimum balance")
}

func TestTokenMeteredWithoutTokenBudgetRefused(t *testing.T) {
	eng, db := newTestEngine(t)
	ctx := context.Background()
	_, err := eng.Reserve(ctx, Scope{APIKeyID: "key-none"}, tokenEstimate(10, 100))
	require.Error(t, err, "no budget at all")
	require.Equal(t, CodeInsufficientTokens, core.AsProviderError(err).Code)

	createBudget(t, db, store.Budget{ID: "b-usd", ScopeKind: store.ScopeAPIKey, ScopeID: "key-usd",
		LimitMicros: 5_000_000, HardCutoff: true})
	_, err = eng.Reserve(ctx, Scope{APIKeyID: "key-usd"}, tokenEstimate(10, 100))
	require.Error(t, err, "a USD budget is no token limit")
	require.Equal(t, CodeInsufficientTokens, core.AsProviderError(err).Code)
}

// With the tail off, a confirmed token request is still counted through its
// pending usage row until the writer stores it, so the next request cannot
// be granted the same tokens.
func TestConfirmedTokensCountThroughPending(t *testing.T) {
	eng, db := newTestEngine(t)
	eng.SetTokenHoldTail(0)
	createBudget(t, db, tokenBudget("b-tok", "key-tok", 1_000))
	ctx := context.Background()
	scope := Scope{APIKeyID: "key-tok"}

	res, err := eng.Reserve(ctx, scope, tokenEstimate(100, 900))
	require.NoError(t, err)
	row := store.UsageRecord{TenantID: store.DefaultTenantID, APIKeyID: "key-tok",
		PromptTokens: 100, CompletionTokens: 850, CreatedAt: time.Now().UTC()}
	eng.UsagePending(row)
	eng.Confirm(Scope{APIKeyID: "key-tok", Reservation: res.ID}, 0)
	require.Zero(t, reservedTokens(eng, "key-tok"))

	got, err := eng.Reserve(ctx, scope, tokenEstimate(20, 500))
	require.NoError(t, err)
	require.Equal(t, int64(30), got.OutputCap, "1000 - 950 pending - 20 prompt")
	eng.Release(Scope{APIKeyID: "key-tok", Reservation: got.ID})

	eng.UsageDiscarded(row)
	got, err = eng.Reserve(ctx, scope, tokenEstimate(20, 500))
	require.NoError(t, err)
	require.Equal(t, int64(500), got.OutputCap, "a dropped row stops counting")
}

// A token key whose recorded and pending tokens use up the budget is refused
// with the token error, not the generic budget one.
func TestTokenExhaustedThroughPendingUsesTokenError(t *testing.T) {
	eng, db := newTestEngine(t)
	createBudget(t, db, tokenBudget("b-tok", "key-tok", 1_000))
	eng.UsagePending(store.UsageRecord{TenantID: store.DefaultTenantID, APIKeyID: "key-tok",
		PromptTokens: 400, CompletionTokens: 600, CreatedAt: time.Now().UTC()})
	_, err := eng.Reserve(context.Background(), Scope{APIKeyID: "key-tok"}, tokenEstimate(10, 100))
	require.Error(t, err)
	require.Equal(t, CodeInsufficientTokens, core.AsProviderError(err).Code)
}
