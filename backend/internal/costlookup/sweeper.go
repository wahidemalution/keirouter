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
	sweepInterval     = 10 * time.Second
	pendingWindow     = time.Hour
	maxPendingRows    = 5000
	maxPagesPerPass   = 50
	pageSize          = 100
	pendingLookback   = 60 * time.Second
	overlapLookback   = 15 * time.Second
	firstPassLookback = 60 * time.Second
	orphanMinAge      = 120 * time.Second
	minBackoff        = 5 * time.Second
	maxBackoff        = 60 * time.Second
	sweeperLockID     = 0x4b52434c
	statusOK          = "ok"
	statusUnauthzd    = "unauthorized"
	transientPrefix   = "costlookup: temporary failure: "
	tokenOpenFailure  = "error: stored access token cannot be read"
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
	// OnProviders receives every custom provider on each pass, on every
	// replica, so routing flags follow edits made on another replica.
	OnProviders func([]store.CustomProvider)
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
	// gaps are older log ranges not read yet because a pass hit its page
	// budget; they are read newest first on later passes.
	gaps []window
	// recent holds entries read but not matched yet, by request id, until
	// they are old enough to count as orphans.
	recent map[string]LogItem
}

type window struct{ start, end int64 }

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
	providers, err := s.d.Providers.ListProviders(ctx, store.DefaultTenantID)
	if err != nil {
		s.d.Log.Warn("cost lookup: list providers failed", "err", err)
		return
	}
	if s.d.OnProviders != nil {
		s.d.OnProviders(providers)
	}
	release, ok, err := s.d.DB.TryAdvisoryLock(ctx, sweeperLockID)
	if err != nil {
		s.d.Log.Warn("cost lookup: lock failed", "err", err)
		return
	}
	if !ok {
		return
	}
	defer release()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range providers {
		if p.CostLookup != store.CostLookupSubrouter {
			// Rows left pending from when lookup was on keep their step price.
			if _, err := s.d.Usage.ExpirePendingReconcile(ctx, p.ID, s.d.Now().Add(-pendingWindow)); err != nil {
				s.d.Log.Warn("cost lookup: expire failed", "provider", p.ID, "err", err)
			}
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

	items, gaps, err := s.readLog(ctx, client, st, rows, now)
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
	st.lastEnd, st.gaps = now.Unix(), gaps

	candidates := mergeRecent(st.recent, items)
	pairs, unmatched := Match(rows, candidates)
	if err := s.settle(ctx, pairs); err != nil {
		s.d.Log.Warn("cost lookup: settle failed", "provider", p.ID, "err", err)
		return
	}
	st.recent = map[string]LogItem{}
	orphanCutoff := now.Add(-orphanMinAge).Unix()
	var old []LogItem
	for _, it := range unmatched {
		if it.CreatedAt <= orphanCutoff {
			old = append(old, it)
		} else if it.RequestID != "" {
			st.recent[it.RequestID] = it
		}
	}
	if err := s.recordOrphans(ctx, p.ID, old); err != nil {
		s.d.Log.Warn("cost lookup: orphans failed", "provider", p.ID, "err", err)
	}
	// Rows are only given up on after the log was read successfully.
	if _, err := s.d.Usage.ExpirePendingReconcile(ctx, p.ID, now.Add(-pendingWindow)); err != nil {
		s.d.Log.Warn("cost lookup: expire failed", "provider", p.ID, "err", err)
	}
	s.setStatus(ctx, p, st, statusOK)
	s.checkYesterday(ctx, p, st, client, now)
}

// mergeRecent adds the entries still waiting for a row to this pass's
// entries, without duplicates.
func mergeRecent(recent map[string]LogItem, items []LogItem) []LogItem {
	seen := make(map[string]bool, len(items))
	out := make([]LogItem, 0, len(items)+len(recent))
	for _, it := range items {
		if it.RequestID != "" {
			if seen[it.RequestID] {
				continue
			}
			seen[it.RequestID] = true
		}
		out = append(out, it)
	}
	for id, it := range recent {
		if !seen[id] {
			out = append(out, it)
		}
	}
	return out
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

// readLog reads the new interval since the previous pass (plus a short
// overlap for entries written late), then spends the rest of the page budget
// on older unread ranges, newest first. It returns the entries read and the
// ranges still unread. On the first pass it also reaches back to the oldest
// pending row. Ranges older than the pending window are dropped.
func (s *Sweeper) readLog(ctx context.Context, c *Client, st *providerState, rows []store.ReconcileRow, now time.Time) ([]LogItem, []window, error) {
	end := now.Unix()
	start := st.lastEnd - int64(overlapLookback/time.Second)
	gaps := append([]window(nil), st.gaps...)
	if st.lastEnd == 0 {
		start = now.Add(-firstPassLookback).Unix()
		if len(rows) > 0 {
			if oldest := rows[0].CreatedAt.Add(-pendingLookback).Unix(); oldest < start {
				gaps = append(gaps, window{oldest, start - 1})
			}
		}
	}
	budget := maxPagesPerPass
	items, rest, used, err := c.readRange(ctx, window{start, end}, budget)
	if err != nil {
		return nil, nil, err
	}
	budget -= used
	var left []window
	if rest != nil {
		left = append(left, *rest)
	}
	// Newest unread range first.
	for i := len(gaps) - 1; i >= 0; i-- {
		g := gaps[i]
		if budget <= 0 {
			left = append(left, g)
			continue
		}
		got, rest, used, err := c.readRange(ctx, g, budget)
		if err != nil {
			return nil, nil, err
		}
		budget -= used
		items = append(items, got...)
		if rest != nil {
			left = append(left, *rest)
		}
	}
	horizon := now.Add(-pendingWindow).Unix()
	var kept []window
	for i := len(left) - 1; i >= 0; i-- { // oldest first, so newest is read first next pass
		if left[i].end < horizon {
			s.d.Log.Warn("cost lookup: log range older than an hour left unread", "from", left[i].start, "to", left[i].end)
			continue
		}
		left[i].start = max(left[i].start, horizon)
		kept = append(kept, left[i])
	}
	sortWindows(kept)
	if budget <= 0 && len(kept) > 0 {
		s.d.Log.Warn("cost lookup: page budget reached; older entries are read on the next pass", "pages", maxPagesPerPass)
	}
	return items, kept, nil
}

// sortWindows orders ranges oldest first, so the last one is the newest.
func sortWindows(w []window) {
	for i := 1; i < len(w); i++ {
		for j := i; j > 0 && w[j].end < w[j-1].end; j-- {
			w[j], w[j-1] = w[j-1], w[j]
		}
	}
}

// readRange pages one time range newest first using at most budget pages.
// rest is the older part not read yet (nil when the range was read fully).
func (c *Client) readRange(ctx context.Context, w window, budget int) (items []LogItem, rest *window, used int, err error) {
	cursor := "first"
	for used < budget {
		pg, err := c.Logs(ctx, LogQuery{Cursor: cursor, Start: w.start, End: w.end, PageSize: pageSize})
		if err != nil {
			return nil, nil, used, err
		}
		used++
		items = append(items, pg.Items...)
		if !pg.HasMore || pg.NextCursor == "" {
			return items, nil, used, nil
		}
		cursor = pg.NextCursor
	}
	oldest := w.end
	for _, it := range items {
		oldest = min(oldest, it.CreatedAt)
	}
	// Entries in the oldest second may be split across pages; re-reading
	// that second is harmless (settle and orphans are idempotent).
	return items, &window{w.start, oldest}, used, nil
}

func (s *Sweeper) settle(ctx context.Context, pairs []Pair) error {
	if len(pairs) == 0 {
		return nil
	}
	settlements := make([]store.ReconcileSettlement, 0, len(pairs))
	rows := make(map[string]store.ReconcileRow, len(pairs))
	for _, pr := range pairs {
		st, _ := Settle(pr.Row, pr.Item.Quota)
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
func (s *Sweeper) recordOrphans(ctx context.Context, provider string, unmatched []LogItem) error {
	var ids []string
	var candidates []LogItem
	for _, it := range unmatched {
		if it.RequestID != "" {
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
