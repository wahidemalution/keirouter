// Package budget enforces spend limits before a request is dispatched.
//
// Budgets are defined per scope (tenant, project, or API key) with a hard cap
// in micros of USD over a rolling period. Before each request the engine sums
// the scope's spend in the current period plus in-flight reservations; a
// request is rejected with ErrBudgetBlocked (not fallbackable) when its
// estimated cost does not fit the remaining balance of a hard-cutoff budget,
// or when an API key holds less than the minimum balance. Alert thresholds let
// the UI warn before the cutoff.
package budget

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mydisha/keirouter/backend/internal/core"
	"github.com/mydisha/keirouter/backend/internal/store"
)

// reservation holds one request's spend that has been budget-checked but is
// not yet visible in the usage table. Each request owns its own entry, so a
// finishing request releases only its own amount.
type reservation struct {
	keys    []string // reservationKey of every hard-cutoff budget scope it counts against, once each
	micros  int64
	expires time.Time
	// tokenKeys are the scopes holding tokens: those with a hard-cutoff token
	// budget, for a token-metered request only.
	tokenKeys []string
	tokens    int64
	// confirmed marks a request whose usage is metered while its tokens stay
	// held for the tail; Release no longer acts on it.
	confirmed bool
}

// CodeInsufficientTokens is the client error code for a token-metered request
// that does not fit in what remains of its token budget.
const CodeInsufficientTokens = "insufficient_tokens"

// defaultTokenHoldTail covers the async usage writer's default 1s flush.
const defaultTokenHoldTail = 3 * time.Second

// reservationMaxAge bounds how long an unreleased reservation is honoured. A
// request path that forgets to release would otherwise block its key until
// restart; an hour comfortably outlives any real request.
const reservationMaxAge = time.Hour

// budgetCacheEntry caches a ListByScope result with an expiry time.
type budgetCacheEntry struct {
	budgets []store.Budget
	expires time.Time
}

// budgetCacheTTL is how long cached budget definitions stay valid. Budgets
// change only on admin CRUD (rare), while the cache is read on every request.
// A 10-second TTL eliminates per-request DB round-trips for the definition
// lookup without making admin changes feel sluggish.
const budgetCacheTTL = 10 * time.Second

// minKeyBalanceMicros is the minimum API-key balance required to use the
// service. A key holding less cannot make requests, which prevents abuse of
// near-zero starter credit.
const minKeyBalanceMicros int64 = 50_000 // $0.05
// budgetSource and spendSource are the store slices the engine reads; the
// concrete repos satisfy them.
type budgetSource interface {
	ListByScope(ctx context.Context, kind store.BudgetScope, scopeID string) ([]store.Budget, error)
}

type spendSource interface {
	SpendAndTokensBatch(ctx context.Context, scopes []store.SpendScope) ([]store.SpendResult, error)
}

