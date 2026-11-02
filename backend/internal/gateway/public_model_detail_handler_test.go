package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/market"
	"github.com/mydisha/keirouter/backend/internal/store"
)

const detailPerfBody = `{"computed_at":1793606100000,"providers":[
 {"provider":"alpha","requests":60,"p50_latency_ms":1000,"p95_latency_ms":2000,"tps_p50":100,"uptime_pct":99.5},
 {"provider":"beta","requests":40,"p50_latency_ms":1500,"p95_latency_ms":3000,"tps_p50":80,"uptime_pct":98}
]}`

// fakeSurplus counts book and performance hits and records every path.
type fakeSurplus struct {
	srv       *httptest.Server
	bookHits  atomic.Int64
	perfHits  atomic.Int64
	failBook  atomic.Bool
	failPerf  atomic.Bool
	pathsMu   sync.Mutex
	pathsSeen []string
}

func (f *fakeSurplus) total() int64 { return f.bookHits.Load() + f.perfHits.Load() }

func (f *fakeSurplus) paths() []string {
	f.pathsMu.Lock()
	defer f.pathsMu.Unlock()
	return append([]string(nil), f.pathsSeen...)
}

func newFakeSurplus(t *testing.T) *fakeSurplus {
	t.Helper()
	f := &fakeSurplus{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.pathsMu.Lock()
		f.pathsSeen = append(f.pathsSeen, r.URL.EscapedPath())
		f.pathsMu.Unlock()
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/markets/"):
			f.bookHits.Add(1)
			if f.failBook.Load() {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			_, _ = w.Write([]byte(detailBook))
		case strings.HasPrefix(r.URL.Path, "/api/analytics/performance/"):
			f.perfHits.Add(1)
			if f.failPerf.Load() {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			_, _ = w.Write([]byte(detailPerfBody))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// detailFixture is a gateway with a market chain, a disabled chain, a chain
// led by a static step and a chain with no Surplus slug, all pointed at a
// fake Surplus.
func detailFixture(t *testing.T) (*store.DB, *Server, *fakeSurplus) {
	t.Helper()
	db, gw := newPublicTestGatewayWithDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	markup := 25.0
	offIn, offOut := 1.0, 4.0
	for _, c := range []store.Chain{
		{ID: "ds", Name: "deepseek-v4.1-flash", MarkupPercent: &markup, OfficialInputPerM: &offIn, OfficialOutputPerM: &offOut,
			MarketSlugs: []string{"surplus-trusted:deepseek-v4.1-flash"},
			Steps:       []store.ChainStep{{ID: "ds1", Provider: "custom-openai-surplus", Model: "deepseek-v4.1-flash", MarketSlug: "surplus-trusted:deepseek-v4.1-flash"}}},
		{ID: "off", Name: "disabled-model", Disabled: true, MarketSlugs: []string{"surplus-trusted:off"},
			Steps: []store.ChainStep{{ID: "off1", Provider: "custom-openai-surplus", Model: "off", MarketSlug: "surplus-trusted:off"}}},
		{ID: "lead", Name: "static-lead",
			Steps: []store.ChainStep{
				{ID: "lead1", Position: 0, Provider: "openai", Model: "gpt-4o", StaticInputPerM: 1, StaticOutputPerM: 2},
				{ID: "lead2", Position: 1, Provider: "custom-openai-surplus", Model: "gpt-4o", MarketSlug: "surplus-trusted:gpt-4o"},
			}},
		{ID: "untrusted", Name: "untrusted-only", MarketSlugs: []string{"surplus:deepseek-v4.1-flash"},
			Steps: []store.ChainStep{{ID: "u1", Provider: "custom-openai-surplus", Model: "deepseek-v4.1-flash", MarketSlug: "surplus:deepseek-v4.1-flash"}}},
		{ID: "plain", Name: "plain", InputPerM: 2, OutputPerM: 8,
			Steps: []store.ChainStep{{ID: "plain1", Provider: "openai", Model: "gpt-4o"}}},
	} {
		c.TenantID, c.Strategy, c.CreatedAt, c.UpdatedAt = store.DefaultTenantID, "priority", now, now
		require.NoError(t, db.Chains().Create(ctx, c))
	}
	cache := market.NewSnapshotCache()
	cache.Replace([]market.Model{
		market.TrustedQuote{Found: true, BestIn: 0.2, BestOut: 0.8, CapIn: 0.5, CapOut: 2, Providers: []string{"alpha"}}.Model("deepseek-v4.1-flash"),
	})
	gw.marketCache = cache
	f := newFakeSurplus(t)
	gw.surplusURL = f.srv.URL + "/api/markets"
	return db, gw, f
}

func getDetail(t *testing.T, h http.Handler, query string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/public/model-detail"+query, nil)
	req.RemoteAddr = "203.0.113.9:40000"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	if rec.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	}
	return rec, out
}

func requireDetailError(t *testing.T, rec *httptest.ResponseRecorder, status int, msg string) {
	t.Helper()
	require.Equal(t, status, rec.Code, rec.Body.String())
	var body struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, msg, body.Error.Message)
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
}

func TestModelDetailUnknownModel404(t *testing.T) {
	_, gw, f := detailFixture(t)
	rec, _ := getDetail(t, gw.routes(), "?model=nope")
	requireDetailError(t, rec, http.StatusNotFound, "model not found")
	rec, _ = getDetail(t, gw.routes(), "?model=++")
	requireDetailError(t, rec, http.StatusNotFound, "model not found")
	rec, _ = getDetail(t, gw.routes(), "")
	requireDetailError(t, rec, http.StatusNotFound, "model not found")
	require.Zero(t, f.total())
}

func TestModelDetailDisabledChain404(t *testing.T) {
	_, gw, f := detailFixture(t)
	rec, _ := getDetail(t, gw.routes(), "?model=disabled-model")
	requireDetailError(t, rec, http.StatusNotFound, "model not found")
	require.Zero(t, f.total())
}

func TestModelDetailFixedChain404(t *testing.T) {
	_, gw, f := detailFixture(t)
	rec, _ := getDetail(t, gw.routes(), "?model=static-lead")
	requireDetailError(t, rec, http.StatusNotFound, "no market detail")
	rec, _ = getDetail(t, gw.routes(), "?model=plain")
	requireDetailError(t, rec, http.StatusNotFound, "no market detail")
	require.Zero(t, f.total())
}

func TestModelDetailOversizedModel404NoUpstream(t *testing.T) {
	_, gw, f := detailFixture(t)
	rec, _ := getDetail(t, gw.routes(), "?model="+strings.Repeat("a", 201))
	requireDetailError(t, rec, http.StatusNotFound, "model not found")
	require.Zero(t, f.total())
}

func TestModelDetailIgnoresQueryForUpstreamPath(t *testing.T) {
	_, gw, f := detailFixture(t)
	rec, out := getDetail(t, gw.routes(), "?model=DeepSeek-V4.1-Flash")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "deepseek-v4.1-flash", out["model"])

	before := f.total()
	rec, _ = getDetail(t, gw.routes(), "?model=../x")
	requireDetailError(t, rec, http.StatusNotFound, "model not found")
	rec, _ = getDetail(t, gw.routes(), "?model=deepseek-v4.1-flash%2F..%2F..%2Fadmin")
	requireDetailError(t, rec, http.StatusNotFound, "model not found")
	require.Equal(t, before, f.total())

	paths := f.paths()
	require.NotEmpty(t, paths)
	for _, p := range paths {
		require.NotContains(t, p, "..")
		require.True(t, strings.HasSuffix(p, "/deepseek-v4.1-flash"), p)
	}
}

func TestModelDetailCachesBook30s(t *testing.T) {
	_, gw, f := detailFixture(t)
	clock := time.Now()
	var mu sync.Mutex
	gw.detail.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }

	for range 2 {
		rec, _ := getDetail(t, gw.routes(), "?model=deepseek-v4.1-flash")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	}
	require.EqualValues(t, 1, f.bookHits.Load())
	require.EqualValues(t, 1, f.perfHits.Load())

	mu.Lock()
	clock = clock.Add(31 * time.Second)
	mu.Unlock()
	rec, _ := getDetail(t, gw.routes(), "?model=deepseek-v4.1-flash")
	require.Equal(t, http.StatusOK, rec.Code)
	require.EqualValues(t, 2, f.bookHits.Load())
	require.EqualValues(t, 1, f.perfHits.Load())
}

