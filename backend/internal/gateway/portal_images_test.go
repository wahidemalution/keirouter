package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/budget"
	"github.com/mydisha/keirouter/backend/internal/core"
	"github.com/mydisha/keirouter/backend/internal/dispatch"
	"github.com/mydisha/keirouter/backend/internal/imagegen"
	"github.com/mydisha/keirouter/backend/internal/market"
	"github.com/mydisha/keirouter/backend/internal/meter"
	"github.com/mydisha/keirouter/backend/internal/pipeline"
	"github.com/mydisha/keirouter/backend/internal/store"
)

// picsPrice is the sell price of one image from the "pics" chain: $0.04 with
// the default 10% markup.
const picsPrice int64 = 44_000

// grok2K is a grok-imagine 2K cost: $0.05, sold at 55,000 micro-USD.
var grok2K = 0.05

func portalPNG(tag string) []byte {
	return append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), tag...)
}

// portalImageGen answers each request with req.N images tagged with the prompt.
type portalImageGen struct {
	mu   sync.Mutex
	reqs []core.ImageRequest
}

func (g *portalImageGen) GenerateImageTarget(_ context.Context, req *core.ImageRequest, opts pipeline.MediaOptions) (*core.ImageResponse, dispatch.Target, error) {
	g.mu.Lock()
	g.reqs = append(g.reqs, *req)
	g.mu.Unlock()
	time.Sleep(5 * time.Millisecond)
	resp := &core.ImageResponse{}
	for i := range req.N {
		resp.Data = append(resp.Data, core.ImageData{B64JSON: base64.StdEncoding.EncodeToString(portalPNG(fmt.Sprintf("%s|%d", req.Prompt, i)))})
	}
	return resp, opts.Targets[0], nil
}

func (g *portalImageGen) last() core.ImageRequest {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.reqs[len(g.reqs)-1]
}

type portalImageFixture struct {
	t   *testing.T
	srv *Server
	gen *portalImageGen
	mux chi.Router
}