// Engine evaluates budgets against recorded usage.
type Engine struct {
	budgets budgetSource
	usage   spendSource

	// Shared counter mode (see EnableSharedCounter). Nil counter means every
	// check sums the usage table.
	counter       Counter
	counterTTL    time.Duration
	counterPrefix string
	deltas        chan deltaItem
	deltaDropped  atomic.Int64
	counterErrors atomic.Int64
	// counterOpenUntil (unix nanos) routes reads to the usage table for a
	// few seconds after a counter failure, so an unreachable redis costs one
	// timeout per cooldown instead of one per request.
	counterOpenUntil atomic.Int64
	// counterStaleUntil (unix nanos) routes reads to the usage table after an
	// increment was lost, until the counters that may lack it re-seed.
	counterStaleUntil atomic.Int64
	stopCounter       context.CancelFunc
	counterDone       chan struct{}
	log               *slog.Logger

	// Reservations track spend for in-flight requests so concurrent requests
	// cannot overshoot hard limits. reserved sums the live entries per budget
	// scope (reservationKey); entries holds each request's own reservation.
	mu              sync.RWMutex
	reserved        map[string]int64
	reservedTokens  map[string]int64
	entries         map[uint64]*reservation
	nextReservation atomic.Uint64
	lastSweep       time.Time
	// tokenHoldTail keeps a confirmed request's token hold a little longer,
	// on top of the pending spend that already covers its usage row.
	tokenHoldTail time.Duration

	// pending holds spend of metered rows not yet in the usage table (or,
	// with shared counters, not yet in the counter), keyed like reservations.
	// Without it a request finishing within the usage writer's flush interval
	// is invisible to the next budget check.
	pendMu  sync.Mutex
	pending map[string]*pendingSpend
	// moved is a running total, per key, of pending spend that has become
	// visible to spend reads. Reserve compares it before and after Check's
	// unlocked read to catch rows that moved in between.
	moved map[string]*pendingSpend

	// budgetCache caches ListByScope results to avoid a DB round-trip on every
	// request. Keyed by "scope_kind:scope_id". Invalidated on budget mutations.
	budgetMu    sync.RWMutex
	budgetCache map[string]*budgetCacheEntry
}

// New builds a budget Engine.
func New(budgets *store.BudgetRepo, usage *store.UsageRepo) *Engine {
	return &Engine{
		budgets:       budgets,
		usage:         usage,
		budgetCache:   make(map[string]*budgetCacheEntry),
		log:           slog.Default(),
		tokenHoldTail: defaultTokenHoldTail,
	}
}

// SetTokenHoldTail sets how long a confirmed request keeps its token hold:
// long enough for the metered usage row to reach the store Check reads. Zero
// releases on confirm. Call before serving traffic.
func (e *Engine) SetTokenHoldTail(d time.Duration) {
	e.mu.Lock()
	e.tokenHoldTail = max(d, 0)
	e.mu.Unlock()
}

// Decision is the outcome of a budget check.
type Decision struct {
	// Allowed is false when a hard-cutoff budget is exhausted.
	Allowed bool
	// Blocking is the budget that caused a block (nil when allowed).
	Blocking *store.Budget
	// CreditsExhausted is true when the block is an API key's prepaid credit
	// running out, rather than a spend or token cap.
	CreditsExhausted bool
	// TokensExhausted is set when Blocking blocked on its token limit.
	TokensExhausted bool
	// Alerts lists budgets that have crossed their alert threshold but not their
	// hard limit. Informational; does not block.
	Alerts []store.Budget
	// BudgetUsage holds per-budget spend details for reporting.
	BudgetUsage []BudgetUsage
}

// BudgetUsage pairs a budget with its current-period spend, both cost and tokens.
type BudgetUsage struct {
	Budget store.Budget
	// CostMicros is recorded spend plus in-flight reservations.
	CostMicros int64
	// SpentMicros is recorded spend only (usage table or shared counter).
	SpentMicros int64
	// TokenCount is recorded plus pending tokens; ReservedTokens the
	// in-flight token holds.
	TokenCount     int64
	ReservedTokens int64

	// Recorded spend as read by Check, before pending rows are added.
	// Reserve re-adds pending spend under its lock from these.
	visibleNanos, visibleTokens int64
}

// Scope identifies the request's billing scopes to check.
type Scope struct {
	TenantID  string
	ProjectID string
	APIKeyID  string
	// Reservation is the id Reserve returned for this request. Confirm and
	// Release act on that reservation only; zero means none.
	Reservation uint64
}

// Estimate is what a request asks Reserve to hold.
type Estimate struct {
	// Micros is the request's expected cost; zero means unknown.
	Micros int64
	// TokenMetered holds PromptTokens plus an output cap against every
	// hard-cutoff budget with a token limit. The cap is MaxOutputTokens
	// clamped to what remains after the prompt.
	TokenMetered    bool
	PromptTokens    int64
	MaxOutputTokens int64
}

