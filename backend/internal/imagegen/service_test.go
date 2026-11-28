package imagegen

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/budget"
	"github.com/mydisha/keirouter/backend/internal/config"
	"github.com/mydisha/keirouter/backend/internal/core"
	"github.com/mydisha/keirouter/backend/internal/dispatch"
	"github.com/mydisha/keirouter/backend/internal/meter"
	"github.com/mydisha/keirouter/backend/internal/pipeline"
	"github.com/mydisha/keirouter/backend/internal/store"
)

// priceStd and price2K are the sell prices of stepA: $0.04 and $0.08 × 1.5.
const (
	priceStd int64 = 60_000
	price2K  int64 = 120_000
)

var stepA = dispatch.Target{Provider: "openai", Model: "gpt-image-1", ImagePrice: 0.04, ImagePrice2K: 0.08, ImageBillMult: 1.5}

func pngBytes(tag string) []byte {
	return append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), tag...)
}

func b64Item(tag string) core.ImageData {
	return core.ImageData{B64JSON: base64.StdEncoding.EncodeToString(pngBytes(tag))}
}

type genFunc func(ctx context.Context, req *core.ImageRequest, opts pipeline.MediaOptions) (*core.ImageResponse, dispatch.Target, error)

type fakeGen struct {
	mu    sync.Mutex
	calls []pipeline.MediaOptions
	fn    genFunc
}

func (g *fakeGen) GenerateImageTarget(ctx context.Context, req *core.ImageRequest, opts pipeline.MediaOptions) (*core.ImageResponse, dispatch.Target, error) {
	g.mu.Lock()
	g.calls = append(g.calls, opts)
	g.mu.Unlock()
	return g.fn(ctx, req, opts)
}

func (g *fakeGen) callCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.calls)
}

// nImages returns req.N distinct images tagged with the prompt.
func nImages(_ context.Context, req *core.ImageRequest, opts pipeline.MediaOptions) (*core.ImageResponse, dispatch.Target, error) {
	resp := &core.ImageResponse{}
	for i := range req.N {
		resp.Data = append(resp.Data, b64Item(fmt.Sprintf("%s|%d", req.Prompt, i)))
	}
	return resp, opts.Targets[0], nil
}

// countingBudget counts Confirm and Release per reservation.
type countingBudget struct {
	*budget.Engine
	mu       sync.Mutex
	reserved []uint64
	settled  map[uint64]int
}

func (c *countingBudget) Reserve(ctx context.Context, s budget.Scope, est budget.Estimate) (budget.Reservation, error) {
	r, err := c.Engine.Reserve(ctx, s, est)
	if err == nil {
		c.mu.Lock()
		c.reserved = append(c.reserved, r.ID)
		c.mu.Unlock()
	}
	return r, err
}

func (c *countingBudget) Confirm(s budget.Scope, m int64) {
	c.mu.Lock()
	c.settled[s.Reservation]++
	c.mu.Unlock()
	c.Engine.Confirm(s, m)
}

func (c *countingBudget) Release(s budget.Scope) {
	c.mu.Lock()
	c.settled[s.Reservation]++
	c.mu.Unlock()
	c.Engine.Release(s)
}

type env struct {
	t       *testing.T
	db      *store.DB
	bud     *countingBudget
	gen     *fakeGen
	svc     *Service
	dataDir string
	done    chan string
}

