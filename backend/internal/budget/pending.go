package budget

import "github.com/mydisha/keirouter/backend/internal/store"

// deltaItem is a counter increment, or (release set) a row whose pending
// spend may be released once the increments queued before it are applied.
type deltaItem struct {
	Delta
	release *store.UsageRecord
}

// pendingSpend is spend of rows metered but not yet visible to spend reads.
type pendingSpend struct {
	nanos, tokens int64
}

type pendingScope struct {
	kind store.BudgetScope
	id   string
}

func pendingScopes(rec store.UsageRecord) []pendingScope {
	out := make([]pendingScope, 0, 3)
	for _, sc := range []pendingScope{
		{store.ScopeAPIKey, rec.APIKeyID},
		{store.ScopeProject, rec.ProjectID},
		{store.ScopeTenant, rec.TenantID},
	} {
		if sc.id != "" {
			out = append(out, sc)
		}
	}
	return out
}

// usageSpend is what the usage table counts for a row: the store derives
// nanos from micros when only micros is set, and spend sums skip the cost
// (not the tokens) of backfilled rows.
func usageSpend(rec store.UsageRecord) (nanos, tokens int64) {
	nanos = rec.CostNanos
	if nanos == 0 && rec.CostMicros != 0 {
		nanos = rec.CostMicros * 1000
	}
	if rec.PricingBackfilled {
		nanos = 0
	}
	return nanos, int64(rec.PromptTokens + rec.CompletionTokens)
}

// UsagePending implements meter.UsageTracker: the row counts toward its
// budgets from now on, before it is written.
func (e *Engine) UsagePending(rec store.UsageRecord) { e.adjustPending(rec, 1) }

// UsagePersisted implements meter.UsageTracker.
func (e *Engine) UsagePersisted(rec store.UsageRecord) { e.ObserveUsage(rec) }

// UsageDiscarded implements meter.UsageTracker: the row will never be
// written, so it stops counting.
func (e *Engine) UsageDiscarded(rec store.UsageRecord) { e.releasePending(rec) }

func (e *Engine) releasePending(rec store.UsageRecord) { e.adjustPending(rec, -1) }

func (e *Engine) adjustPending(rec store.UsageRecord, sign int64) {
	nanos, tokens := usageSpend(rec)
	if nanos == 0 && tokens == 0 {
		return
	}
	e.pendMu.Lock()
	defer e.pendMu.Unlock()
	if e.pending == nil {
		e.pending = map[string]*pendingSpend{}
	}
	for _, sc := range pendingScopes(rec) {
		key := reservationKey(sc.kind, sc.id)
		p := e.pending[key]
		if p == nil {
			if sign < 0 {
				continue
			}
			p = &pendingSpend{}
			e.pending[key] = p
		}
		p.nanos += sign * nanos
		p.tokens += sign * tokens
		if p.nanos <= 0 && p.tokens <= 0 {
			delete(e.pending, key)
		}
	}
}

func (e *Engine) getPending(kind store.BudgetScope, scopeID string) (nanos, tokens int64) {
	e.pendMu.Lock()
	defer e.pendMu.Unlock()
	if p := e.pending[reservationKey(kind, scopeID)]; p != nil {
		return max(p.nanos, 0), max(p.tokens, 0)
	}
	return 0, 0
}