// A changed blocked list must not serve offers filtered under the old one.
func TestModelDetailBlockedChangeRefetches(t *testing.T) {
	_, gw, f := detailFixture(t)
	rec, out := getDetail(t, gw.routes(), "?model=deepseek-v4.1-flash")
	require.Equal(t, http.StatusOK, rec.Code)
	require.EqualValues(t, 3, out["offer_count"])

	require.NoError(t, gw.settings.Set(context.Background(), market.SettingsKey, `{"markup_percent":10,"surplus_blocked_providers":["alpha"]}`))
	rec, out = getDetail(t, gw.routes(), "?model=deepseek-v4.1-flash")
	require.Equal(t, http.StatusOK, rec.Code)
	require.EqualValues(t, 2, f.bookHits.Load())
	require.EqualValues(t, 2, out["offer_count"])
}

func TestModelDetailServesStaleOnUpstreamError(t *testing.T) {
	_, gw, f := detailFixture(t)
	clock := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	gw.detail.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }

	rec, first := getDetail(t, gw.routes(), "?model=deepseek-v4.1-flash")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "2026-11-02T08:00:00Z", first["as_of"])

	f.failBook.Store(true)
	f.failPerf.Store(true)
	mu.Lock()
	clock = clock.Add(10 * time.Minute)
	mu.Unlock()
	rec, out := getDetail(t, gw.routes(), "?model=deepseek-v4.1-flash")
	require.Equal(t, http.StatusOK, rec.Code)
	require.EqualValues(t, 2, f.bookHits.Load())
	require.EqualValues(t, 2, f.perfHits.Load())
	require.Equal(t, "2026-11-02T08:00:00Z", out["as_of"])
	require.Equal(t, first["best"], out["best"])
	require.Equal(t, first["performance"], out["performance"])
	require.NotContains(t, rec.Body.String(), "boom")
}