func newEnv(t *testing.T, fn genFunc) *env {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: ":memory:"}, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.Migrate(ctx))
	require.NoError(t, db.Tenants().EnsureDefault(ctx))
	eng := budget.New(db.Budgets(), db.Usage())
	t.Cleanup(eng.Close)
	m := meter.New(db.Usage(), nil, nil)
	m.AddUsageTracker(eng)
	e := &env{
		t: t, db: db, bud: &countingBudget{Engine: eng, settled: map[uint64]int{}},
		gen: &fakeGen{fn: fn}, dataDir: t.TempDir(), done: make(chan string, 1024),
	}
	e.svc = New(Deps{
		Jobs: db.ImageJobs(), Generator: e.gen, Budget: e.bud, Meter: m,
		DataDir: e.dataDir, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	e.svc.onDone = func(id, _ string) { e.done <- id }
	return e
}

// start runs the workers until the test ends.
func (e *env) start() {
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { e.svc.Run(ctx); close(stopped) }()
	e.t.Cleanup(func() { cancel(); <-stopped })
}

// key creates an API key whose prepaid balance is balance micro-USD.
func (e *env) key(id string, balance int64, hardCutoff bool) store.APIKey {
	e.t.Helper()
	now := time.Now()
	require.NoError(e.t, e.db.Budgets().Create(context.Background(), store.Budget{
		ID: "b-" + id, TenantID: store.DefaultTenantID, ScopeKind: store.ScopeAPIKey, ScopeID: id,
		LimitMicros: balance, Period: "total", AlertPct: 80, HardCutoff: hardCutoff, CreatedAt: now, UpdatedAt: now,
	}))
	return store.APIKey{ID: id, TenantID: store.DefaultTenantID, ProjectID: "p1"}
}

func (e *env) spend(keyID string) int64 {
	e.t.Helper()
	spent, _, err := e.db.Usage().SpendAndTokens(context.Background(), store.ScopeAPIKey, keyID, budget.PeriodStart("total", time.Now()))
	require.NoError(e.t, err)
	return spent
}

func input(owner string, key store.APIKey, n int, targets ...dispatch.Target) SubmitInput {
	if len(targets) == 0 {
		targets = []dispatch.Target{stepA}
	}
	return SubmitInput{OwnerSub: owner, Key: key, ChainID: "chain-img", Targets: targets, Prompt: "a cat " + owner, N: n}
}

func (e *env) waitFor(ids ...string) {
	e.t.Helper()
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	timeout := time.After(30 * time.Second)
	for len(want) > 0 {
		select {
		case id := <-e.done:
			delete(want, id)
		case <-timeout:
			e.t.Fatalf("jobs never finished: %v", want)
		}
	}
}

func (e *env) submitWait(in SubmitInput) store.ImageJob {
	e.t.Helper()
	job, err := e.svc.Submit(context.Background(), in)
	require.NoError(e.t, err)
	e.waitFor(job.ID)
	got, err := e.db.ImageJobs().Get(context.Background(), in.OwnerSub, job.ID)
	require.NoError(e.t, err)
	return got
}

// requireSettledOnce asserts every reservation was confirmed or released exactly once.
func (e *env) requireSettledOnce() {
	e.t.Helper()
	e.bud.mu.Lock()
	defer e.bud.mu.Unlock()
	for _, id := range e.bud.reserved {
		require.Equal(e.t, 1, e.bud.settled[id], "reservation %d", id)
	}
	require.Len(e.t, e.bud.settled, len(e.bud.reserved), "no settle for an unknown reservation")
}

func TestSubmitExactBalanceSucceeds(t *testing.T) {
	e := newEnv(t, nImages)
	e.start()
	key := e.key("k1", priceStd, true)
	job := e.submitWait(input("A", key, 1))
	require.Equal(t, store.ImageJobSucceeded, job.Status, job.Error)
	require.Equal(t, 1, job.ImageCount)
	require.Equal(t, priceStd, job.CostMicro)
	require.Equal(t, int64(40_000), job.UpstreamCostMicro)
	require.Equal(t, priceStd, e.spend("k1"), "the balance is now 0")
	e.requireSettledOnce()
}

func TestPartialImagesBilledByStoredCount(t *testing.T) {
	e := newEnv(t, func(_ context.Context, req *core.ImageRequest, _ pipeline.MediaOptions) (*core.ImageResponse, dispatch.Target, error) {
		return &core.ImageResponse{Data: []core.ImageData{b64Item("1"), b64Item("2")}}, stepA, nil
	})
	e.start()
	key := e.key("k1", 4*priceStd, true)
	job := e.submitWait(input("A", key, 4))
	require.Equal(t, store.ImageJobSucceeded, job.Status)
	require.Equal(t, 2, job.ImageCount)
	require.Len(t, job.Files, 2)
	require.Equal(t, 2*priceStd, job.CostMicro)
	require.Equal(t, 2*priceStd, e.spend("k1"))
	e.requireSettledOnce()
}

func TestLostImageNotBilled(t *testing.T) {
	e := newEnv(t, func(context.Context, *core.ImageRequest, pipeline.MediaOptions) (*core.ImageResponse, dispatch.Target, error) {
		return &core.ImageResponse{Data: []core.ImageData{b64Item("ok"), {B64JSON: "!!not base64!!"}}}, stepA, nil
	})
	e.start()
	key := e.key("k1", 2*priceStd, true)
	job := e.submitWait(input("A", key, 2))
	require.Equal(t, store.ImageJobSucceeded, job.Status)
	require.Equal(t, 1, job.ImageCount)
	require.Equal(t, priceStd, e.spend("k1"))
}

func TestNonImageBytesNotBilled(t *testing.T) {
	e := newEnv(t, func(context.Context, *core.ImageRequest, pipeline.MediaOptions) (*core.ImageResponse, dispatch.Target, error) {
		html := base64.StdEncoding.EncodeToString([]byte("<html>error</html>"))
		return &core.ImageResponse{Data: []core.ImageData{{B64JSON: html}}}, stepA, nil
	})
	e.start()
	key := e.key("k1", priceStd, true)
	job := e.submitWait(input("A", key, 1))
	require.Equal(t, store.ImageJobFailed, job.Status)
	require.Zero(t, e.spend("k1"))
	e.requireSettledOnce()
}

func TestExtraItemsNotBilled(t *testing.T) {
	e := newEnv(t, func(context.Context, *core.ImageRequest, pipeline.MediaOptions) (*core.ImageResponse, dispatch.Target, error) {
		return &core.ImageResponse{Data: []core.ImageData{b64Item("1"), b64Item("2"), b64Item("3")}}, stepA, nil
	})
	e.start()
	key := e.key("k1", priceStd, true)
	job := e.submitWait(input("A", key, 1))
	require.Equal(t, 1, job.ImageCount)
	require.Equal(t, priceStd, e.spend("k1"))
}

func TestFailedJobReleasesReservation(t *testing.T) {
	fail := true
	var mu sync.Mutex
	e := newEnv(t, func(ctx context.Context, req *core.ImageRequest, opts pipeline.MediaOptions) (*core.ImageResponse, dispatch.Target, error) {
		mu.Lock()
		f := fail
		mu.Unlock()
		if f {
			return nil, dispatch.Target{}, &core.ProviderError{Kind: core.ErrUpstream, Message: "content policy violation"}
		}
		return nImages(ctx, req, opts)
	})
	e.start()
	key := e.key("k1", priceStd, true)
	job := e.submitWait(input("A", key, 1))
	require.Equal(t, store.ImageJobFailed, job.Status)
	require.Equal(t, msgModeration, job.Error, "the user sees a fixed message, not the provider text")
	require.Zero(t, job.CostMicro)
	require.Zero(t, e.spend("k1"))

	mu.Lock()
	fail = false
	mu.Unlock()
	job = e.submitWait(input("A", key, 1))
	require.Equal(t, store.ImageJobSucceeded, job.Status, "the released reservation frees the balance")
	require.Equal(t, priceStd, e.spend("k1"))
	e.requireSettledOnce()
}

func TestFallbackNeverAboveShownPrice(t *testing.T) {
	e := newEnv(t, nImages)
	e.start()
	pricier := dispatch.Target{Provider: "xai", Model: "grok-imagine", ImagePrice: 0.07, ImageBillMult: 1.5}
	cheaper := dispatch.Target{Provider: "gemini", Model: "gemini-image", ImagePrice: 0.02, ImageBillMult: 1.5}
	chat := dispatch.Target{Provider: "openai", Model: "gpt-4o"}
	key := e.key("k1", priceStd, true)
	job := e.submitWait(input("A", key, 1, stepA, pricier, chat, cheaper))
	require.Equal(t, store.ImageJobSucceeded, job.Status)
	require.Equal(t, 1, e.gen.callCount())
	got := e.gen.calls[0].Targets
	require.Len(t, got, 2)
	require.Equal(t, "openai", got[0].Provider)
	require.Equal(t, "gemini", got[1].Provider)
	require.True(t, e.gen.calls[0].Reserved)
}

func TestFallbackBillsWinningStep(t *testing.T) {
	e := newEnv(t, func(_ context.Context, req *core.ImageRequest, opts pipeline.MediaOptions) (*core.ImageResponse, dispatch.Target, error) {
		return &core.ImageResponse{Data: []core.ImageData{b64Item("x")}}, opts.Targets[1], nil
	})
	e.start()
	cheaper := dispatch.Target{Provider: "gemini", Model: "gemini-image", ImagePrice: 0.02, ImageBillMult: 1.5}
	key := e.key("k1", priceStd, true)
	job := e.submitWait(input("A", key, 1, stepA, cheaper))
	require.Equal(t, int64(30_000), job.CostMicro)
	require.Equal(t, int64(20_000), job.UpstreamCostMicro)
	require.Equal(t, int64(30_000), e.spend("k1"))
}

// Two eligible steps of one provider: the step that answered is billed at
// its own price and cost, never at the first same-provider step's.
func TestSameProviderStepsBillExactWinner(t *testing.T) {
	e := newEnv(t, func(_ context.Context, _ *core.ImageRequest, opts pipeline.MediaOptions) (*core.ImageResponse, dispatch.Target, error) {
		return &core.ImageResponse{Data: []core.ImageData{b64Item("x")}}, opts.Targets[2], nil
	})
	e.start()
	xai := dispatch.Target{Provider: "xai", Model: "grok-imagine", ImagePrice: 0.04, ImageBillMult: 1.5}         // shown 60k
	mini := dispatch.Target{Provider: "openai", Model: "gpt-image-1-mini", ImagePrice: 0.01, ImageBillMult: 1.5} // 15k / 10k
	full := dispatch.Target{Provider: "openai", Model: "gpt-image-1", ImagePrice: 0.05, ImageBillMult: 1.16}     // 58k / 50k
	key := e.key("k1", priceStd, true)
	job := e.submitWait(input("A", key, 1, xai, mini, full))
	require.Equal(t, store.ImageJobSucceeded, job.Status, job.Error)
	require.Equal(t, int64(58_000), job.CostMicro)
	require.Equal(t, int64(50_000), job.UpstreamCostMicro)
	require.Equal(t, int64(58_000), e.spend("k1"))
	var model string
	require.NoError(t, e.db.SQL().QueryRow(`SELECT model FROM usage_records WHERE api_key_id = ?`, "k1").Scan(&model))
	require.Equal(t, "gpt-image-1", model)
}

// A winner priced above the shown price (never passed by Submit) is not billed.
func TestWinnerAboveShownPriceFailsClosed(t *testing.T) {
	e := newEnv(t, func(context.Context, *core.ImageRequest, pipeline.MediaOptions) (*core.ImageResponse, dispatch.Target, error) {
		return &core.ImageResponse{Data: []core.ImageData{b64Item("x")}},
			dispatch.Target{Provider: "openai", Model: "gpt-image-1", ImagePrice: 1, ImageBillMult: 1.5}, nil
	})
	e.start()
	key := e.key("k1", priceStd, true)
	job := e.submitWait(input("A", key, 1))
	require.Equal(t, store.ImageJobFailed, job.Status)
	require.Empty(t, job.Files)
	require.Zero(t, e.spend("k1"))
	e.requireSettledOnce()
}

func TestZeroPriceTargetRefused(t *testing.T) {
	e := newEnv(t, nImages)
	key := e.key("k1", priceStd, true)
	unpriced := dispatch.Target{Provider: "openai", Model: "gpt-image-1", ImagePrice: 0.04}
	_, err := e.svc.Submit(context.Background(), input("A", key, 1, unpriced))
	require.ErrorIs(t, err, ErrNoImageTarget)
	below := dispatch.Target{Provider: "openai", Model: "gpt-image-1", ImagePrice: 0.04, ImageBillMult: 0.5}
	_, err = e.svc.Submit(context.Background(), input("A", key, 1, below))
	require.ErrorIs(t, err, ErrNoImageTarget, "a sell price below cost is refused")
	_, err = e.svc.Submit(context.Background(), input("A", key, 1, dispatch.Target{Provider: "openai", Model: "gpt-4o"}))
	require.ErrorIs(t, err, ErrNoImageTarget)
	require.Zero(t, e.gen.callCount())
}

func TestTooManyActive(t *testing.T) {
	e := newEnv(t, nImages) // workers not started: jobs stay queued
	key := e.key("k1", 10*priceStd, true)
	for range 2 {
		_, err := e.svc.Submit(context.Background(), input("A", key, 1))
		require.NoError(t, err)
	}
	_, err := e.svc.Submit(context.Background(), input("A", key, 1))
	require.ErrorIs(t, err, ErrTooManyActive)
	_, err = e.svc.Submit(context.Background(), input("B", key, 1))
	require.NoError(t, err, "the limit is per owner")
}

func TestEmptyPromptRefused(t *testing.T) {
	e := newEnv(t, nImages)
	key := e.key("k1", priceStd, true)
	in := input("A", key, 1)
	in.Prompt = "   "
	_, err := e.svc.Submit(context.Background(), in)
	require.ErrorIs(t, err, ErrEmptyPrompt)
}

func TestNClampedToFour(t *testing.T) {
	e := newEnv(t, nImages)
	e.start()
	key := e.key("k1", 4*priceStd, true)
	job := e.submitWait(input("A", key, 9))
	require.Equal(t, 4, job.ImageCount)
	require.Contains(t, job.Params, `"n":4`)
}

// A key whose balance is below N × shown price is refused before any
// generation, with the real budget engine and store.
func TestBalanceBelowPriceRefused(t *testing.T) {
	e := newEnv(t, nImages)
	e.start()
	key := e.key("k1", 2*priceStd-1, true)
	_, err := e.svc.Submit(context.Background(), input("A", key, 2))
	require.ErrorIs(t, err, ErrInsufficientBalance)
	jobs, err := e.db.ImageJobs().ListByOwner(context.Background(), "A", 10)
	require.NoError(t, err)
	require.Empty(t, jobs)
	require.Zero(t, e.gen.callCount())
}

// A key with no prepaid balance (no budget row, a soft budget, or a
// token-only budget) fails closed instead of generating unbilled.
func TestNoPrepaidBalanceRefused(t *testing.T) {
	e := newEnv(t, nImages)
	e.start()
	ctx := context.Background()
	noBudget := store.APIKey{ID: "k-none", TenantID: store.DefaultTenantID, ProjectID: "p1"}
	_, err := e.svc.Submit(ctx, input("A", noBudget, 1))
	require.ErrorIs(t, err, ErrInsufficientBalance)

	soft := e.key("k-soft", 10*priceStd, false)
	_, err = e.svc.Submit(ctx, input("A", soft, 1))
	require.ErrorIs(t, err, ErrInsufficientBalance)

	now := time.Now()
	require.NoError(t, e.db.Budgets().Create(ctx, store.Budget{
		ID: "b-tok", TenantID: store.DefaultTenantID, ScopeKind: store.ScopeAPIKey, ScopeID: "k-tok",
		LimitTokens: 1_000_000, Period: "total", HardCutoff: true, CreatedAt: now, UpdatedAt: now,
	}))
	_, err = e.svc.Submit(ctx, input("A", store.APIKey{ID: "k-tok", TenantID: store.DefaultTenantID}, 1))
	require.ErrorIs(t, err, ErrInsufficientBalance)
	require.Zero(t, e.gen.callCount())
}

func TestRecoverFailsStale(t *testing.T) {
	e := newEnv(t, nImages) // no workers: the job is orphaned as after a crash
	key := e.key("k1", priceStd, true)
	job, err := e.svc.Submit(context.Background(), input("A", key, 1))
	require.NoError(t, err)
	require.NoError(t, e.svc.Recover(context.Background()))
	got, err := e.db.ImageJobs().Get(context.Background(), "A", job.ID)
	require.NoError(t, err)
	require.Equal(t, store.ImageJobFailed, got.Status)
	require.Equal(t, "server dimulai ulang, saldo tidak dipotong", got.Error)
	require.NotNil(t, got.ExpiresAt)
	require.Zero(t, e.spend("k1"))

	// A worker that later picks the stale job up neither generates nor bills.
	e.start()
	e.waitFor(job.ID)
	require.Zero(t, e.gen.callCount())
	require.Zero(t, e.spend("k1"))
	e.requireSettledOnce()
}

func TestHighResPricing(t *testing.T) {
	targets := []dispatch.Target{{Provider: "openai", Model: "gpt-4o"}, stepA}
	for res, want := range map[string]int64{"": priceStd, "1k": priceStd, " 1K ": priceStd, "2K": price2K, "4k": price2K} {
		got, ok := ShownPriceMicro(targets, res)
		require.True(t, ok)
		require.Equal(t, want, got, "resolution %q", res)
	}
	no2K := []dispatch.Target{{Provider: "openai", Model: "gpt-image-1", ImagePrice: 0.02, ImageBillMult: 1.25}}
	got, ok := ShownPriceMicro(no2K, "2k")
	require.True(t, ok)
	require.Equal(t, int64(25_000), got, "outside grok-imagine, no 2K price means the standard price")
	_, ok = ShownPriceMicro([]dispatch.Target{{Provider: "openai", Model: "gpt-4o"}}, "")
	require.False(t, ok)
	require.False(t, isHighRes(""))
	require.False(t, isHighRes("1K"))
	require.True(t, isHighRes("2k"))
}

func TestPriceHelperRounding(t *testing.T) {
	// 0.0042 × 2 carries float noise above 8400; the bill stays 8400.
	p, ok := priceOf(dispatch.Target{ImagePrice: 0.0042, ImageBillMult: 2}, "")
	require.True(t, ok)
	require.Equal(t, int64(8_400), p.sell)
	require.Equal(t, int64(4_200), p.cost)
	// A real fraction of a micro rounds up, never below cost.
	p, ok = priceOf(dispatch.Target{ImagePrice: 0.0000015, ImageBillMult: 1}, "")
	require.True(t, ok)
	require.Equal(t, int64(2), p.sell)
	require.GreaterOrEqual(t, p.sell, p.cost)
}

func TestHighResJobBilled2K(t *testing.T) {
	e := newEnv(t, nImages)
	e.start()
	key := e.key("k1", price2K, true)
	in := input("A", key, 1)
	in.Resolution = "2K"
	job := e.submitWait(in)
	require.Equal(t, store.ImageJobSucceeded, job.Status, job.Error)
	require.Equal(t, price2K, e.spend("k1"))
	require.Contains(t, job.Params, `"resolution":"2K"`)
}

func TestFilesStoredUnderOwnerDir(t *testing.T) {
	e := newEnv(t, nImages)
	e.start()
	key := e.key("k1", priceStd, true)
	job := e.submitWait(input("A", key, 1))
	require.Len(t, job.Files, 1)
	f := job.Files[0]
	require.Equal(t, filepath.Join(e.dataDir, "images", ownerHash("A")), filepath.Dir(f.Path))
	require.Equal(t, f.ID+".png", filepath.Base(f.Path))
	require.Equal(t, "image/png", f.Mime)
	st, err := os.Stat(f.Path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), st.Mode().Perm())
	require.Equal(t, f.Bytes, st.Size())
	dir, err := os.Stat(filepath.Dir(f.Path))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), dir.Mode().Perm())
}

