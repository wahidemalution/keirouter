// Package budget enforces spend limits before a request is dispatched.
//
// Budgets are defined per scope (tenant, project, or API key) with a hard cap
// in micros of USD over a rolling period. Before each request the engine sums
// the scope's spend in the current period; if a hard-cutoff budget is already
// at or over its limit, the request is rejected with ErrBudgetBlocked (which is
// not fallbackable). Alert thresholds let the UI warn before the cutoff.
package budget

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mydisha/keirouter/backend/internal/core"
	"github.com/mydisha/keirouter/backend/internal/store"
)

// reservation tracks in-flight spend that has been budget-checked but not yet
// metered. This prevents the TOCTOU race where concurrent requests all pass
// the budget check before any usage is recorded.
type reservation struct {
	micros int64
	tokens int64
}

// hold is one request's share of the reservations, so finishing one request
// releases exactly what it reserved.
type hold struct {
	amounts   map[string]reservation // by reservation key
	created   time.Time
	confirmed bool
}

// CodeInsufficientTokens is the client error code for a token-metered request
// that does not fit in what remains of its token budget.
const CodeInsufficientTokens = "insufficient_tokens"

// defaultTokenHoldTail covers the async usage writer's default 1s flush.
const defaultTokenHoldTail = 3 * time.Second

// holdMaxAge drops a hold that was never confirmed or released, so a leaked
// hold cannot block a key for good.
const holdMaxAge = 2 * time.Hour

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
	deltas        chan Delta
	deltaDropped  atomic.Int64
	counterErrors atomic.Int64
	// counterOpenUntil (unix nanos) routes reads to the usage table for a
	// few seconds after a counter failure, so an unreachable redis costs one
	// timeout per cooldown instead of one per request.
	counterOpenUntil atomic.Int64
	stopCounter      context.CancelFunc
	counterDone      chan struct{}
	log              *slog.Logger

	// reservations tracks estimated spend for in-flight requests, keyed by
	// scope ID. Prevents concurrent requests from overshooting hard limits.
	mu           sync.RWMutex
	reservations map[string]*reservation // key: scopeID
	holds        map[uint64]*hold
	nextHold     uint64
	lastSweep    time.Time
	// tokenHoldTail keeps a confirmed request's token hold until its usage
	// row is visible to Check.
	tokenHoldTail time.Duration

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
		reservations:  make(map[string]*reservation),
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
	// Alerts lists budgets that have crossed their alert threshold but not their
	// hard limit. Informational; does not block.
	Alerts []store.Budget
	// BudgetUsage holds per-budget spend details for reporting.
	BudgetUsage []BudgetUsage
	// TokensExhausted is set when Blocking blocked on its token limit.
	TokensExhausted bool
}

// BudgetUsage pairs a budget with its current-period spend, both cost and tokens.
type BudgetUsage struct {
	Budget     store.Budget
	CostMicros int64
	// TokenCount is the recorded tokens; ReservedTokens the in-flight holds.
	TokenCount     int64
	ReservedTokens int64
}

// Scope identifies the request's billing scopes to check.
type Scope struct {
	TenantID  string
	ProjectID string
	APIKeyID  string
	// Hold is the request's reservation from Reserve. Confirm and Release
	// act on it alone; zero means the request holds nothing.
	Hold uint64
}

// Estimate is what a request asks Reserve to hold.
type Estimate struct {
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
	// ID goes in Scope.Hold for Confirm and Release.
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

		// Include in-flight reservations in the spend calculation to
		// prevent the TOCTOU race where concurrent requests all pass the
		// budget check before any usage is recorded.
		reserved := e.getReserved(b.ScopeKind, b.ScopeID)
		spent += reserved.micros

		dec.BudgetUsage = append(dec.BudgetUsage, BudgetUsage{
			Budget:         b,
			CostMicros:     spent,
			TokenCount:     tokens,
			ReservedTokens: reserved.tokens,
		})

		// Check USD cost limit.
		if b.LimitMicros > 0 && spent >= b.LimitMicros && b.HardCutoff {
			dec.Allowed = false
			blocking := b
			dec.Blocking = &blocking
			return dec, nil
		}
		// Check token limit.
		if b.LimitTokens > 0 && tokens+reserved.tokens >= b.LimitTokens && b.HardCutoff {
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
		return &core.ProviderError{
			Kind:    core.ErrBudgetBlocked,
			Message: fmt.Sprintf("budget %q exhausted for %s", dec.Blocking.ID, dec.Blocking.ScopeKind),
		}
	}
	return nil
}