// Reservation is the outcome of Reserve.
type Reservation struct {
	// ID goes in Scope.Reservation for Confirm and Release.
	ID uint64
	// OutputCap is the granted output cap; zero when no token budget applies.
	OutputCap int64
}

// cachedListByScope returns budgets for a scope, using a short-lived in-memory
// cache to avoid a DB round-trip on every request.
func (e *Engine) cachedListByScope(ctx context.Context, kind store.BudgetScope, scopeID string) ([]store.Budget, error) {
	key := string(kind) + ":" + scopeID
	now := time.Now()

	// Fast path: read lock only.
	e.budgetMu.RLock()
	if ent, ok := e.budgetCache[key]; ok && now.Before(ent.expires) {
		budgets := ent.budgets
		e.budgetMu.RUnlock()
		return budgets, nil
	}
	e.budgetMu.RUnlock()

	// Slow path: fetch from DB and populate cache.
	budgets, err := e.budgets.ListByScope(ctx, kind, scopeID)
	if err != nil {
		return nil, err
	}

	e.budgetMu.Lock()
	e.budgetCache[key] = &budgetCacheEntry{budgets: budgets, expires: now.Add(budgetCacheTTL)}
	e.budgetMu.Unlock()
	return budgets, nil
}

// InvalidateBudgetCache clears the cached budget definitions. Call this after
// budget CRUD operations so the next request sees fresh data immediately. In
// shared counter mode every other replica clears its cache too.
func (e *Engine) InvalidateBudgetCache() {
	e.invalidateLocal()
	if e.counter != nil {
		if err := e.counter.PublishInvalidate(context.Background()); err != nil {
			e.log.Warn("budget: broadcast cache invalidation failed; other replicas refresh within 10s", "err", err)
		}
	}
}

func (e *Engine) invalidateLocal() {
	e.budgetMu.Lock()
	// Clear all entries; budget mutations are infrequent so a full flush is fine.
	for k := range e.budgetCache {
		delete(e.budgetCache, k)
	}
	e.budgetMu.Unlock()
}

// InvalidateBudgetCacheForScope clears cached entries for a specific scope kind
// and ID. More targeted than InvalidateBudgetCache for single-budget changes.
func (e *Engine) InvalidateBudgetCacheForScope(kind store.BudgetScope, scopeID string) {
	key := string(kind) + ":" + scopeID
	e.budgetMu.Lock()
	delete(e.budgetCache, key)
	e.budgetMu.Unlock()
	if e.counter != nil {
		// Other replicas cannot tell which scope changed; they flush all.
		_ = e.counter.PublishInvalidate(context.Background())
	}
}