func TestURLItemsDownloaded(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/img.png":
			_, _ = w.Write(pngBytes("from-url"))
		case "/redirect-http":
			http.Redirect(w, r, "http://example.invalid/img.png", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	e := newEnv(t, func(context.Context, *core.ImageRequest, pipeline.MediaOptions) (*core.ImageResponse, dispatch.Target, error) {
		return &core.ImageResponse{Data: []core.ImageData{
			{URL: srv.URL + "/img.png"},
			{URL: strings.Replace(srv.URL, "https://", "http://", 1) + "/img.png"},
			{URL: srv.URL + "/redirect-http"},
			{URL: srv.URL + "/missing"},
		}}, stepA, nil
	})
	e.svc.http.Transport = srv.Client().Transport
	e.start()
	key := e.key("k1", 4*priceStd, true)
	job := e.submitWait(input("A", key, 4))
	require.Equal(t, 1, job.ImageCount, "only the https download is stored")
	data, err := os.ReadFile(job.Files[0].Path)
	require.NoError(t, err)
	require.Equal(t, pngBytes("from-url"), data)
	require.Equal(t, priceStd, e.spend("k1"))
}

// ---- Concurrency (owner request) ----

func TestConcurrentUsersNoCrossover(t *testing.T) {
	const users, jobsPerUser = 20, 3
	e := newEnv(t, func(ctx context.Context, req *core.ImageRequest, opts pipeline.MediaOptions) (*core.ImageResponse, dispatch.Target, error) {
		time.Sleep(time.Duration(rand.IntN(3000)) * time.Microsecond) // jitter only, never for correctness
		return nImages(ctx, req, opts)
	})
	owners := make([]string, users)
	keys := make([]store.APIKey, users)
	doneBy := map[string]chan string{}
	for u := range users {
		owners[u] = fmt.Sprintf("sub-%02d", u)
		keys[u] = e.key(fmt.Sprintf("key-%02d", u), 100*priceStd, true)
		doneBy[owners[u]] = make(chan string, jobsPerUser)
	}
	e.svc.onDone = func(_, owner string) { doneBy[owner] <- owner }
	e.start()

	var wg sync.WaitGroup
	for u := range users {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobsPerUser {
				in := input(owners[u], keys[u], (u+j)%4+1)
				in.Prompt = fmt.Sprintf("owner=%s job=%d", owners[u], j)
				_, err := e.svc.Submit(context.Background(), in)
				if !assert.NoError(t, err) {
					return
				}
				select {
				case <-doneBy[owners[u]]:
				case <-time.After(60 * time.Second):
					t.Errorf("owner %s job %d never finished", owners[u], j)
					return
				}
			}
		}()
	}
	wg.Wait()
	require.False(t, t.Failed())

	ctx := context.Background()
	for u := range users {
		jobs, err := e.db.ImageJobs().ListByOwner(ctx, owners[u], 50)
		require.NoError(t, err)
		require.Len(t, jobs, jobsPerUser)
		images := 0
		for _, j := range jobs {
			require.Equal(t, owners[u], j.OwnerSub)
			require.Equal(t, keys[u].ID, j.KeyID)
			require.Equal(t, store.ImageJobSucceeded, j.Status, j.Error)
			require.True(t, strings.HasPrefix(j.Prompt, "owner="+owners[u]+" "), j.Prompt)
			require.Len(t, j.Files, j.ImageCount)
			for _, f := range j.Files {
				require.Equal(t, filepath.Join(e.dataDir, "images", ownerHash(owners[u])), filepath.Dir(f.Path))
				data, err := os.ReadFile(f.Path)
				require.NoError(t, err)
				require.True(t, bytes.Contains(data, []byte(j.Prompt+"|")), "file %s holds another job's bytes", f.ID)
				// The file is reachable only by its owner.
				_, err = e.db.ImageJobs().GetFile(ctx, owners[(u+1)%users], f.ID)
				require.ErrorIs(t, err, store.ErrNotFound)
			}
			require.Equal(t, int64(j.ImageCount)*priceStd, j.CostMicro)
			images += j.ImageCount
		}
		require.Equal(t, int64(images)*priceStd, e.spend(keys[u].ID), "spend of %s", keys[u].ID)
	}
	e.requireSettledOnce()
}