func TestModelDetailNoCacheOnError(t *testing.T) {
	_, gw, f := detailFixture(t)
	f.failBook.Store(true)
	f.failPerf.Store(true)
	rec, out := getDetail(t, gw.routes(), "?model=deepseek-v4.1-flash")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Nil(t, out["best"])
	require.Nil(t, out["performance"])
	require.Nil(t, out["offer_count"])
	require.Nil(t, out["as_of"])
	require.NotContains(t, rec.Body.String(), "boom")
	require.NotContains(t, rec.Body.String(), "127.0.0.1")
}

// Without the chain's trusted slug in the routing snapshot there is no
// billed floor to price against, so no price is shown.
func TestModelDetailNoSnapshotNoPrice(t *testing.T) {
	_, gw, _ := detailFixture(t)
	gw.marketCache = market.NewSnapshotCache()
	rec, out := getDetail(t, gw.routes(), "?model=deepseek-v4.1-flash")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Nil(t, out["best"])
	require.Nil(t, out["offer_count"])
	require.NotNil(t, out["performance"])
}

func TestModelDetailHeaders(t *testing.T) {
	_, gw, _ := detailFixture(t)
	rec, _ := getDetail(t, gw.routes(), "?model=deepseek-v4.1-flash")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "public, max-age=30", rec.Header().Get("Cache-Control"))
	rec, _ = getDetail(t, gw.routes(), "?model=nope")
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
}