// Check evaluates all budgets applicable to a scope and reports whether the
// request may proceed. It checks key, project, and tenant budgets; the first
// exhausted hard-cutoff budget blocks the request. Both USD cost and token
// limits are evaluated in a single batched query per check.
func (e *Engine) Check(ctx context.Context, scope Scope) (Decision, error) {
	dec := Decision{Allowed: true}

	checks := []struct {
		kind store.BudgetScope
		id   string
	}{
		{store.ScopeAPIKey, scope.APIKeyID},
		{store.ScopeProject, scope.ProjectID},
		{store.ScopeTenant, scope.TenantID},
	}

	// Phase 1: collect all budgets and their spend scopes.
	type budgetEntry struct {
		budget store.Budget
		idx    int // index into the batch spend query
	}
	var entries []budgetEntry
	var spendScopes []store.SpendScope

	for _, c := range checks {
		if c.id == "" {
			continue
		}
		budgets, err := e.cachedListByScope(ctx, c.kind, c.id)
		if err != nil {
			return Decision{}, fmt.Errorf("budget: list %s budgets: %w", c.kind, err)
		}
		for _, b := range budgets {
			since := PeriodStart(b.Period, time.Now())
			idx := len(spendScopes)
			spendScopes = append(spendScopes, store.SpendScope{
				Kind:    b.ScopeKind,
				ScopeID: b.ScopeID,
				Since:   since,
			})
			entries = append(entries, budgetEntry{budget: b, idx: idx})
		}
	}

	if len(entries) == 0 {
		return dec, nil
	}

	// Phase 2: fetch all spend data in one round trip — to the shared
	// counters when enabled, else a single batched SQL query.
	spendResults, err := e.spend(ctx, spendScopes)
	if err != nil {
		return Decision{}, err
	}

	// Phase 3: evaluate limits using the pre-fetched results.
	for _, ent := range entries {
		b := ent.budget
		r := spendResults[ent.idx]
		spent := r.CostMicros
		tokens := r.Tokens

		// Add rows metered but not yet visible in spend, then in-flight
		// reservations, so concurrent and back-to-back requests cannot all
		// pass the check before their usage is recorded.
		nanos := r.CostNanos
		if nanos == 0 && r.CostMicros != 0 {
			nanos = r.CostMicros * 1000
		}
		visibleTokens := tokens
		pendNanos, pendTokens := e.getPending(b.ScopeKind, b.ScopeID)
		reservedMicros, reservedTokens := e.getReservedBoth(b.ScopeKind, b.ScopeID)
		spent = (nanos+pendNanos+500)/1000 + reservedMicros
		tokens += pendTokens

		dec.BudgetUsage = append(dec.BudgetUsage, BudgetUsage{
			Budget:         b,
			CostMicros:     spent,
			SpentMicros:    r.CostMicros,
			TokenCount:     tokens,
			ReservedTokens: reservedTokens,
			visibleNanos:   nanos,
			visibleTokens:  visibleTokens,
		})

		// Check USD cost limit.
		if b.LimitMicros > 0 && spent >= b.LimitMicros && b.HardCutoff {
			dec.Allowed = false
			blocking := b
			dec.Blocking = &blocking
			dec.CreditsExhausted = isKeyCredit(b)
			return dec, nil
		}
		// Check token limit, counting in-flight token holds.
		if b.LimitTokens > 0 && tokens+reservedTokens >= b.LimitTokens && b.HardCutoff {
			dec.Allowed = false
			blocking := b
			dec.Blocking = &blocking
			dec.TokensExhausted = true
			return dec, nil
		}
		// Alert thresholds.
		if b.AlertPct > 0 {
			if b.LimitMicros > 0 && spent*100 >= b.LimitMicros*int64(b.AlertPct) {
				dec.Alerts = append(dec.Alerts, b)
			} else if b.LimitTokens > 0 && tokens*100 >= b.LimitTokens*int64(b.AlertPct) {
				dec.Alerts = append(dec.Alerts, b)
			}
		}
	}
	return dec, nil
}

// CheckOrError is a convenience wrapper that returns a ProviderError when the
// request is blocked, suitable for direct use in the pipeline.
func (e *Engine) CheckOrError(ctx context.Context, scope Scope) error {
	dec, err := e.Check(ctx, scope)
	if err != nil {
		return err
	}
	if !dec.Allowed {
		return blockedError(dec)
	}
	return nil
}

// isKeyCredit reports whether b is an API key's prepaid credit: a key budget
// that never resets. Periodic key budgets are spend caps, not credit.
func isKeyCredit(b store.Budget) bool {
	return b.ScopeKind == store.ScopeAPIKey && (b.Period == "" || b.Period == "total")
}

func blockedError(dec Decision) error {
	return &core.ProviderError{
		Kind:             core.ErrBudgetBlocked,
		Message:          fmt.Sprintf("budget %q exhausted for %s", dec.Blocking.ID, dec.Blocking.ScopeKind),
		CreditsExhausted: dec.CreditsExhausted,
	}
}

