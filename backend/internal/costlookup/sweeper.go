package costlookup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mydisha/keirouter/backend/internal/store"
)

const (
	sweepInterval    = 10 * time.Second
	pendingWindow    = time.Hour
	maxPendingRows   = 5000
	maxPagesPerPass  = 50
	pageSize         = 100
	pendingLookback  = 60 * time.Second
	overlapLookback  = 180 * time.Second
	orphanMinAge     = 120 * time.Second
	minBackoff       = 5 * time.Second
	maxBackoff       = 60 * time.Second
	sweeperLockID    = 0x4b52434c
	statusOK         = "ok"
	statusUnauthzd   = "unauthorized"
	transientPrefix  = "costlookup: temporary failure: "
	tokenOpenFailure = "error: stored access token cannot be read"
)

// CostDeltaObserver receives cost corrections for the shared budget counter.
type CostDeltaObserver interface {
	ObserveCostDelta(tenantID, projectID, apiKeyID string, at time.Time, deltaNanos int64)
}

// Deps wires the sweeper to the store, the vault, the budget counter and HTTP.
type Deps struct {
	Usage     *store.UsageRepo
	Providers *store.CustomProviderRepo
	DB        *store.DB
	Open      func(sealed string) (string, error)
	Budget    CostDeltaObserver
	HTTP      *http.Client
	Log       *slog.Logger
	Now       func() time.Time
}

// Sweeper reconciles pending usage rows of cost-lookup providers against
// the provider's usage log. One replica sweeps at a time.
type Sweeper struct {
	d     Deps
	mu    sync.Mutex
	state map[string]*providerState
}

type providerState struct {
	lastEnd         int64
	unauthorizedFor time.Time
	backoff         time.Duration
	backoffUntil    time.Time
	loggedStatus    string
	reportedDay     string
}

// New returns a sweeper; Run drives it.
func New(d Deps) *Sweeper {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	return &Sweeper{d: d, state: map[string]*providerState{}}
}

// Run sweeps every sweepInterval until ctx ends.
func (s *Sweeper) Run(ctx context.Context) {
	t := time.NewTicker(sweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Pass(ctx)
		}
	}
}

