package costlookup

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/config"
	"github.com/mydisha/keirouter/backend/internal/store"
)

const provID = "custom-openai-subrouter"

type deltaCall struct {
	Tenant, Project, Key string
	At                   time.Time
	Delta                int64
}

type fakeBudget struct {
	mu    sync.Mutex
	calls []deltaCall
}

func (f *fakeBudget) ObserveCostDelta(tenant, project, key string, at time.Time, d int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, deltaCall{tenant, project, key, at, d})
}

// fakeLog serves the usage log. respond decides each reply; requests counts calls.
type fakeLog struct {
	mu       sync.Mutex
	requests int
	queries  []map[string]string
	respond  func(n int, q map[string]string) (int, any)
}

func (f *fakeLog) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests++
	n := f.requests
	q := map[string]string{"authorization": r.Header.Get("Authorization"), "user": r.Header.Get("New-Api-User"), "path": r.URL.Path}
	for k := range r.URL.Query() {
		q[k] = r.URL.Query().Get(k)
	}
	f.queries = append(f.queries, q)
	f.mu.Unlock()
	status, body := f.respond(n, q)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// count returns how many usage-log page requests were made (stat calls excluded).
func (f *fakeLog) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, q := range f.queries {
		if q["path"] == "/api/log/self" {
			n++
		}
	}
	return n
}

func pageOf(items []LogItem, hasMore bool, next string) map[string]any {
	return map[string]any{"success": true, "message": "",
		"data": map[string]any{"items": items, "has_more": hasMore, "next_cursor": next, "page_size": 100}}
}

type harness struct {
	db     *store.DB
	log    *fakeLog
	budget *fakeBudget
	now    time.Time
	sw     *Sweeper
}

func newHarness(t *testing.T, respond func(n int, q map[string]string) (int, any)) *harness {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: ":memory:"}, t.TempDir())
	require.NoError(t, err)
	require.NoError(t, db.Migrate(ctx))
	require.NoError(t, db.Tenants().EnsureDefault(ctx))
	t.Cleanup(func() { _ = db.Close() })

	fl := &fakeLog{respond: respond}
	srv := httptest.NewServer(fl)
	t.Cleanup(srv.Close)
	require.NoError(t, db.CustomProviders().CreateProvider(ctx, store.CustomProvider{
		ID: provID, TenantID: store.DefaultTenantID, DisplayName: "Subrouter", Alias: "subrouter",
		Dialect: "openai", BaseURL: "https://asiasouth.up.railway.app/v1",
		CostLookup: store.CostLookupSubrouter, CostLookupBaseURL: srv.URL,
		CostLookupUserID: "19579", CostLookupToken: "sealed-token",
	}))

	h := &harness{db: db, log: fl, budget: &fakeBudget{}, now: time.Unix(1793458400, 0).UTC()}
	h.sw = New(Deps{
		Usage: db.Usage(), Providers: db.CustomProviders(), DB: db,
		Open:   func(s string) (string, error) { return "plain-" + s, nil },
		Budget: h.budget, HTTP: srv.Client(), Log: slog.Default(),
		Now: func() time.Time { return h.now },
	})
	return h
}

// seed stores a provisional row billed at 150000 nanos for 100000 upstream.
func (h *harness) seed(t *testing.T, id, upstreamID string, at time.Time, prompt, completion int) {
	t.Helper()
	require.NoError(t, h.db.Usage().Record(context.Background(), store.UsageRecord{
		ID: id, TenantID: store.DefaultTenantID, APIKeyID: "k1", Provider: provID, Model: "deepseek-v4.1-flash",
		Status: "success", ReconcileStatus: "pending", UpstreamRequestID: upstreamID,
		PromptTokens: prompt, CompletionTokens: completion, CostNanos: 150000, UpstreamCostNanos: 100000,
		OutputCostNanos: 150000, CreatedAt: at,
	}))
}