// Reserve checks the budget and records this request's estimated spend as a
// reservation, returning its id. Pass the id back through Scope.Reservation to
// Confirm (actual cost known) or Release (request failed before metering).
//
// est.Micros is the request's expected cost. The request is refused when it
// would not fit in a hard-cutoff budget's remaining balance, and an API key
// must keep at least minKeyBalanceMicros remaining to start a request. Zero
// means unknown: only the minimum-balance gate applies then.
//
// A token-metered estimate also holds its prompt plus an output cap against
// every hard-cutoff token budget in scope; the cap is the wanted output
// clamped to what remains after the prompt, returned as OutputCap.
//
// The remaining balance and tokens are re-evaluated under the reservation
// lock that adds the hold, so a burst of concurrent requests cannot all pass
// on the same stale balance.
func (e *Engine) Reserve(ctx context.Context, scope Scope, est Estimate) (Reservation, error) {
	// Drop expired reservations before Check reads them: a stale one could
	// otherwise make Check refuse, and the sweep below would never be reached.
	e.mu.Lock()
	e.sweepLocked(time.Now())
	e.mu.Unlock()

	// Check reads recorded spend without the lock. A row that becomes visible
	// to spend reads after that read is missing from it, and has also left
	// pending by the time the lock is held. Snapshot how much has moved
	// before the read so the lock section can add that difference back. A row
	// that moved just before the read may be counted twice for this one
	// decision, which errs toward refusing rather than overspending.
	scopeKeyList := scopeKeys(scope)
	movedBefore := e.movedSnapshot(scopeKeyList)

	dec, err := e.Check(ctx, scope)
	if err != nil {
		return Reservation{}, err
	}
	if !dec.Allowed {
		if est.TokenMetered && dec.TokensExhausted {
			return Reservation{}, insufficientTokens(0)
		}
		return Reservation{}, blockedError(dec)
	}
	estimatedMicros := max(est.Micros, 0)

	now := time.Now()
	e.mu.Lock()
	defer e.mu.Unlock()
	e.sweepLocked(now)

	pend, movedNow := e.pendingState(scopeKeyList)

	var keys, tokenKeys []string
	var tokensLeft int64
	tokenLimited := false
	for _, bu := range dec.BudgetUsage {
		b := bu.Budget
		if !b.HardCutoff {
			continue
		}
		key := reservationKey(b.ScopeKind, b.ScopeID)
		if !slices.Contains(keys, key) {
			// Budgets of one scope (say daily and total) share its
			// reservation total, so the request is held there once.
			keys = append(keys, key)
		}
		// Spend as of now: what Check read, plus rows still pending, plus
		// rows that became visible after Check's read.
		p, mNow, mBefore := pend[key], movedNow[key], movedBefore[key]
		spentNanos := bu.visibleNanos + p.nanos + (mNow.nanos - mBefore.nanos)
		spentTokens := bu.visibleTokens + p.tokens + (mNow.tokens - mBefore.tokens)
		if b.LimitTokens > 0 {
			left := b.LimitTokens - spentTokens - e.reservedTokens[key]
			if left <= 0 {
				if est.TokenMetered {
					return Reservation{}, insufficientTokens(0)
				}
				return Reservation{}, &core.ProviderError{
					Kind:    core.ErrBudgetBlocked,
					Message: fmt.Sprintf("budget %q exhausted for %s", b.ID, b.ScopeKind),
				}
			}
			if !tokenLimited || left < tokensLeft {
				tokensLeft = left
			}
			tokenLimited = true
			if !slices.Contains(tokenKeys, key) {
				tokenKeys = append(tokenKeys, key)
			}
		}
		// Minimum-balance gate: an API key must still hold at least
		// minKeyBalanceMicros to start a metered request. This stops abuse
		// of near-zero balances. Read-only checks that never reserve (e.g.
		// the media endpoint) are unaffected. A token-only budget is exempt:
		// it is metered in tokens and has no balance to gate. A key budget
		// with no limit at all holds no balance, so it is gated.
		tokenOnly := b.LimitTokens > 0 && b.LimitMicros == 0
		if b.LimitMicros <= 0 {
			if b.ScopeKind == store.ScopeAPIKey && !tokenOnly {
				return Reservation{}, &core.ProviderError{
					Kind: core.ErrBudgetBlocked,
					Message: fmt.Sprintf("minimum API key balance is $%.2f (remaining $%s)",
						float64(minKeyBalanceMicros)/1_000_000, usd(0)),
					CreditsExhausted: isKeyCredit(b),
				}
			}
			continue
		}
		remaining := b.LimitMicros - (spentNanos+500)/1000 - e.reserved[key]
		if b.ScopeKind == store.ScopeAPIKey && remaining < minKeyBalanceMicros {
			return Reservation{}, &core.ProviderError{
				Kind: core.ErrBudgetBlocked,
				Message: fmt.Sprintf("minimum API key balance is $%.2f (remaining $%s)",
					float64(minKeyBalanceMicros)/1_000_000, usd(remaining)),
				CreditsExhausted: isKeyCredit(b),
			}
		}
		if estimatedMicros > remaining {
			return Reservation{}, &core.ProviderError{
				Kind: core.ErrBudgetBlocked,
				Message: fmt.Sprintf("insufficient balance: request estimated at $%s, remaining $%s",
					usd(estimatedMicros), usd(remaining)),
				CreditsExhausted: isKeyCredit(b),
			}
		}
	}

	var outputCap, heldTokens int64
	if est.TokenMetered {
		if !tokenLimited {
			// A token-metered key without a token limit is never unlimited.
			return Reservation{}, insufficientTokens(0)
		}
		outputCap = min(est.MaxOutputTokens, tokensLeft-est.PromptTokens)
		if outputCap < 1 {
			return Reservation{}, insufficientTokens(max(tokensLeft, 0))
		}
		heldTokens = est.PromptTokens + outputCap
	} else {
		tokenKeys = nil
	}

	e.ensureMapsLocked()
	// A reservation is held even for an unknown estimate so concurrent
	// requests see that spend is in flight.
	micros := max(estimatedMicros, 1)
	id := e.nextReservation.Add(1)
	e.entries[id] = &reservation{keys: keys, micros: micros, expires: now.Add(reservationMaxAge),
		tokenKeys: tokenKeys, tokens: heldTokens}
	for _, k := range keys {
		e.reserved[k] += micros
	}
	for _, k := range tokenKeys {
		e.reservedTokens[k] += heldTokens
	}
	return Reservation{ID: id, OutputCap: outputCap}, nil
}