// Pass sweeps every provider with cost lookup on, once.
func (s *Sweeper) Pass(ctx context.Context) {
	release, ok, err := s.d.DB.TryAdvisoryLock(ctx, sweeperLockID)
	if err != nil {
		s.d.Log.Warn("cost lookup: lock failed", "err", err)
		return
	}
	if !ok {
		return
	}
	defer release()
	providers, err := s.d.Providers.ListProviders(ctx, store.DefaultTenantID)
	if err != nil {
		s.d.Log.Warn("cost lookup: list providers failed", "err", err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range providers {
		if p.CostLookup != store.CostLookupSubrouter {
			continue
		}
		s.sweepProvider(ctx, p)
	}
}

func (s *Sweeper) stateFor(id string) *providerState {
	st := s.state[id]
	if st == nil {
		st = &providerState{}
		s.state[id] = st
	}
	return st
}

func (s *Sweeper) sweepProvider(ctx context.Context, p store.CustomProvider) {
	defer func() {
		if r := recover(); r != nil {
			s.d.Log.Error("cost lookup: sweep panicked", "provider", p.ID, "panic", r)
		}
	}()
	st := s.stateFor(p.ID)
	if !st.unauthorizedFor.IsZero() {
		if st.unauthorizedFor.Equal(p.UpdatedAt) {
			return
		}
		st.unauthorizedFor = time.Time{}
	}
	now := s.d.Now()
	if now.Before(st.backoffUntil) {
		return
	}

	if _, err := s.d.Usage.ExpirePendingReconcile(ctx, p.ID, now.Add(-pendingWindow)); err != nil {
		s.d.Log.Warn("cost lookup: expire failed", "provider", p.ID, "err", err)
	}
	rows, err := s.d.Usage.ListPendingReconcile(ctx, p.ID, now.Add(-pendingWindow), maxPendingRows)
	if err != nil {
		s.d.Log.Warn("cost lookup: list pending failed", "provider", p.ID, "err", err)
		return
	}
	token, err := s.d.Open(p.CostLookupToken)
	if err != nil {
		s.setStatus(ctx, p, st, tokenOpenFailure)
		return
	}
	client := &Client{BaseURL: p.CostLookupBaseURL, Token: token, UserID: p.CostLookupUserID,
		TokenName: p.CostLookupTokenName, HTTP: s.d.HTTP}

	end := now.Unix()
	start := s.windowStart(st, rows, now)
	items, err := s.readLog(ctx, client, start, end)
	if err != nil {
		if errors.Is(err, ErrUnauthorized) {
			st.unauthorizedFor = p.UpdatedAt
			s.setStatus(ctx, p, st, statusUnauthzd)
			return
		}
		st.backoff = min(max(minBackoff, st.backoff*2), maxBackoff)
		st.backoffUntil = now.Add(st.backoff)
		s.setStatus(ctx, p, st, "error: "+strings.TrimPrefix(err.Error(), transientPrefix))
		return
	}
	st.backoff, st.backoffUntil = 0, time.Time{}

	pairs, unmatched := Match(rows, items)
	if err := s.settle(ctx, pairs); err != nil {
		s.d.Log.Warn("cost lookup: settle failed", "provider", p.ID, "err", err)
		return
	}
	if err := s.recordOrphans(ctx, p.ID, unmatched, now); err != nil {
		s.d.Log.Warn("cost lookup: orphans failed", "provider", p.ID, "err", err)
	}
	st.lastEnd = end
	s.setStatus(ctx, p, st, statusOK)
	s.checkYesterday(ctx, p, st, client, now)
}

// checkYesterday compares yesterday's charge with keirouter's records once
// per UTC day, after 01:00 so late log entries have landed.
func (s *Sweeper) checkYesterday(ctx context.Context, p store.CustomProvider, st *providerState, c *Client, now time.Time) {
	now = now.UTC()
	if now.Hour() < 1 {
		return
	}
	yesterday := now.Add(-24 * time.Hour)
	day := yesterday.Format("2006-01-02")
	if st.reportedDay == day {
		return
	}
	st.reportedDay = day
	r, err := BuildReport(ctx, p.ID, yesterday, c.StatQuota, s.d.Usage)
	if err != nil {
		s.d.Log.Warn("cost lookup: daily report failed", "provider", p.ID, "day", day, "err", err)
		return
	}
	if r.Warn {
		s.d.Log.Warn("cost lookup: provider charged more than keirouter recorded", "provider", p.ID, "day", day,
			"provider_usd", r.SubrouterUSD, "recorded_usd", r.KeirouterUpstreamUSD, "gap_pct", r.GapPct,
			"orphans", r.OrphanCount, "unverified", r.Unverified)
	}
}

// windowStart reaches back to the oldest pending row and overlaps the
// previous pass, so entries written late are still seen.
func (s *Sweeper) windowStart(st *providerState, rows []store.ReconcileRow, now time.Time) int64 {
	start := now.Add(-pendingWindow).Unix()
	if st.lastEnd > 0 {
		start = st.lastEnd - int64(overlapLookback/time.Second)
	}
	if len(rows) > 0 {
		if oldest := rows[0].CreatedAt.Add(-pendingLookback).Unix(); oldest < start {
			start = oldest
		}
	}
	return start
}

func (s *Sweeper) readLog(ctx context.Context, c *Client, start, end int64) ([]LogItem, error) {
	var items []LogItem
	cursor := "first"
	for page := 0; page < maxPagesPerPass; page++ {
		pg, err := c.Logs(ctx, LogQuery{Cursor: cursor, Start: start, End: end, PageSize: pageSize})
		if err != nil {
			return nil, err
		}
		items = append(items, pg.Items...)
		if !pg.HasMore || pg.NextCursor == "" {
			return items, nil
		}
		cursor = pg.NextCursor
	}
	s.d.Log.Warn("cost lookup: page cap reached; older entries wait for the next pass", "pages", maxPagesPerPass)
	return items, nil
}

func (s *Sweeper) settle(ctx context.Context, pairs []Pair) error {
	if len(pairs) == 0 {
		return nil
	}
	settlements := make([]store.ReconcileSettlement, 0, len(pairs))
	rows := make(map[string]store.ReconcileRow, len(pairs))
	for _, pr := range pairs {
		st, ok := Settle(pr.Row, pr.Item.Quota)
		if !ok {
			st = store.ReconcileSettlement{ID: pr.Row.ID, Status: "unverified",
				CostNanos: pr.Row.CostNanos, UpstreamCostNanos: pr.Row.UpstreamCostNanos,
				InputCostNanos: pr.Row.InputCostNanos, CachedCostNanos: pr.Row.CachedCostNanos,
				CacheWriteCostNanos: pr.Row.CacheWriteCostNanos, OutputCostNanos: pr.Row.OutputCostNanos,
				ReasoningCostNanos: pr.Row.ReasoningCostNanos}
		}
		st.UpstreamRequestID = pr.Item.RequestID
		settlements = append(settlements, st)
		rows[pr.Row.ID] = pr.Row
	}
	applied, err := s.d.Usage.SettleReconciled(ctx, settlements)
	if err != nil {
		return err
	}
	byID := make(map[string]store.ReconcileSettlement, len(settlements))
	for _, st := range settlements {
		byID[st.ID] = st
	}
	for _, id := range applied {
		row, st := rows[id], byID[id]
		if delta := st.CostNanos - row.CostNanos; delta != 0 && s.d.Budget != nil {
			s.d.Budget.ObserveCostDelta(row.TenantID, row.ProjectID, row.APIKeyID, row.CreatedAt, delta)
		}
	}
	return nil
}

// recordOrphans stores entries old enough that their usage row would exist
// by now and that no row accounts for.
func (s *Sweeper) recordOrphans(ctx context.Context, provider string, unmatched []LogItem, now time.Time) error {
	cutoff := now.Add(-orphanMinAge).Unix()
	var ids []string
	var candidates []LogItem
	for _, it := range unmatched {
		if it.CreatedAt <= cutoff && it.RequestID != "" {
			ids = append(ids, it.RequestID)
			candidates = append(candidates, it)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	known, err := s.d.Usage.KnownUpstreamRequestIDs(ctx, provider, ids)
	if err != nil {
		return err
	}
	var orphans []store.CostLookupOrphan
	for _, it := range candidates {
		if known[it.RequestID] {
			continue
		}
		orphans = append(orphans, store.CostLookupOrphan{Provider: provider, RequestID: it.RequestID,
			Model: it.ModelName, PromptTokens: it.PromptTokens, CompletionTokens: it.CompletionTokens,
			Quota: it.Quota, CreatedAt: time.Unix(it.CreatedAt, 0).UTC()})
	}
	n, err := s.d.Usage.InsertCostLookupOrphans(ctx, orphans)
	if n > 0 {
		s.d.Log.Warn("cost lookup: provider charged requests with no usage row", "provider", provider, "count", n)
	}
	return err
}

// setStatus stores the outcome when it changed and logs each change once.
func (s *Sweeper) setStatus(ctx context.Context, p store.CustomProvider, st *providerState, status string) {
	if status != st.loggedStatus {
		if status == statusOK {
			s.d.Log.Info("cost lookup: reading usage log", "provider", p.ID)
		} else {
			s.d.Log.Error("cost lookup: "+status, "provider", p.ID)
		}
		st.loggedStatus = status
	}
	if status == p.CostLookupStatus {
		return
	}
	if err := s.d.Providers.UpdateCostLookupStatus(ctx, p.ID, status); err != nil {
		s.d.Log.Warn("cost lookup: status update failed", "provider", p.ID, "err", fmt.Sprint(err))
	}
}