// Reserve checks the budget and holds the request's estimated spend, so
// concurrent requests see each other before any usage is recorded.
//
// est.Micros is the expected cost (0 for unknown; the hold still makes
// concurrent requests count it). A token-metered estimate is checked against
// the token budgets and held under one lock, so concurrent holds never add up
// past a limit.
//
// After the request completes, call Confirm with Scope.Hold set to the
// returned ID, or Release if it failed before metering.
func (e *Engine) Reserve(ctx context.Context, scope Scope, est Estimate) (Reservation, error) {
	dec, err := e.Check(ctx, scope)
	if err != nil {
		return Reservation{}, err
	}
	if !dec.Allowed {
		if est.TokenMetered && dec.TokensExhausted {
			return Reservation{}, insufficientTokens(0)
		}
		return Reservation{}, &core.ProviderError{
			Kind:    core.ErrBudgetBlocked,
			Message: fmt.Sprintf("budget %q exhausted for %s", dec.Blocking.ID, dec.Blocking.ScopeKind),
		}
	}

	// Minimum-balance gate: an API key must hold at least minKeyBalanceMicros
	// to make a metered (debiting) request. This stops abuse of near-zero
	// starter credit. Read-only checks that never reserve (e.g. the media
	// endpoint) are intentionally unaffected. A token-only budget is exempt:
	// it is metered in tokens and has no balance to gate.
	for _, bu := range dec.BudgetUsage {
		b := bu.Budget
		tokenOnly := b.LimitTokens > 0 && b.LimitMicros == 0
		if b.ScopeKind == store.ScopeAPIKey && b.HardCutoff && !tokenOnly && b.LimitMicros < minKeyBalanceMicros {
			return Reservation{}, &core.ProviderError{
				Kind:    core.ErrBudgetBlocked,
				Message: fmt.Sprintf("minimum API key balance is $%.2f", float64(minKeyBalanceMicros)/1_000_000),
			}
		}
	}

	cost := est.Micros
	if cost <= 0 {
		cost = 1
	}
	amounts := map[string]reservation{}
	for _, bu := range dec.BudgetUsage {
		if !bu.Budget.HardCutoff {
			continue
		}
		key := reservationKey(bu.Budget.ScopeKind, bu.Budget.ScopeID)
		a := amounts[key]
		a.micros += cost
		amounts[key] = a
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.Now()
	e.sweepLocked(now)

	var outputCap int64
	if est.TokenMetered {
		// Remaining is read under the lock that adds the hold: the recorded
		// tokens come from Check, the in-flight ones from the map right now.
		remaining := int64(-1)
		for _, bu := range dec.BudgetUsage {
			b := bu.Budget
			if !b.HardCutoff || b.LimitTokens <= 0 {
				continue
			}
			left := b.LimitTokens - bu.TokenCount
			if r, ok := e.reservations[reservationKey(b.ScopeKind, b.ScopeID)]; ok {
				left -= r.tokens
			}
			if remaining < 0 || left < remaining {
				remaining = left
			}
		}
		if remaining >= 0 {
			outputCap = min(est.MaxOutputTokens, remaining-est.PromptTokens)
			if outputCap < 1 {
				return Reservation{}, insufficientTokens(max(remaining, 0))
			}
			held := est.PromptTokens + outputCap
			for _, bu := range dec.BudgetUsage {
				b := bu.Budget
				if !b.HardCutoff || b.LimitTokens <= 0 {
					continue
				}
				key := reservationKey(b.ScopeKind, b.ScopeID)
				a := amounts[key]
				a.tokens = held
				amounts[key] = a
			}
		}
	}

	if len(amounts) == 0 {
		return Reservation{OutputCap: outputCap}, nil
	}
	for key, a := range amounts {
		r, ok := e.reservations[key]
		if !ok {
			r = &reservation{}
			e.reservations[key] = r
		}
		r.micros += a.micros
		r.tokens += a.tokens
	}
	if e.holds == nil {
		e.holds = make(map[uint64]*hold)
	}
	e.nextHold++
	id := e.nextHold
	e.holds[id] = &hold{amounts: amounts, created: now}
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

// Confirm ends the request's hold after its usage was metered. The micros are
// released at once; tokens stay held for the tail so the next Check cannot
// miss a usage row that the async writer has not flushed yet.
func (e *Engine) Confirm(scope Scope, actualMicros int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	h, ok := e.holds[scope.Hold]
	if !ok || h.confirmed {
		return
	}
	var tokens bool
	for key, a := range h.amounts {
		if r, ok := e.reservations[key]; ok {
			r.micros -= a.micros
		}
		a.micros = 0
		h.amounts[key] = a
		tokens = tokens || a.tokens > 0
	}
	if !tokens || e.tokenHoldTail <= 0 {
		e.dropLocked(scope.Hold)
		return
	}
	h.confirmed = true
	id := scope.Hold
	time.AfterFunc(e.tokenHoldTail, func() {
		e.mu.Lock()
		e.dropLocked(id)
		e.mu.Unlock()
	})
}

// Release drops the request's hold when it failed before metering. It is a
// no-op once the hold was confirmed or released.
func (e *Engine) Release(scope Scope) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if h, ok := e.holds[scope.Hold]; ok && !h.confirmed {
		e.dropLocked(scope.Hold)
	}
}

func (e *Engine) dropLocked(id uint64) {
	h, ok := e.holds[id]
	if !ok {
		return
	}
	delete(e.holds, id)
	for key, a := range h.amounts {
		r, ok := e.reservations[key]
		if !ok {
			continue
		}
		r.micros -= a.micros
		r.tokens -= a.tokens
		if r.micros <= 0 && r.tokens <= 0 {
			delete(e.reservations, key)
		}
	}
}

func (e *Engine) sweepLocked(now time.Time) {
	if now.Sub(e.lastSweep) < time.Minute {
		return
	}
	e.lastSweep = now
	for id, h := range e.holds {
		if now.Sub(h.created) > holdMaxAge {
			e.dropLocked(id)
		}
	}
}

func reservationKey(kind store.BudgetScope, scopeID string) string {
	return string(kind) + ":" + scopeID
}

// getReserved returns the in-flight reservations for a scope.
func (e *Engine) getReserved(kind store.BudgetScope, scopeID string) reservation {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if r, ok := e.reservations[reservationKey(kind, scopeID)]; ok {
		return *r
	}
	return reservation{}
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