func insufficientTokens(remaining int64) *core.ProviderError {
	return &core.ProviderError{
		Kind: core.ErrBudgetBlocked,
		Code: CodeInsufficientTokens,
		Message: fmt.Sprintf("Sisa token paket tidak cukup untuk permintaan ini: tersisa %d token. "+
			"Perpendek prompt, kurangi max_tokens, atau beli paket token baru.", remaining),
	}
}

// Confirm ends the request's reservation once its usage is metered.
// meter.Record has already counted the row as pending spend (UsagePending),
// and pending spend stays counted until spend reads can see the row, so
// dropping the micros here leaves no gap and counts nothing twice. A token
// hold stays for the tail on top of that, erring toward refusing.
func (e *Engine) Confirm(scope Scope, actualMicros int64) {
	if scope.Reservation == 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	r, ok := e.entries[scope.Reservation]
	if !ok || r.confirmed {
		return
	}
	if r.tokens <= 0 || e.tokenHoldTail <= 0 {
		e.dropLocked(scope.Reservation, r)
		return
	}
	for _, k := range r.keys {
		if e.reserved[k] -= r.micros; e.reserved[k] <= 0 {
			delete(e.reserved, k)
		}
	}
	r.micros = 0
	r.confirmed = true
	id := scope.Reservation
	time.AfterFunc(e.tokenHoldTail, func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		if r, ok := e.entries[id]; ok {
			e.dropLocked(id, r)
		}
	})
}