func TestModelDetailDoesNotTouchSnapshot(t *testing.T) {
	_, gw, _ := detailFixture(t)
	slugs := []string{"surplus-trusted:deepseek-v4.1-flash"}
	_, err := gw.surplusPrices(context.Background())
	require.NoError(t, err)
	beforeModels := gw.marketCache.Models(slugs)
	beforeAt := gw.marketCache.FetchedAt()
	gw.pricing.mu.Lock()
	beforePrices, beforeBuilt := gw.pricing.byName, gw.pricing.builtAt
	gw.pricing.mu.Unlock()

	rec, _ := getDetail(t, gw.routes(), "?model=deepseek-v4.1-flash")
	require.Equal(t, http.StatusOK, rec.Code)

	require.Equal(t, beforeModels, gw.marketCache.Models(slugs))
	require.Equal(t, beforeAt, gw.marketCache.FetchedAt())
	gw.pricing.mu.Lock()
	defer gw.pricing.mu.Unlock()
	require.Equal(t, beforePrices, gw.pricing.byName)
	require.Equal(t, beforeBuilt, gw.pricing.builtAt)
}

func TestModelDetailRequests24h(t *testing.T) {
	db, gw, _ := detailFixture(t)
	now := time.Now().UTC()
	require.NoError(t, db.Usage().RecordBatch(context.Background(), []store.UsageRecord{
		{ID: "u1", TenantID: store.DefaultTenantID, ChainID: "ds", Status: "success", CreatedAt: now.Add(-time.Hour)},
		{ID: "u2", TenantID: store.DefaultTenantID, ChainID: "ds", Status: "success", CreatedAt: now.Add(-25 * time.Hour)},
	}))
	rec, out := getDetail(t, gw.routes(), "?model=deepseek-v4.1-flash")
	require.Equal(t, http.StatusOK, rec.Code)
	require.EqualValues(t, 1, out["requests_24h"])
}

func TestModelDetailLimitsPerIP(t *testing.T) {
	_, gw, _ := detailFixture(t)
	h := gw.routes()
	for i := range 60 {
		rec, _ := getDetail(t, h, "?model=nope")
		require.Equal(t, http.StatusNotFound, rec.Code, "request %d", i)
	}
	rec, _ := getDetail(t, h, "?model=nope")
	requireDetailError(t, rec, http.StatusTooManyRequests, "rate limit exceeded: at most 60 detail requests per minute")
	require.NotEmpty(t, rec.Header().Get("Retry-After"))
}

// After a failure, no refetch for detailRetryAfter: an outage must not turn
// into a retry storm.
func TestModelDetailFailureCooldown(t *testing.T) {
	_, gw, f := detailFixture(t)
	clock := time.Now()
	var mu sync.Mutex
	gw.detail.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	f.failBook.Store(true)
	f.failPerf.Store(true)

	for range 2 {
		rec, out := getDetail(t, gw.routes(), "?model=deepseek-v4.1-flash")
		require.Equal(t, http.StatusOK, rec.Code)
		require.Nil(t, out["best"])
		require.Nil(t, out["performance"])
	}
	require.EqualValues(t, 1, f.bookHits.Load())
	require.EqualValues(t, 1, f.perfHits.Load())

	f.failBook.Store(false)
	f.failPerf.Store(false)
	mu.Lock()
	clock = clock.Add(16 * time.Second)
	mu.Unlock()
	rec, out := getDetail(t, gw.routes(), "?model=deepseek-v4.1-flash")
	require.Equal(t, http.StatusOK, rec.Code)
	require.EqualValues(t, 2, f.bookHits.Load())
	require.EqualValues(t, 2, f.perfHits.Load())
	require.NotNil(t, out["best"])
	require.NotNil(t, out["performance"])
}

// A chain priced only from a plain surplus: slug does not route through
// trusted offers, so it gets no price; performance still shows, with no
// routes.
func TestModelDetailSurplusOnlyChainNoPrice(t *testing.T) {
	_, gw, f := detailFixture(t)
	gw.marketCache.Replace([]market.Model{{Slug: "surplus:deepseek-v4.1-flash", MinAskIn: 0.2, MinAskOut: 0.8}})
	rec, out := getDetail(t, gw.routes(), "?model=untrusted-only")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Nil(t, out["best"])
	require.Nil(t, out["median_discount_pct"])
	require.Nil(t, out["offer_count"])
	require.Zero(t, f.bookHits.Load())
	perf := out["performance"].(map[string]any)
	require.Empty(t, perf["routes"])
}