func (h *harness) row(t *testing.T, id string) (status string, cost int64, upstreamID string) {
	t.Helper()
	require.NoError(t, h.db.SQL().QueryRowContext(context.Background(),
		`SELECT reconcile_status, cost_nanos, upstream_request_id FROM usage_records WHERE id = ?`, id).Scan(&status, &cost, &upstreamID))
	return
}

func (h *harness) status(t *testing.T) string {
	t.Helper()
	p, err := h.db.CustomProviders().GetProvider(context.Background(), provID)
	require.NoError(t, err)
	return p.CostLookupStatus
}

func (h *harness) orphans(t *testing.T) int {
	t.Helper()
	var n int
	require.NoError(t, h.db.SQL().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM cost_lookup_orphans`).Scan(&n))
	return n
}

func logItem(id string, at time.Time, prompt, completion int, quota int64) LogItem {
	return LogItem{CreatedAt: at.Unix(), ModelName: "deepseek-v4.1-flash", PromptTokens: prompt,
		CompletionTokens: completion, Quota: quota, RequestID: id, IsStream: true}
}

func TestSweepSettlesStreamAndFingerprint(t *testing.T) {
	var h *harness
	h = newHarness(t, func(int, map[string]string) (int, any) {
		at := h.now.Add(-time.Minute)
		return 200, pageOf([]LogItem{
			logItem("20261031145028472218319QeMWIGBK", at, 872, 515, 17),
			logItem("20261031145059384885689jMgnd0ql", at.Add(2*time.Second), 900, 20, 1000),
		}, false, "")
	})
	at := h.now.Add(-time.Minute)
	h.seed(t, "stream", "20261031145028472218319QeMWIGBK", at, 872, 515)
	h.seed(t, "plain", "", at, 900, 20)

	h.sw.Pass(context.Background())

	st, cost, _ := h.row(t, "stream")
	require.Equal(t, "reconciled", st)
	require.Equal(t, int64(51000), cost, "17 quota × 2000 × 1.5")
	st, cost, uid := h.row(t, "plain")
	require.Equal(t, "over_ceiling", st)
	require.Equal(t, int64(3000000), cost, "1000 quota × 2000 × 1.5")
	require.Equal(t, "20261031145059384885689jMgnd0ql", uid, "the matched log id is stored on the row")
	require.Equal(t, "ok", h.status(t))
	require.Equal(t, "2", h.log.queries[0]["type"])
	require.Equal(t, "Bearer plain-sealed-token", h.log.queries[0]["authorization"], "the stored token is opened before use")
	require.Equal(t, "19579", h.log.queries[0]["user"])
}

func TestSweepPushesCounterDelta(t *testing.T) {
	var h *harness
	h = newHarness(t, func(int, map[string]string) (int, any) {
		return 200, pageOf([]LogItem{logItem("A", h.now.Add(-time.Minute), 872, 515, 17)}, false, "")
	})
	at := h.now.Add(-time.Minute)
	h.seed(t, "r1", "A", at, 872, 515)

	h.sw.Pass(context.Background())

	require.Equal(t, []deltaCall{{Tenant: store.DefaultTenantID, Key: "k1", At: at, Delta: 51000 - 150000}}, h.budget.calls)

	h.sw.Pass(context.Background())
	require.Len(t, h.budget.calls, 1, "a settled row is never charged again")
}

func TestSweepRecordsOrphanOnce(t *testing.T) {
	start := time.Unix(1793458400, 0).UTC()
	entries := []LogItem{
		logItem("old-orphan", start.Add(-130*time.Second), 5, 5, 3),
		logItem("young", start.Add(-60*time.Second), 6, 6, 3),
		logItem("matched", start.Add(-200*time.Second), 872, 515, 17),
	}
	h := newHarness(t, func(int, map[string]string) (int, any) { return 200, pageOf(entries, false, "") })
	h.seed(t, "r1", "matched", start.Add(-200*time.Second), 872, 515)

	h.sw.Pass(context.Background())
	require.Equal(t, 1, h.orphans(t), "only the unmatched entry older than 120 s")

	h.now = h.now.Add(10 * time.Second)
	h.sw.Pass(context.Background())
	require.Equal(t, 1, h.orphans(t), "re-reading the same entries adds nothing; young is 70 s old")

	h.now = h.now.Add(60 * time.Second)
	h.sw.Pass(context.Background())
	require.Equal(t, 2, h.orphans(t), "young is 130 s old now")
}

func TestSweepFollowsCursorAndCapsPages(t *testing.T) {
	h := newHarness(t, func(n int, q map[string]string) (int, any) {
		return 200, pageOf(nil, true, "c"+strconv.Itoa(n))
	})
	h.sw.Pass(context.Background())
	require.Equal(t, 50, h.log.count())
	require.Equal(t, "first", h.log.queries[0]["cursor"])
	require.Equal(t, "c1", h.log.queries[1]["cursor"])
}

func TestSweepExpiresStaleRows(t *testing.T) {
	var h *harness
	h = newHarness(t, func(int, map[string]string) (int, any) { return 200, pageOf(nil, false, "") })
	h.seed(t, "stale", "", h.now.Add(-61*time.Minute), 1, 1)
	h.sw.Pass(context.Background())
	st, cost, _ := h.row(t, "stale")
	require.Equal(t, "unverified", st)
	require.Equal(t, int64(150000), cost, "keeps the ceiling bill")
}

func TestSweepUnauthorizedStopsProvider(t *testing.T) {
	h := newHarness(t, func(int, map[string]string) (int, any) { return 401, map[string]any{"success": false} })
	h.seed(t, "r1", "A", h.now.Add(-time.Minute), 1, 1)

	h.sw.Pass(context.Background())
	require.Equal(t, "unauthorized", h.status(t))
	st, _, _ := h.row(t, "r1")
	require.Equal(t, "pending", st)

	h.now = h.now.Add(time.Hour)
	h.sw.Pass(context.Background())
	require.Equal(t, 1, h.log.count(), "no further requests until the settings change")
}

func TestSweepResumesAfterSettingsChange(t *testing.T) {
	h := newHarness(t, func(n int, _ map[string]string) (int, any) {
		if n == 1 {
			return 401, map[string]any{"success": false}
		}
		return 200, pageOf(nil, false, "")
	})
	h.sw.Pass(context.Background())
	require.Equal(t, "unauthorized", h.status(t))

	ctx := context.Background()
	p, err := h.db.CustomProviders().GetProvider(ctx, provID)
	require.NoError(t, err)
	p.CostLookupToken = "new-sealed-token"
	p.CostLookupStatus = ""
	time.Sleep(1100 * time.Millisecond) // updated_at has second precision
	require.NoError(t, h.db.CustomProviders().UpdateProvider(ctx, p))

	h.sw.Pass(ctx)
	require.Equal(t, 2, h.log.count())
	require.Equal(t, "ok", h.status(t))
}

func TestSweepBackoffOnTransient(t *testing.T) {
	h := newHarness(t, func(int, map[string]string) (int, any) { return 429, map[string]any{} })
	ctx := context.Background()
	base := h.now

	h.sw.Pass(ctx)
	require.Equal(t, "error: rate limited", h.status(t))
	require.Equal(t, 1, h.log.count())

	h.now = base.Add(4 * time.Second)
	h.sw.Pass(ctx)
	require.Equal(t, 1, h.log.count(), "waits 5 s after the first failure")

	h.now = base.Add(5 * time.Second)
	h.sw.Pass(ctx)
	require.Equal(t, 2, h.log.count())

	h.now = base.Add(14 * time.Second)
	h.sw.Pass(ctx)
	require.Equal(t, 2, h.log.count(), "then 10 s")
	h.now = base.Add(15 * time.Second)
	h.sw.Pass(ctx)
	require.Equal(t, 3, h.log.count())

	for i := 0; i < 6; i++ {
		h.now = h.now.Add(61 * time.Second)
		h.sw.Pass(ctx)
	}
	require.Equal(t, 9, h.log.count(), "capped at 60 s")
}

func TestSweepUnknownMultiplierMarksUnverified(t *testing.T) {
	var h *harness
	h = newHarness(t, func(int, map[string]string) (int, any) {
		return 200, pageOf([]LogItem{logItem("A", h.now.Add(-time.Minute), 1, 1, 17)}, false, "")
	})
	require.NoError(t, h.db.Usage().Record(context.Background(), store.UsageRecord{
		ID: "zero", TenantID: store.DefaultTenantID, Provider: provID, Model: "deepseek-v4.1-flash",
		Status: "success", ReconcileStatus: "pending", UpstreamRequestID: "A", PromptTokens: 1, CompletionTokens: 1,
		CostNanos: 500, UpstreamCostNanos: 0, CreatedAt: h.now.Add(-time.Minute),
	}))
	h.sw.Pass(context.Background())
	st, cost, _ := h.row(t, "zero")
	require.Equal(t, "unverified", st)
	require.Equal(t, int64(34000), cost, "floored at Subrouter's charge (quota 17)")
	require.Len(t, h.budget.calls, 1)
	require.Equal(t, int64(34000-500), h.budget.calls[0].Delta)
	require.Equal(t, 0, h.orphans(t), "the entry is accounted for by the row")
}

func TestSweepSkipsProvidersWithoutCostLookup(t *testing.T) {
	h := newHarness(t, func(int, map[string]string) (int, any) { return 200, pageOf(nil, false, "") })
	ctx := context.Background()
	p, err := h.db.CustomProviders().GetProvider(ctx, provID)
	require.NoError(t, err)
	p.CostLookup = ""
	require.NoError(t, h.db.CustomProviders().UpdateProvider(ctx, p))
	h.sw.Pass(ctx)
	require.Equal(t, 0, h.log.count())
}

type countingHandler struct {
	mu    sync.Mutex
	warns []string
}

func (c *countingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (c *countingHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Level == slog.LevelWarn {
		c.mu.Lock()
		c.warns = append(c.warns, r.Message)
		c.mu.Unlock()
	}
	return nil
}
func (c *countingHandler) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *countingHandler) WithGroup(string) slog.Handler      { return c }

func (c *countingHandler) count(msg string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, m := range c.warns {
		if m == msg {
			n++
		}
	}
	return n
}

func TestSweepWarnsDailyGapOnce(t *testing.T) {
	statCalls := 0
	h := newHarness(t, func(_ int, q map[string]string) (int, any) {
		if q["path"] == "/api/log/self/stat" {
			statCalls++
			return 200, map[string]any{"success": true, "data": map[string]any{"quota": 50000}}
		}
		return 200, pageOf(nil, false, "")
	})
	logs := &countingHandler{}
	h.sw.d.Log = slog.New(logs)
	h.now = time.Date(2026, 10, 31, 0, 30, 0, 0, time.UTC)

	h.sw.Pass(context.Background())
	require.Equal(t, 0, statCalls, "yesterday is not checked before 01:00 UTC")

	h.now = time.Date(2026, 10, 31, 1, 5, 0, 0, time.UTC)
	h.sw.Pass(context.Background())
	h.now = h.now.Add(10 * time.Second)
	h.sw.Pass(context.Background())
	require.Equal(t, 1, statCalls)
	require.Equal(t, 1, logs.count("cost lookup: provider charged more than keirouter recorded"))

	h.now = time.Date(2026, 11, 1, 2, 0, 0, 0, time.UTC)
	h.sw.Pass(context.Background())
	require.Equal(t, 2, statCalls, "next day is checked again")
}