// Release drops the request's in-flight reservation without recording a
// cost. Call this when a request fails before metering. Releasing twice or
// after Confirm is a no-op.
func (e *Engine) Release(scope Scope) {
	if scope.Reservation == 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if r, ok := e.entries[scope.Reservation]; ok && !r.confirmed {
		e.dropLocked(scope.Reservation, r)
	}
}

func (e *Engine) ensureMapsLocked() {
	if e.entries == nil {
		e.entries = make(map[uint64]*reservation)
		e.reserved = make(map[string]int64)
		e.reservedTokens = make(map[string]int64)
	}
}

// scopeKeys lists the reservation keys a scope's budgets can live under. A
// key with no budget behind it is never read, so over-listing is harmless.
func scopeKeys(scope Scope) []string {
	var keys []string
	if scope.APIKeyID != "" {
		keys = append(keys, reservationKey(store.ScopeAPIKey, scope.APIKeyID))
	}
	if scope.ProjectID != "" {
		keys = append(keys, reservationKey(store.ScopeProject, scope.ProjectID))
	}
	if scope.TenantID != "" {
		keys = append(keys, reservationKey(store.ScopeTenant, scope.TenantID))
	}
	return keys
}

func (e *Engine) dropLocked(id uint64, r *reservation) {
	for _, k := range r.keys {
		if e.reserved[k] -= r.micros; e.reserved[k] <= 0 {
			delete(e.reserved, k)
		}
	}
	for _, k := range r.tokenKeys {
		if e.reservedTokens[k] -= r.tokens; e.reservedTokens[k] <= 0 {
			delete(e.reservedTokens, k)
		}
	}
	delete(e.entries, id)
}

// sweepLocked drops reservations a request path never released.
func (e *Engine) sweepLocked(now time.Time) {
	if now.Sub(e.lastSweep) < time.Second {
		return
	}
	e.lastSweep = now
	for id, r := range e.entries {
		if now.After(r.expires) {
			e.dropLocked(id, r)
		}
	}
}

func reservationKey(kind store.BudgetScope, scopeID string) string {
	return string(kind) + ":" + scopeID
}

// getReserved returns the sum of live reservations for a scope.
func (e *Engine) getReserved(kind store.BudgetScope, scopeID string) int64 {
	micros, _ := e.getReservedBoth(kind, scopeID)
	return micros
}

// getReservedTokens returns the in-flight token holds of a scope.
func (e *Engine) getReservedTokens(kind store.BudgetScope, scopeID string) int64 {
	_, tokens := e.getReservedBoth(kind, scopeID)
	return tokens
}

// getReservedBoth returns the live reserved micros and held tokens of a scope.
func (e *Engine) getReservedBoth(kind store.BudgetScope, scopeID string) (micros, tokens int64) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	key := reservationKey(kind, scopeID)
	return e.reserved[key], e.reservedTokens[key]
}

// usd formats micros as dollars for error messages.
func usd(micros int64) string {
	return fmt.Sprintf("%.4f", float64(micros)/1_000_000)
}

// PeriodStart returns the start of the current budget window for a period.
// PeriodEnd returns when a budget window that began at start resets. A
// "total" budget never resets and yields the zero time.
func PeriodEnd(period string, start time.Time) time.Time {
	switch period {
	case "daily":
		return start.AddDate(0, 0, 1)
	case "weekly":
		return start.AddDate(0, 0, 7)
	case "monthly":
		return start.AddDate(0, 1, 0)
	default:
		return time.Time{}
	}
}

func PeriodStart(period string, now time.Time) time.Time {
	now = now.UTC()
	switch period {
	case "daily":
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	case "weekly":
		// ISO-ish: start of the week on Monday.
		offset := (int(now.Weekday()) + 6) % 7
		d := now.AddDate(0, 0, -offset)
		return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC)
	case "monthly":
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	default: // "total"
		return time.Time{}
	}
}
