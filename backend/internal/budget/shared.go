package budget

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/mydisha/keirouter/backend/internal/store"
)

// EnableSharedCounter switches spend lookups to c, shared by every replica,
// and starts the worker that feeds it from ObserveUsage. Counters are
// re-seeded from the usage table every ttl. Call before serving traffic and
// Close on shutdown. Install the engine as the meter's UsageTracker so
// counters receive rows once they are written to the usage table.
//
// A counter never reads lower than the usage table, apart from increments
// still in flight (typically < 1ms). When an increment is lost (full queue,
// redis error) this replica reads the usage table instead until every counter
// that could be missing it has expired and been re-seeded.
func (e *Engine) EnableSharedCounter(c Counter, ttl time.Duration, log *slog.Logger) {
	if ttl <= 0 {
		ttl = time.Minute
	}
	if log != nil {
		e.log = log
	} else if e.log == nil {
		e.log = slog.Default()
	}
	e.counter = c
	e.counterTTL = ttl
	// v2: counters carry a seeded flag; older counters must not be reused.
	e.counterPrefix = "kr:bud2:"
	e.deltas = make(chan deltaItem, 8192)
	e.counterDone = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	e.stopCounter = cancel
	c.SubscribeInvalidate(ctx, e.invalidateLocal)
	go e.runDeltas(ctx)
}

// Close stops the shared counter worker after applying queued increments.
func (e *Engine) Close() {
	if e.stopCounter == nil {
		return
	}
	e.stopCounter()
	<-e.counterDone
}

// SharedCounterStats reports shared counter health.
type SharedCounterStats struct {
	Enabled bool `json:"enabled"`
	Healthy bool `json:"healthy"`
	// Stale is true while spend is read from the usage table because an
	// increment was lost.
	Stale   bool  `json:"stale"`
	Dropped int64 `json:"dropped"`
	Errors  int64 `json:"errors"`
}

// Stats returns shared counter counters.
func (e *Engine) Stats() SharedCounterStats {
	return SharedCounterStats{
		Enabled: e.counter != nil,
		Healthy: e.counter != nil && !e.counterOpen(),
		Stale:   e.counter != nil && e.countersStale(),
		Dropped: e.deltaDropped.Load(),
		Errors:  e.counterErrors.Load(),
	}
}

// spend returns current-period spend for each scope. With shared counters it
// reads them in one round trip, seeds the missing ones from the usage table,
// and falls back to the table entirely if redis fails or counters may be
// missing lost increments.
func (e *Engine) spend(ctx context.Context, scopes []store.SpendScope) ([]store.SpendResult, error) {
	if e.counter == nil || e.countersStale() {
		res, err := e.usage.SpendAndTokensBatch(ctx, scopes)
		if err != nil {
			return nil, fmt.Errorf("budget: batch spend: %w", err)
		}
		return res, nil
	}

	keys := make([]string, len(scopes))
	for i, s := range scopes {
		keys[i] = counterKey(e.counterPrefix, s.Kind, s.ScopeID, s.Since)
	}
	var vals []CounterValue
	err := errCounterOpen
	if !e.counterOpen() {
		vals, err = e.counter.Load(ctx, keys)
		if err != nil {
			e.tripCounter(err)
		} else if e.counterOpenUntil.Load() != 0 && e.counterOpenUntil.Swap(0) != 0 {
			e.log.Info("budget: shared counters reachable again")
		}
	}
	if err != nil {
		res, err := e.usage.SpendAndTokensBatch(ctx, scopes)
		if err != nil {
			return nil, fmt.Errorf("budget: batch spend: %w", err)
		}
		return res, nil
	}

	out := make([]store.SpendResult, len(scopes))
	var missing []int
	for i, v := range vals {
		if !v.Found {
			missing = append(missing, i)
			continue
		}
		// Same rounding as the SQL path: nanos summed, then to micros.
		out[i] = store.SpendResult{CostMicros: (v.CostNanos + 500) / 1000, CostNanos: v.CostNanos, Tokens: v.Tokens}
	}
	if len(missing) == 0 {
		return out, nil
	}
	seedScopes := make([]store.SpendScope, len(missing))
	seedKeys := make([]string, len(missing))
	for j, i := range missing {
		seedScopes[j] = scopes[i]
		seedKeys[j] = keys[i]
	}
	// The empty counters must exist before the table is read; see Counter.
	tokens, err := e.counter.BeginSeed(ctx, seedKeys, seedMarkerTTL)
	if err != nil {
		e.counterErrors.Add(1)
	}
	seeded, err := e.usage.SpendAndTokensBatch(ctx, seedScopes)
	if err != nil {
		return nil, fmt.Errorf("budget: batch spend: %w", err)
	}
	for j, i := range missing {
		out[i] = seeded[j]
		if tokens == nil {
			continue
		}
		v := CounterValue{CostNanos: seeded[j].CostNanos, Tokens: seeded[j].Tokens}
		if err := e.counter.Seed(ctx, keys[i], tokens[j], v, e.counterTTL); err != nil {
			e.counterErrors.Add(1)
		}
	}
	return out, nil
}