func newPortalImageFixture(t *testing.T) *portalImageFixture {
	t.Helper()
	srv := newPortalTestServer(t)
	srv.marketCache = market.NewSnapshotCache()
	eng := budget.New(srv.db.Budgets(), srv.db.Usage())
	t.Cleanup(eng.Close)
	m := meter.New(srv.db.Usage(), nil, nil)
	m.AddUsageTracker(eng)
	gen := &portalImageGen{}
	srv.imageGen = imagegen.New(imagegen.Deps{
		Jobs: srv.db.ImageJobs(), Generator: gen, Budget: eng, Meter: m,
		DataDir: t.TempDir(), Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { srv.imageGen.Run(ctx); close(stopped) }()
	t.Cleanup(func() { cancel(); <-stopped })

	createImageChain(t, srv.db, "pics", store.ChainStep{Provider: "openai", Model: "gpt-image-1", StaticPerImage: 0.04})
	return &portalImageFixture{t: t, srv: srv, gen: gen, mux: portalImageRouter(srv)}
}

func portalImageRouter(srv *Server) chi.Router {
	mux := chi.NewRouter()
	mux.Group(func(r chi.Router) {
		r.Use(srv.portalSessionMiddleware)
		srv.mountPortalImageRoutes(r)
	})
	return mux
}

// user claims a fresh key with a prepaid balance of balance micro-USD for
// sub and returns its session token and key id.
func (f *portalImageFixture) user(sub string, balance int64) (string, string) {
	f.t.Helper()
	ctx := context.Background()
	issued, err := f.srv.identity.Create(ctx, store.DefaultTenantID, "", "key-"+sub)
	require.NoError(f.t, err)
	keyID := issued.Record.ID
	now := time.Now().UTC()
	require.NoError(f.t, f.srv.db.Budgets().Create(ctx, store.Budget{
		ID: "b-" + sub, TenantID: store.DefaultTenantID, ScopeKind: store.ScopeAPIKey, ScopeID: keyID,
		LimitMicros: balance, Period: "total", AlertPct: 80, HardCutoff: true, CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(f.t, f.srv.db.PortalUsers().Upsert(ctx, store.PortalUser{
		GoogleSub: sub, Email: sub + "@example.com", KeyID: keyID, CreatedAt: now, UpdatedAt: now,
	}))
	tok, err := f.srv.auth.IssuePortalSession("portal:"+sub, sub+"@example.com")
	require.NoError(f.t, err)
	return tok, keyID
}

func (f *portalImageFixture) do(method, path, body, tok string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if tok != "" {
		req.AddCookie(&http.Cookie{Name: portalSessionCookie, Value: tok})
	}
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	return rec
}

type portalJobJSON struct {
	ID         string         `json:"id"`
	Status     string         `json:"status"`
	Error      string         `json:"error"`
	Prompt     string         `json:"prompt"`
	Params     map[string]any `json:"params"`
	ImageCount int            `json:"image_count"`
	CostMicro  int64          `json:"cost_micro"`
	Files      []struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	} `json:"files"`
}

func (f *portalImageFixture) submit(tok, body string) portalJobJSON {
	f.t.Helper()
	rec := f.do(http.MethodPost, "/portal/api/images", body, tok)
	require.Equal(f.t, http.StatusCreated, rec.Code, rec.Body.String())
	var out struct {
		Job portalJobJSON `json:"job"`
	}
	require.NoError(f.t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.NotEmpty(f.t, out.Job.ID)
	return out.Job
}

func (f *portalImageFixture) list(tok string) []portalJobJSON {
	f.t.Helper()
	rec := f.do(http.MethodGet, "/portal/api/images?limit=50", "", tok)
	require.Equal(f.t, http.StatusOK, rec.Code, rec.Body.String())
	var out struct {
		Jobs []portalJobJSON `json:"jobs"`
	}
	require.NoError(f.t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out.Jobs
}

// waitDone polls the list until every job of tok is finished.
func (f *portalImageFixture) waitDone(tok string, want int) []portalJobJSON {
	f.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		jobs := f.list(tok)
		done := 0
		for _, j := range jobs {
			if j.Status == store.ImageJobSucceeded || j.Status == store.ImageJobFailed {
				done++
			}
		}
		if len(jobs) == want && done == want {
			return jobs
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("jobs never finished: %+v", jobs)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (f *portalImageFixture) spend(keyID string) int64 {
	f.t.Helper()
	spent, _, err := f.srv.db.Usage().SpendAndTokens(context.Background(), store.ScopeAPIKey, keyID, budget.PeriodStart("total", time.Now()))
	require.NoError(f.t, err)
	return spent
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var out struct {
		Error string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
	return out.Error
}

func TestPortalImageFlow(t *testing.T) {
	f := newPortalImageFixture(t)
	tok, keyID := f.user("sub-a", 10*picsPrice)

	job := f.submit(tok, `{"chain_id":"pics","expected_price_micro":44000,"prompt":"  a red cat  ","n":1,"size":"1024x1024"}`)
	require.Equal(t, store.ImageJobQueued, job.Status)
	require.Equal(t, "a red cat", job.Prompt)

	jobs := f.waitDone(tok, 1)
	got := jobs[0]
	require.Equal(t, job.ID, got.ID)
	require.Equal(t, store.ImageJobSucceeded, got.Status, got.Error)
	require.Equal(t, 1, got.ImageCount)
	require.Equal(t, picsPrice, got.CostMicro)
	require.Len(t, got.Files, 1)
	require.Equal(t, "/portal/api/images/files/"+got.Files[0].ID, got.Files[0].URL)
	require.Equal(t, picsPrice, f.spend(keyID))

	rec := f.do(http.MethodGet, got.Files[0].URL, "", tok)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, portalPNG("a red cat|0"), rec.Body.Bytes())
	require.Equal(t, "image/png", rec.Header().Get("Content-Type"))
	require.Equal(t, "private, no-store", rec.Header().Get("Cache-Control"))
	require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	require.Equal(t, `inline; filename="`+got.Files[0].ID+`.png"`, rec.Header().Get("Content-Disposition"))

	rec = f.do(http.MethodGet, got.Files[0].URL+"?download=1", "", tok)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, `attachment; filename="`+got.Files[0].ID+`.png"`, rec.Header().Get("Content-Disposition"))

	// The job JSON carries no internal fields.
	rec = f.do(http.MethodGet, "/portal/api/images", "", tok)
	body := rec.Body.String()
	for _, leak := range []string{"owner_sub", "key_id", "tenant_id", "upstream", "path", keyID, "sub-a", "gpt-image-1"} {
		require.NotContains(t, body, leak)
	}
}

func TestPortalImageIsolation(t *testing.T) {
	f := newPortalImageFixture(t)
	tokA, _ := f.user("sub-a", 10*picsPrice)
	tokB, _ := f.user("sub-b", 10*picsPrice)

	f.submit(tokA, `{"chain_id":"pics","expected_price_micro":44000,"prompt":"secret of A","n":1}`)
	jobA := f.waitDone(tokA, 1)[0]
	fileURL := jobA.Files[0].URL

	require.Empty(t, f.list(tokB), "B sees none of A's jobs")
	require.Equal(t, http.StatusNotFound, f.do(http.MethodGet, fileURL, "", tokB).Code)
	require.Equal(t, http.StatusNotFound, f.do(http.MethodGet, fileURL+"?download=1", "", tokB).Code)
	require.Equal(t, http.StatusNotFound, f.do(http.MethodDelete, "/portal/api/images/"+jobA.ID, "", tokB).Code)
	require.Equal(t, http.StatusUnauthorized, f.do(http.MethodGet, fileURL, "", "").Code)

	// A's job and file survive B's attempts.
	require.Equal(t, http.StatusOK, f.do(http.MethodGet, fileURL, "", tokA).Code)
	stored, err := f.srv.db.ImageJobs().GetFile(context.Background(), "sub-a", jobA.Files[0].ID)
	require.NoError(t, err)

	require.Equal(t, http.StatusNoContent, f.do(http.MethodDelete, "/portal/api/images/"+jobA.ID, "", tokA).Code)
	require.Equal(t, http.StatusNotFound, f.do(http.MethodGet, fileURL, "", tokA).Code)
	_, err = os.Stat(stored.Path)
	require.ErrorIs(t, err, os.ErrNotExist, "the file is removed from disk")
	require.Equal(t, http.StatusNotFound, f.do(http.MethodDelete, "/portal/api/images/"+jobA.ID, "", tokA).Code)
}

func TestPortalImageModelsListsImageChainsOnly(t *testing.T) {
	f := newPortalImageFixture(t)
	two := 0.08
	createImageChain(t, f.srv.db, "pics-hd", store.ChainStep{Provider: "openai", Model: "gpt-image-1", StaticPerImage: 0.04, StaticPerImage2K: &two})
	createImageChain(t, f.srv.db, "grok-pics", store.ChainStep{Provider: "xai", Model: "grok-imagine-image", StaticPerImage: 0.02})
	createImageChain(t, f.srv.db, "grok-hd", store.ChainStep{Provider: "xai", Model: "grok-imagine-image", StaticPerImage: 0.02, StaticPerImage2K: &two})
	createImageChain(t, f.srv.db, "chatty", store.ChainStep{Provider: "openai", Model: "gpt-4o", StaticInputPerM: 1, StaticOutputPerM: 2})
	tok, _ := f.user("sub-a", picsPrice)

	rec := f.do(http.MethodGet, "/portal/api/images/models", "", tok)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out struct {
		Models []map[string]any `json:"models"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	byName := map[string]map[string]any{}
	for _, m := range out.Models {
		byName[m["name"].(string)] = m
	}
	require.NotContains(t, byName, "chatty")
	require.Len(t, byName, 4)

	require.Equal(t, "pics", byName["pics"]["chain_id"])
	require.Equal(t, "gpt-image", byName["pics"]["family"])
	require.EqualValues(t, picsPrice, byName["pics"]["price_micro"])
	require.NotContains(t, byName["pics"], "price_2k_micro", "no 2K price when it equals the standard one")

	require.EqualValues(t, picsPrice, byName["pics-hd"]["price_micro"])
	require.NotContains(t, byName["pics-hd"], "price_2k_micro", "only grok-imagine offers a 2K resolution")

	require.EqualValues(t, 22_000, byName["grok-hd"]["price_micro"])
	require.EqualValues(t, 88_000, byName["grok-hd"]["price_2k_micro"])

	require.Equal(t, "grok-imagine", byName["grok-pics"]["family"])
	require.EqualValues(t, 22_000, byName["grok-pics"]["price_micro"])
	require.NotContains(t, byName["grok-pics"], "price_2k_micro", "a grok step without a 2K cost cannot make 2K images")
	require.NotContains(t, rec.Body.String(), "openai")
}

func TestPortalImageModelsHonourAllowedModels(t *testing.T) {
	f := newPortalImageFixture(t)
	createImageChain(t, f.srv.db, "other-pics", store.ChainStep{Provider: "openai", Model: "gpt-image-1", StaticPerImage: 0.04})
	tok, keyID := f.user("sub-a", 10*picsPrice)
	require.NoError(t, f.srv.identity.Keys().SetAllowedModels(context.Background(), keyID, []string{"chain:pics"}))

	rec := f.do(http.MethodGet, "/portal/api/images/models", "", tok)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"pics"`)
	require.NotContains(t, rec.Body.String(), "other-pics")

	rec = f.do(http.MethodPost, "/portal/api/images", `{"chain_id":"other-pics","prompt":"x","n":1}`, tok)
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Equal(t, "model_not_available", errorCode(t, rec))
}

// requirePicsUnavailable asserts the "pics" chain is neither listed nor
// submittable for tok.
func (f *portalImageFixture) requirePicsUnavailable(tok string) {
	f.t.Helper()
	rec := f.do(http.MethodGet, "/portal/api/images/models", "", tok)
	require.Equal(f.t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotContains(f.t, rec.Body.String(), `"pics"`)
	rec = f.do(http.MethodPost, "/portal/api/images", `{"chain_id":"pics","expected_price_micro":44000,"prompt":"x","n":1}`, tok)
	require.Equal(f.t, http.StatusNotFound, rec.Code, rec.Body.String())
	require.Equal(f.t, "model_not_available", errorCode(f.t, rec))
	require.Empty(f.t, f.list(tok))
}

func TestPortalImageAdminAllowedProviders(t *testing.T) {
	f := newPortalImageFixture(t)
	tok, keyID := f.user("sub-a", 10*picsPrice)
	require.NoError(t, f.srv.identity.Keys().SetAllowedProviders(context.Background(), keyID, []string{"anthropic"}))
	f.requirePicsUnavailable(tok)
}

func TestPortalImageUserDisabledProvider(t *testing.T) {
	f := newPortalImageFixture(t)
	tok, keyID := f.user("sub-a", 10*picsPrice)
	require.NoError(t, f.srv.identity.Keys().SetModelProviderEnabled(context.Background(), keyID, "pics", "openai", false))
	f.requirePicsUnavailable(tok)
}

// Only the steps the key may use are routed: a disabled first step is skipped
// and the job runs (and is billed) on the next one.
func TestPortalImageDisabledStepSkipped(t *testing.T) {
	f := newPortalImageFixture(t)
	createImageChain(t, f.srv.db, "duo",
		store.ChainStep{Provider: "openai", Model: "gpt-image-1", StaticPerImage: 0.04},
		store.ChainStep{Provider: "azure", Model: "gpt-image-1", StaticPerImage: 0.02})
	tok, keyID := f.user("sub-a", 10*picsPrice)
	require.NoError(t, f.srv.identity.Keys().SetModelProviderEnabled(context.Background(), keyID, "duo", "openai", false))

	rec := f.do(http.MethodGet, "/portal/api/images/models", "", tok)
	require.Contains(t, rec.Body.String(), `"chain_id":"duo","name":"duo","family":"gpt-image","price_micro":22000`)
	f.submit(tok, `{"chain_id":"duo","expected_price_micro":22000,"prompt":"x","n":1}`)
	job := f.waitDone(tok, 1)[0]
	require.Equal(t, store.ImageJobSucceeded, job.Status, job.Error)
	require.Equal(t, int64(22_000), f.spend(keyID))
}

func TestPortalImageDisabledKey(t *testing.T) {
	f := newPortalImageFixture(t)
	tok, keyID := f.user("sub-a", 10*picsPrice)
	require.NoError(t, f.srv.identity.Keys().SetDisabled(context.Background(), keyID, true))
	for _, rq := range []struct{ method, path, body string }{
		{http.MethodGet, "/portal/api/images/models", ""},
		{http.MethodPost, "/portal/api/images", `{"chain_id":"pics","expected_price_micro":44000,"prompt":"x","n":1}`},
	} {
		rec := f.do(rq.method, rq.path, rq.body, tok)
		require.Equal(t, http.StatusForbidden, rec.Code, rq.path)
		require.Equal(t, "key_disabled", errorCode(t, rec))
	}
	require.Empty(t, f.list(tok))
}

func TestPortalImageInsufficientBalance(t *testing.T) {
	f := newPortalImageFixture(t)
	tok, keyID := f.user("sub-a", picsPrice)
	rec := f.do(http.MethodPost, "/portal/api/images", `{"chain_id":"pics","expected_price_micro":44000,"prompt":"two cats","n":2}`, tok)
	require.Equal(t, http.StatusPaymentRequired, rec.Code, rec.Body.String())
	require.Equal(t, "insufficient_balance", errorCode(t, rec))
	require.Empty(t, f.list(tok))
	require.Zero(t, f.spend(keyID))
}

func TestPortalImageValidation(t *testing.T) {
	f := newPortalImageFixture(t)
	createImageChain(t, f.srv.db, "grok-pics", store.ChainStep{Provider: "xai", Model: "grok-imagine-image", StaticPerImage: 0.02})
	createImageChain(t, f.srv.db, "gem-pics", store.ChainStep{Provider: "google", Model: "gemini-2.5-flash-image", StaticPerImage: 0.02})
	tok, _ := f.user("sub-a", 100*picsPrice)
	cases := []struct {
		name, body string
		status     int
		code       string
	}{
		{"empty prompt", `{"chain_id":"pics","expected_price_micro":44000,"prompt":"   ","n":1}`, http.StatusBadRequest, "empty_prompt"},
		{"long prompt", `{"chain_id":"pics","expected_price_micro":44000,"prompt":"` + strings.Repeat("a", 4001) + `","n":1}`, http.StatusBadRequest, "prompt_too_long"},
		{"n zero", `{"chain_id":"pics","expected_price_micro":44000,"prompt":"x","n":0}`, http.StatusBadRequest, "invalid_n"},
		{"n five", `{"chain_id":"pics","expected_price_micro":44000,"prompt":"x","n":5}`, http.StatusBadRequest, "invalid_n"},
		{"no chain", `{"prompt":"x","n":1}`, http.StatusBadRequest, "chain_required"},
		{"unknown chain", `{"chain_id":"nope","prompt":"x","n":1}`, http.StatusNotFound, "model_not_available"},
		{"bad size", `{"chain_id":"pics","expected_price_micro":44000,"prompt":"x","n":1,"size":"4096x4096"}`, http.StatusBadRequest, "invalid_size"},
		{"bad resolution", `{"chain_id":"grok-pics","prompt":"x","n":1,"resolution":"4k"}`, http.StatusBadRequest, "invalid_resolution"},
		{"bad grok aspect", `{"chain_id":"grok-pics","prompt":"x","n":1,"aspect_ratio":"5:4"}`, http.StatusBadRequest, "invalid_aspect_ratio"},
		{"gemini lacks 21:9", `{"chain_id":"gem-pics","prompt":"x","n":1,"aspect_ratio":"21:9"}`, http.StatusBadRequest, "invalid_aspect_ratio"},
		{"bad json", `{"chain_id":`, http.StatusBadRequest, "invalid_request"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := f.do(http.MethodPost, "/portal/api/images", c.body, tok)
			require.Equal(t, c.status, rec.Code, rec.Body.String())
			require.Equal(t, c.code, errorCode(t, rec))
		})
	}

	big := `{"chain_id":"pics","n":1,"prompt":"x","pad":"` + strings.Repeat("a", 70<<10) + `"}`
	rec := f.do(http.MethodPost, "/portal/api/images", big, tok)
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code, rec.Body.String())
	require.Empty(t, f.list(tok))
}

func TestPortalImageFamilyGating(t *testing.T) {
	f := newPortalImageFixture(t)
	createImageChain(t, f.srv.db, "grok-pics", store.ChainStep{Provider: "xai", Model: "grok-imagine-image", StaticPerImage: 0.02, StaticPerImage2K: &grok2K})
	tok, _ := f.user("sub-a", 100*picsPrice)

	f.submit(tok, `{"chain_id":"pics","expected_price_micro":44000,"prompt":"gpt","n":1,"size":"1024x1536","aspect_ratio":"16:9","resolution":"2k"}`)
	f.waitDone(tok, 1)
	req := f.gen.last()
	require.Equal(t, "1024x1536", req.Size)
	require.Empty(t, req.AspectRatio)
	require.Empty(t, req.Resolution)

	f.submit(tok, `{"chain_id":"grok-pics","expected_price_micro":55000,"prompt":"grok","n":1,"size":"1024x1536","aspect_ratio":"16:9","resolution":"2K"}`)
	f.waitDone(tok, 2)
	req = f.gen.last()
	require.Empty(t, req.Size)
	require.Equal(t, "16:9", req.AspectRatio)
	require.Equal(t, "2k", req.Resolution)
}

func TestPortalImageTooManyActive(t *testing.T) {
	f := newPortalImageFixture(t)
	tok, _ := f.user("sub-a", 100*picsPrice)
	// Two queued jobs held by a service with no workers fill the owner's slots.
	eng := budget.New(f.srv.db.Budgets(), f.srv.db.Usage())
	t.Cleanup(eng.Close)
	f.srv.imageGen = imagegen.New(imagegen.Deps{
		Jobs: f.srv.db.ImageJobs(), Generator: f.gen, Budget: eng,
		Meter: meter.New(f.srv.db.Usage(), nil, nil), DataDir: t.TempDir(),
	})
	f.submit(tok, `{"chain_id":"pics","expected_price_micro":44000,"prompt":"one","n":1}`)
	f.submit(tok, `{"chain_id":"pics","expected_price_micro":44000,"prompt":"two","n":1}`)
	rec := f.do(http.MethodPost, "/portal/api/images", `{"chain_id":"pics","expected_price_micro":44000,"prompt":"three","n":1}`, tok)
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Equal(t, "too_many_active", errorCode(t, rec))

	jobs := f.list(tok)
	require.Len(t, jobs, 2)
	rec = f.do(http.MethodDelete, "/portal/api/images/"+jobs[0].ID, "", tok)
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Equal(t, "job_active", errorCode(t, rec))
}

func TestPortalImageUnavailable(t *testing.T) {
	f := newPortalImageFixture(t)
	tok, _ := f.user("sub-a", picsPrice)
	f.srv.imageGen = nil
	for _, rq := range []struct{ method, path string }{
		{http.MethodGet, "/portal/api/images/models"},
		{http.MethodGet, "/portal/api/images"},
		{http.MethodPost, "/portal/api/images"},
		{http.MethodGet, "/portal/api/images/files/x"},
		{http.MethodDelete, "/portal/api/images/x"},
	} {
		rec := f.do(rq.method, rq.path, `{}`, tok)
		require.Equal(t, http.StatusServiceUnavailable, rec.Code, rq.path)
		require.Equal(t, "image_generation_unavailable", errorCode(t, rec))
	}
}

// Two users submit two jobs each at the same time. Each sees only their own
// jobs and bytes, and each key pays exactly for its own images.
func TestPortalImageConcurrentUsers(t *testing.T) {
	f := newPortalImageFixture(t)
	type account struct{ sub, tok, key string }
	users := []*account{{sub: "sub-a"}, {sub: "sub-b"}}
	for _, u := range users {
		u.tok, u.key = f.user(u.sub, 100*picsPrice)
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan string, 8)
	for _, u := range users {
		for i := range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				body := fmt.Sprintf(`{"chain_id":"pics","expected_price_micro":44000,"prompt":"owner %s job %d","n":%d}`, u.sub, i, i+1)
				rec := f.do(http.MethodPost, "/portal/api/images", body, u.tok)
				if rec.Code != http.StatusCreated {
					errs <- rec.Body.String()
				}
			}()
		}
	}
	close(start)
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatalf("submit failed: %s", e)
	}

	for _, u := range users {
		jobs := f.waitDone(u.tok, 2)
		images := 0
		for _, j := range jobs {
			require.Equal(t, store.ImageJobSucceeded, j.Status, j.Error)
			require.Contains(t, j.Prompt, "owner "+u.sub+" ")
			images += j.ImageCount
			for _, file := range j.Files {
				rec := f.do(http.MethodGet, file.URL, "", u.tok)
				require.Equal(t, http.StatusOK, rec.Code)
				require.True(t, bytes.Contains(rec.Body.Bytes(), []byte("owner "+u.sub+" ")), "bytes belong to %s", u.sub)
				for _, other := range users {
					if other != u {
						require.Equal(t, http.StatusNotFound, f.do(http.MethodGet, file.URL, "", other.tok).Code)
						require.Equal(t, http.StatusNotFound, f.do(http.MethodDelete, "/portal/api/images/"+j.ID, "", other.tok).Code)
					}
				}
			}
		}
		require.Equal(t, 3, images)
		require.Equal(t, 3*picsPrice, f.spend(u.key), "key of %s pays only its own images", u.sub)
	}
}