func TestActiveLimitAtomic(t *testing.T) {
	e := newEnv(t, nImages) // no workers: accepted jobs stay active
	key := e.key("k1", 100*priceStd, true)
	start := make(chan struct{})
	errs := make(chan error, 10)
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := e.svc.Submit(context.Background(), input("A", key, 1))
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	ok, limited := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrTooManyActive):
			limited++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	require.Equal(t, 2, ok)
	require.Equal(t, 8, limited)
	n, err := e.db.ImageJobs().CountActive(context.Background(), "A")
	require.NoError(t, err)
	require.Equal(t, 2, n)
}

func TestConcurrentSubmitBalanceNeverNegative(t *testing.T) {
	e := newEnv(t, nImages)
	key := e.key("k1", 3*priceStd, true)
	start := make(chan struct{})
	type result struct {
		id  string
		err error
	}
	results := make(chan result, 10)
	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			job, err := e.svc.Submit(context.Background(), input(fmt.Sprintf("owner-%d", i), key, 1))
			results <- result{job.ID, err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	var accepted []string
	for r := range results {
		if r.err != nil {
			require.ErrorIs(t, r.err, ErrInsufficientBalance)
			continue
		}
		accepted = append(accepted, r.id)
	}
	require.Len(t, accepted, 3)
	e.start()
	e.waitFor(accepted...)
	require.Equal(t, 3*priceStd, e.spend("k1"))
	e.requireSettledOnce()
}

func TestConfirmExactlyOnce(t *testing.T) {
	e := newEnv(t, func(ctx context.Context, req *core.ImageRequest, opts pipeline.MediaOptions) (*core.ImageResponse, dispatch.Target, error) {
		switch {
		case strings.HasSuffix(req.Prompt, "panic"):
			panic("connector bug")
		case strings.HasSuffix(req.Prompt, "error"):
			return nil, dispatch.Target{}, errors.New("upstream down")
		case strings.HasSuffix(req.Prompt, "empty"):
			return &core.ImageResponse{}, stepA, nil
		case strings.HasSuffix(req.Prompt, "nil"):
			return nil, stepA, nil
		case strings.HasSuffix(req.Prompt, "stranger"):
			return &core.ImageResponse{Data: []core.ImageData{b64Item("x")}}, dispatch.Target{Provider: "unknown-provider", Model: "x"}, nil
		}
		return nImages(ctx, req, opts)
	})
	e.start()
	key := e.key("k1", 100*priceStd, true)
	want := map[string]string{
		"ok": store.ImageJobSucceeded, "panic": store.ImageJobFailed, "error": store.ImageJobFailed,
		"empty": store.ImageJobFailed, "nil": store.ImageJobFailed, "stranger": store.ImageJobFailed,
	}
	ids := map[string]string{}
	var all []string
	for kind := range want {
		in := input("owner-"+kind, key, 1)
		in.Prompt = "case " + kind
		job, err := e.svc.Submit(context.Background(), in)
		require.NoError(t, err)
		ids[kind] = job.ID
		all = append(all, job.ID)
	}
	e.waitFor(all...)
	for kind, id := range ids {
		job, err := e.db.ImageJobs().Get(context.Background(), "owner-"+kind, id)
		require.NoError(t, err)
		require.Equal(t, want[kind], job.Status, "case %s: %s", kind, job.Error)
		if job.Status == store.ImageJobFailed {
			require.Empty(t, job.Files)
			require.NotEmpty(t, job.Error)
		}
	}
	require.Equal(t, priceStd, e.spend("k1"), "only the ok job is billed")
	e.requireSettledOnce()
}

// ---- Fix round 1 ----

// testClock is a settable clock safe to read from workers.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *testClock) set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

func (e *env) ownerFiles(owner string) []os.DirEntry {
	e.t.Helper()
	entries, err := os.ReadDir(filepath.Join(e.dataDir, "images", ownerHash(owner)))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	require.NoError(e.t, err)
	return entries
}

// A job that waited in the queue past 45 minutes is dropped before any
// upstream call: its reservation may be near the budget's one-hour expiry.
func TestQueuedTooLongDropped(t *testing.T) {
	e := newEnv(t, nImages)
	clock := &testClock{t: time.Now()}
	e.svc.d.Now = clock.now
	key := e.key("k1", priceStd, true)
	job, err := e.svc.Submit(context.Background(), input("A", key, 1))
	require.NoError(t, err)

	clock.set(clock.now().Add(45*time.Minute + time.Second))
	e.start()
	e.waitFor(job.ID)
	got, err := e.db.ImageJobs().Get(context.Background(), "A", job.ID)
	require.NoError(t, err)
	require.Equal(t, store.ImageJobFailed, got.Status)
	require.Equal(t, "antrian terlalu lama, saldo tidak dipotong", got.Error)
	require.Zero(t, e.gen.callCount())
	require.Zero(t, e.spend("k1"))
	e.requireSettledOnce()
}

func TestQueuedJustUnderLimitRuns(t *testing.T) {
	e := newEnv(t, nImages)
	clock := &testClock{t: time.Now()}
	e.svc.d.Now = clock.now
	key := e.key("k1", priceStd, true)
	job, err := e.svc.Submit(context.Background(), input("A", key, 1))
	require.NoError(t, err)
	clock.set(clock.now().Add(44 * time.Minute))
	e.start()
	e.waitFor(job.ID)
	require.Equal(t, priceStd, e.spend("k1"))
}

func TestQueueFullReleasesAndFails(t *testing.T) {
	e := newEnv(t, nImages) // no workers: the queue only fills
	key := e.key("k1", 100*priceStd, true)
	ctx := context.Background()
	for i := range queueSize {
		_, err := e.svc.Submit(ctx, input(fmt.Sprintf("owner-%d", i/2), key, 1))
		require.NoError(t, err)
	}
	_, err := e.svc.Submit(ctx, input("late", key, 1))
	require.ErrorIs(t, err, ErrQueueFull)

	jobs, err := e.db.ImageJobs().ListByOwner(ctx, "late", 10)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	require.Equal(t, store.ImageJobFailed, jobs[0].Status)
	require.Zero(t, e.spend("k1"))
	e.bud.mu.Lock()
	last := e.bud.reserved[len(e.bud.reserved)-1]
	require.Equal(t, 1, e.bud.settled[last], "the refused job's reservation is released")
	require.Len(t, e.bud.settled, 1, "queued jobs keep their reservations")
	e.bud.mu.Unlock()
}

type failingMeter struct{}

func (failingMeter) BuildRecord(meter.Event) (store.UsageRecord, int64, error) {
	return store.UsageRecord{}, 0, errors.New("bad image event")
}

func (failingMeter) Persisted(store.UsageRecord) {}

// panicTransport panics inside the download, after an earlier item was stored.
type panicTransport struct{}

func (panicTransport) RoundTrip(*http.Request) (*http.Response, error) { panic("transport bug") }

func TestPanicMidSaveRemovesWrittenFiles(t *testing.T) {
	e := newEnv(t, func(context.Context, *core.ImageRequest, pipeline.MediaOptions) (*core.ImageResponse, dispatch.Target, error) {
		return &core.ImageResponse{Data: []core.ImageData{b64Item("first"), {URL: "https://images.example.com/2.png"}}}, stepA, nil
	})
	e.svc.http.Transport = panicTransport{}
	e.start()
	key := e.key("k1", 2*priceStd, true)
	job := e.submitWait(input("A", key, 2))
	require.Equal(t, store.ImageJobFailed, job.Status)
	require.Empty(t, e.ownerFiles("A"), "the file stored before the panic is removed")
	require.Zero(t, e.spend("k1"))
	e.requireSettledOnce()
}

// flakyJobs fails FinishSucceededBilled a set number of times.
type flakyJobs struct {
	*store.ImageJobsRepo
	mu       sync.Mutex
	failures int
	calls    int
}

func (f *flakyJobs) FinishSucceededBilled(ctx context.Context, id string, files []store.ImageFile, usage store.UsageRecord, at time.Time) error {
	f.mu.Lock()
	f.calls++
	fail := f.calls <= f.failures
	f.mu.Unlock()
	if fail {
		return errors.New("database is locked")
	}
	return f.ImageJobsRepo.FinishSucceededBilled(ctx, id, files, usage, at)
}

func TestFinishRetriedBeforeBilling(t *testing.T) {
	old := finishBackoff
	finishBackoff = time.Millisecond
	t.Cleanup(func() { finishBackoff = old })

	for _, tc := range []struct {
		failures   int
		wantStatus string
		wantSpend  int64
	}{{failures: 3, wantStatus: store.ImageJobSucceeded, wantSpend: priceStd}, {failures: 10, wantStatus: store.ImageJobFailed}} {
		e := newEnv(t, nImages)
		jobs := &flakyJobs{ImageJobsRepo: e.db.ImageJobs(), failures: tc.failures}
		e.svc.d.Jobs = jobs
		e.start()
		key := e.key("k1", priceStd, true)
		job := e.submitWait(input("A", key, 1))
		require.Equal(t, tc.wantStatus, job.Status)
		jobs.mu.Lock()
		require.Equal(t, 4, jobs.calls, "one try plus at most three retries")
		jobs.mu.Unlock()
		require.Equal(t, tc.wantSpend, e.spend("k1"), "billed exactly when succeeded")
		e.requireSettledOnce()
	}
}

// lostAckJobs commits the first FinishSucceededBilled but reports an
// error, as when the database commits and the ack is lost.
type lostAckJobs struct {
	*store.ImageJobsRepo
	mu    sync.Mutex
	calls int
}

func (f *lostAckJobs) FinishSucceededBilled(ctx context.Context, id string, files []store.ImageFile, usage store.UsageRecord, at time.Time) error {
	err := f.ImageJobsRepo.FinishSucceededBilled(ctx, id, files, usage, at)
	f.mu.Lock()
	f.calls++
	first := f.calls == 1
	f.mu.Unlock()
	if first && err == nil {
		return context.DeadlineExceeded
	}
	return err
}

func TestFinishLostAckBilledOnceKeepsFiles(t *testing.T) {
	old := finishBackoff
	finishBackoff = time.Millisecond
	t.Cleanup(func() { finishBackoff = old })

	e := newEnv(t, nImages)
	jobs := &lostAckJobs{ImageJobsRepo: e.db.ImageJobs()}
	e.svc.d.Jobs = jobs
	e.start()
	key := e.key("k1", priceStd, true)
	job := e.submitWait(input("A", key, 1))
	require.Equal(t, store.ImageJobSucceeded, job.Status)
	require.Len(t, job.Files, 1)
	_, err := os.Stat(job.Files[0].Path)
	require.NoError(t, err, "the committed job's file stays on disk")
	f, err := e.db.ImageJobs().GetFile(context.Background(), "A", job.Files[0].ID)
	require.NoError(t, err, "and is still served")
	require.Equal(t, job.Files[0].Path, f.Path)
	jobs.mu.Lock()
	require.Equal(t, 2, jobs.calls, "the retry saw the job already finished")
	jobs.mu.Unlock()
	require.Equal(t, priceStd, e.spend("k1"), "billed once")
	e.requireSettledOnce()
}

func TestPublicIP(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "::1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "fc00::1", "fd12::1",
		"169.254.169.254", "fe80::1", "0.0.0.0", "::", "::ffff:127.0.0.1", "224.0.0.1",
		"100.64.0.1", "100.127.255.254", "64:ff9b::a00:1"} {
		require.False(t, publicIP(net.ParseIP(s)), s)
	}
	for _, s := range []string{"8.8.8.8", "1.1.1.1", "2606:4700::1111", "100.128.0.1", "100.63.255.255"} {
		require.True(t, publicIP(net.ParseIP(s)), s)
	}
}