// seedMarkerTTL is how long an empty counter waits for its seed value. A
// slower table read leaves the counter to expire and be seeded again.
const seedMarkerTTL = 10 * time.Second

// countersStale reports whether this replica lost an increment recently
// enough that some counter may still be missing it.
func (e *Engine) countersStale() bool {
	until := e.counterStaleUntil.Load()
	return until != 0 && time.Now().UnixNano() < until
}

// markCountersStale routes spend reads to the usage table until every counter
// seeded before now has expired. Counters live counterTTL plus 10% jitter;
// seedMarkerTTL covers a seed whose table read started before the loss.
func (e *Engine) markCountersStale(lost int64) {
	e.deltaDropped.Add(lost)
	wasStale := e.countersStale()
	until := time.Now().Add(e.counterTTL*11/10 + seedMarkerTTL).UnixNano()
	for {
		cur := e.counterStaleUntil.Load()
		if cur >= until || e.counterStaleUntil.CompareAndSwap(cur, until) {
			break
		}
	}
	if !wasStale {
		e.log.Warn("budget: shared counter increment lost; checking spend in the usage table until counters re-seed",
			"for", time.Duration(until-time.Now().UnixNano()).Round(time.Second))
	}
}

// counterBreakerCooldown is how long a counter failure bypasses redis.
const counterBreakerCooldown = 5 * time.Second

var errCounterOpen = errors.New("budget: shared counter circuit open")

func (e *Engine) counterOpen() bool {
	until := e.counterOpenUntil.Load()
	return until != 0 && time.Now().UnixNano() < until
}

func (e *Engine) tripCounter(err error) {
	e.counterErrors.Add(1)
	if e.counterOpenUntil.Swap(time.Now().Add(counterBreakerCooldown).UnixNano()) == 0 {
		e.log.Warn("budget: shared counters unavailable, checking spend in the usage table", "retry_in", counterBreakerCooldown, "err", err)
	}
}

// ObserveUsage adds a row already written to the usage table to the shared
// counters of every budget covering its key, project and tenant, then
// releases the row's pending spend once the counters have it. It never
// blocks: if an increment cannot be queued, this replica reads the usage
// table until counters re-seed.
func (e *Engine) ObserveUsage(rec store.UsageRecord) {
	if e.counter == nil {
		e.releasePending(rec)
		return
	}
	cost, tokens := usageSpend(rec)
	if cost == 0 && tokens == 0 {
		e.releasePending(rec)
		return
	}
	now := rec.CreatedAt
	if now.IsZero() {
		now = time.Now()
	}
	lost := false
	for _, sc := range pendingScopes(rec) {
		budgets, err := e.cachedListByScope(context.Background(), sc.kind, sc.id)
		if err != nil {
			lost = true
			continue
		}
		seen := map[string]bool{}
		for _, b := range budgets {
			key := counterKey(e.counterPrefix, b.ScopeKind, b.ScopeID, PeriodStart(b.Period, now))
			if seen[key] {
				continue
			}
			seen[key] = true
			select {
			case e.deltas <- deltaItem{Delta: Delta{Key: key, CostNanos: cost, Tokens: tokens}}:
			default:
				lost = true
			}
		}
	}
	if !lost {
		r := rec
		select {
		case e.deltas <- deltaItem{release: &r}:
			return
		default:
		}
	}
	// The row is in the usage table, which stale counters fall back to.
	e.markCountersStale(1)
	e.releasePending(rec)
}

// runDeltas applies queued increments. Under load it merges every delta that
// is already queued into one pipelined round trip, one write per counter, so
// redis traffic grows with the number of active budgets, not requests.
func (e *Engine) runDeltas(ctx context.Context) {
	defer close(e.counterDone)
	merged := map[string]*Delta{}
	var releases []store.UsageRecord
	flush := func() {
		if len(merged) > 0 {
			batch := make([]Delta, 0, len(merged))
			for _, d := range merged {
				batch = append(batch, *d)
			}
			clear(merged)
			if e.counterOpen() {
				e.markCountersStale(int64(len(batch)))
			} else if err := e.counter.Add(context.Background(), batch); err != nil {
				e.tripCounter(err)
				e.markCountersStale(int64(len(batch)))
			}
		}
		// Pending spend is released only after the counters have the rows,
		// or after a failure has sent reads to the usage table.
		for _, rec := range releases {
			e.releasePending(rec)
		}
		releases = releases[:0]
	}
	add := func(it deltaItem) {
		if it.release != nil {
			releases = append(releases, *it.release)
			return
		}
		d := it.Delta
		if m, ok := merged[d.Key]; ok {
			m.CostNanos += d.CostNanos
			m.Tokens += d.Tokens
			return
		}
		merged[d.Key] = &d
	}
	for {
		select {
		case <-ctx.Done():
			for {
				select {
				case d := <-e.deltas:
					add(d)
				default:
					flush()
					return
				}
			}
		case d := <-e.deltas:
			add(d)
		drain:
			for n := 0; n < 4096 && len(merged) < 512 && len(releases) < 4096; n++ {
				select {
				case d := <-e.deltas:
					add(d)
				default:
					break drain
				}
			}
			flush()
		}
	}
}