// The real download client refuses a loopback server at dial time.
func TestDownloadRefusesLoopback(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(pngBytes("internal"))
	}))
	t.Cleanup(srv.Close)
	e := newEnv(t, nImages)
	_, err := e.svc.download(context.Background(), srv.URL+"/img.png")
	require.Error(t, err)
	require.Contains(t, err.Error(), "internal address")
}

func TestFailureMessageHidesUpstreamText(t *testing.T) {
	secret := "provider wegoo-relay model gpt-image-1 key sk-abc123 refused"
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"moderation kind", &core.ProviderError{Kind: core.ErrContentFilter, Message: secret}, msgModeration},
		{"moderation 400", &core.ProviderError{Kind: core.ErrBadRequest, StatusCode: 400, Message: "Your request was rejected by the safety system: " + secret}, msgModeration},
		{"rate limit kind", &core.ProviderError{Kind: core.ErrRateLimit, Message: secret}, msgBusy},
		{"429", &core.ProviderError{Kind: core.ErrUpstream, StatusCode: 429, Message: secret}, msgBusy},
		{"timeout kind", &core.ProviderError{Kind: core.ErrTimeout, Message: secret}, msgTimeout},
		{"deadline", fmt.Errorf("%s: %w", secret, context.DeadlineExceeded), msgTimeout},
		{"other", &core.ProviderError{Kind: core.ErrAuth, StatusCode: 401, Message: secret}, msgGenericFail},
		{"plain", errors.New(secret), msgGenericFail},
	}
	for _, c := range cases {
		require.Equal(t, c.want, failureMessage(c.err), c.name)
		require.NotContains(t, c.want, "—")
	}

	e := newEnv(t, func(context.Context, *core.ImageRequest, pipeline.MediaOptions) (*core.ImageResponse, dispatch.Target, error) {
		return nil, dispatch.Target{}, &core.ProviderError{Kind: core.ErrUpstream, StatusCode: 502, Provider: "wegoo-relay", Message: secret}
	})
	e.start()
	key := e.key("k1", priceStd, true)
	job := e.submitWait(input("A", key, 1))
	require.Equal(t, store.ImageJobFailed, job.Status)
	require.Equal(t, msgGenericFail, job.Error)
	for _, leak := range []string{"sk-abc", "wegoo", "gpt-image-1"} {
		require.NotContains(t, job.Error, leak)
	}
}
