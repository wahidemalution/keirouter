// Package imagegen runs portal image jobs. Submit reserves the job's maximum
// price against the key's prepaid balance and queues it; a worker generates
// the images, stores them on disk, and bills only the images it stored.
package imagegen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/mydisha/keirouter/backend/internal/budget"
	"github.com/mydisha/keirouter/backend/internal/core"
	"github.com/mydisha/keirouter/backend/internal/dispatch"
	"github.com/mydisha/keirouter/backend/internal/limits"
	"github.com/mydisha/keirouter/backend/internal/meter"
	"github.com/mydisha/keirouter/backend/internal/pipeline"
	"github.com/mydisha/keirouter/backend/internal/store"
)

// Errors shown to the user.
var (
	ErrTooManyActive       = errors.New("imagegen: too many active image jobs")
	ErrInsufficientBalance = errors.New("imagegen: insufficient balance")
	ErrNoImageTarget       = errors.New("imagegen: no priced image target")
	ErrEmptyPrompt         = errors.New("imagegen: prompt is empty")
	ErrQueueFull           = errors.New("imagegen: job queue is full")
	ErrPriceChanged        = errors.New("imagegen: price changed")
)

const (
	maxActive     = 2
	maxImages     = 4
	workerCount   = 4
	queueSize     = 32
	jobTimeout    = 5 * time.Minute
	settleTimeout = 30 * time.Second
	// failTimeout bounds marking a job failed, which also runs during shutdown.
	failTimeout  = 5 * time.Second
	ownerStripes = 64
	staleMessage = "server dimulai ulang, saldo tidak dipotong"
	// maxQueueWait stays well inside the budget's one-hour reservation life.
	maxQueueWait     = 45 * time.Minute
	queueWaitMessage = "antrian terlalu lama, saldo tidak dipotong"
	finishRetries    = 3
)

// finishBackoff is the base delay between Finish retries.
var finishBackoff = 200 * time.Millisecond

// Generator generates images with fallback across MediaOptions.Targets and
// reports the exact target that answered.
type Generator interface {
	GenerateImageTarget(ctx context.Context, req *core.ImageRequest, opts pipeline.MediaOptions) (*core.ImageResponse, dispatch.Target, error)
}

// Jobs is the slice of store.ImageJobsRepo the service uses.
type Jobs interface {
	Create(ctx context.Context, j store.ImageJob) error
	Get(ctx context.Context, ownerSub, id string) (store.ImageJob, error)
	CountActive(ctx context.Context, ownerSub string) (int, error)
	SetRunning(ctx context.Context, id string) error
	Finish(ctx context.Context, id, status, errMsg string, count int, cost, upstream int64, files []store.ImageFile, finishedAt time.Time) error
	FinishSucceededBilled(ctx context.Context, id string, files []store.ImageFile, usage store.UsageRecord, finishedAt time.Time) error
	FailStale(ctx context.Context, msg string) (int, error)
	ListExpired(ctx context.Context, now time.Time) ([]store.ImageJob, error)
	Delete(ctx context.Context, ownerSub, id string) ([]store.ImageFile, error)
}

// Budget is the slice of budget.Engine the service uses.
type Budget interface {
	Check(ctx context.Context, scope budget.Scope) (budget.Decision, error)
	Reserve(ctx context.Context, scope budget.Scope, est budget.Estimate) (budget.Reservation, error)
	Confirm(scope budget.Scope, actualMicros int64)
	Release(scope budget.Scope)
}

// Meter builds an image job's usage row; the row is inserted in the same
// transaction that marks the job succeeded, then Persisted tells the
// budget trackers and observers.
type Meter interface {
	BuildRecord(ev meter.Event) (store.UsageRecord, int64, error)
	Persisted(rec store.UsageRecord)
}

// Deps wires the service.
type Deps struct {
	Jobs      Jobs
	Generator Generator
	Budget    Budget
	Meter     Meter
	// DataDir holds generated files under images/.
	DataDir string
	Log     *slog.Logger
	// Now supplies the clock; nil means time.Now.
	Now func() time.Time
}

// SubmitInput is one portal image request.
type SubmitInput struct {
	OwnerSub string
	Key      store.APIKey
	ChainID  string
	// Targets is the chain's resolved fallback order; only priced image steps
	// at or below the shown price are used.
	Targets                       []dispatch.Target
	Limits                        limits.EffectiveLimits
	Prompt                        string
	N                             int
	Size, AspectRatio, Resolution string
	// PriceOrder is the image steps in the chain's declared order; the
	// shown price is the first priced one. Empty means Targets.
	PriceOrder []dispatch.Target
	// ExpectedPriceMicro is the per-image price the client showed; when
	// set, a different shown price refuses the job with ErrPriceChanged.
	ExpectedPriceMicro int64
}

// Service queues and runs portal image jobs.
type Service struct {
	d      Deps
	log    *slog.Logger
	queue  chan job
	owners [ownerStripes]sync.Mutex
	http   *http.Client
	// onDone, when set, is called after each job is handled (tests).
	onDone func(id, owner string)
}

// job is everything a worker needs, copied by value at submit time so no
// state is shared between jobs.
type job struct {
	id, owner, chainID string
	scope              budget.Scope
	targets            []dispatch.Target
	limits             limits.EffectiveLimits
	req                core.ImageRequest
	resolution         string
	// shown is the per-image price the reservation was sized on.
	shown      int64
	enqueuedAt time.Time
}

// New builds the service. Call Recover once, then run Run and RunSweeper.
func New(d Deps) *Service {
	log := d.Log
	if log == nil {
		log = slog.Default()
	}
	return &Service{d: d, log: log, queue: make(chan job, queueSize), http: newDownloadClient()}
}

func (s *Service) now() time.Time {
	if s.d.Now != nil {
		return s.d.Now()
	}
	return time.Now()
}

// stepPrice is one target's per-image price in micro-USD.
type stepPrice struct{ sell, cost int64 }

// priceOf is the single pricing helper: the shown price, the reservation and
// the meter event all use it, so shown price × count equals the bill. ok is
// false for a step that is not a priced image step, has a non-finite or
// out-of-range price, or would sell below cost. A grok-imagine step without
// a 2K cost cannot price a 2K image: its standard price may sell below the
// 2K cost.
func priceOf(t dispatch.Target, resolution string) (stepPrice, bool) {
	usd := t.ImagePrice
	if !finite(usd) || usd <= 0 || !finite(t.ImagePrice2K) || !finite(t.ImageBillMult) {
		return stepPrice{}, false
	}
	if isHighRes(resolution) {
		switch {
		case t.ImagePrice2K > 0:
			usd = t.ImagePrice2K
		case Family(t.Model) == "grok-imagine":
			return stepPrice{}, false
		}
	}
	// The 1e-6 micro slack absorbs float noise (0.0042 × 2 × 1e6 is
	// 8400.000000000001) so it does not ceil to an extra micro.
	sellF := math.Ceil(usd*t.ImageBillMult*1e6 - 1e-6)
	costF := math.Round(usd * 1e6)
	if !(sellF > 0 && sellF <= float64(maxImageMicro)) || costF > float64(maxImageMicro) {
		return stepPrice{}, false
	}
	sell, cost := int64(sellF), int64(costF)
	if sell < cost {
		return stepPrice{}, false
	}
	return stepPrice{sell: sell, cost: cost}, true
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// maxImageMicro caps one image's sell price at $10,000, far above the $100
// admin cap even after markup.
const maxImageMicro int64 = 10_000 * 1_000_000

// mulMicro is n × p micro-USD, refusing a negative input, an overflow or a
// per-image price above maxImageMicro.
func mulMicro(n, p int64) (int64, bool) {
	if n < 0 || p < 0 || p > maxImageMicro || (p > 0 && n > math.MaxInt64/p) {
		return 0, false
	}
	return n * p, true
}

// Family names the image model family of model, matched on the id after
// its last "/"; "" when it is not a known image model.
func Family(model string) string {
	id := strings.ToLower(model)
	if i := strings.LastIndex(id, "/"); i >= 0 {
		id = id[i+1:]
	}
	switch {
	case strings.HasPrefix(id, "gpt-image"):
		return "gpt-image"
	case strings.HasPrefix(id, "grok-imagine"):
		return "grok-imagine"
	case strings.HasPrefix(id, "gemini-") && strings.Contains(id, "image"):
		return "gemini-image"
	}
	return ""
}

// isHighRes reports whether resolution asks for more than the standard 1K output.
func isHighRes(resolution string) bool {
	r := strings.ToLower(strings.TrimSpace(resolution))
	return r != "" && r != "1k"
}

// ShownPriceMicro is the per-image sell price shown to the user: the price
// of the first priced image step at that resolution.
func ShownPriceMicro(targets []dispatch.Target, resolution string) (int64, bool) {
	for _, t := range targets {
		if p, ok := priceOf(t, resolution); ok {
			return p.sell, true
		}
	}
	return 0, false
}

// eligibleTargets keeps, in order, the priced image steps whose sell price
// does not exceed the shown price, so a fallback never costs more than shown.
func eligibleTargets(targets []dispatch.Target, resolution string, shown int64) []dispatch.Target {
	var out []dispatch.Target
	for _, t := range targets {
		if p, ok := priceOf(t, resolution); ok && p.sell <= shown {
			out = append(out, t)
		}
	}
	return out
}

func (s *Service) ownerLock(owner string) *sync.Mutex {
	h := fnv.New32a()
	_, _ = h.Write([]byte(owner))
	return &s.owners[h.Sum32()%ownerStripes]
}

type jobParams struct {
	ChainID     string `json:"chain_id"`
	Model       string `json:"model"`
	Size        string `json:"size"`
	AspectRatio string `json:"aspect_ratio"`
	Resolution  string `json:"resolution"`
	N           int    `json:"n"`
}

// Submit reserves N × the shown price and queues the job. The owner's active
// count, the reservation and the insert run under one per-owner lock, so
// concurrent submits cannot pass the active limit.
func (s *Service) Submit(ctx context.Context, in SubmitInput) (store.ImageJob, error) {
	prompt := strings.TrimSpace(in.Prompt)
	if prompt == "" {
		return store.ImageJob{}, ErrEmptyPrompt
	}
	n := min(max(in.N, 1), maxImages)

	mu := s.ownerLock(in.OwnerSub)
	mu.Lock()
	defer mu.Unlock()

	active, err := s.d.Jobs.CountActive(ctx, in.OwnerSub)
	if err != nil {
		return store.ImageJob{}, err
	}
	if active >= maxActive {
		return store.ImageJob{}, ErrTooManyActive
	}
	order := in.PriceOrder
	if len(order) == 0 {
		order = in.Targets
	}
	shown, ok := ShownPriceMicro(order, in.Resolution)
	if !ok {
		return store.ImageJob{}, ErrNoImageTarget
	}
	if in.ExpectedPriceMicro != 0 && in.ExpectedPriceMicro != shown {
		return store.ImageJob{}, ErrPriceChanged
	}
	targets := eligibleTargets(in.Targets, in.Resolution, shown)
	if len(targets) == 0 {
		return store.ImageJob{}, ErrNoImageTarget
	}
	maxCost, ok := mulMicro(int64(n), shown)
	if !ok {
		return store.ImageJob{}, ErrNoImageTarget
	}

	scope := budget.Scope{TenantID: in.Key.TenantID, ProjectID: in.Key.ProjectID, APIKeyID: in.Key.ID}
	if err := s.requirePrepaid(ctx, scope); err != nil {
		return store.ImageJob{}, err
	}
	res, err := s.d.Budget.Reserve(ctx, scope, budget.Estimate{Micros: maxCost})
	if err != nil {
		if pe := core.AsProviderError(err); pe != nil && pe.Kind == core.ErrBudgetBlocked {
			return store.ImageJob{}, fmt.Errorf("%w: %s", ErrInsufficientBalance, pe.Message)
		}
		return store.ImageJob{}, err
	}
	scope.Reservation = res.ID

	params, _ := json.Marshal(jobParams{
		ChainID: in.ChainID, Model: targets[0].Model, Size: in.Size,
		AspectRatio: in.AspectRatio, Resolution: in.Resolution, N: n,
	})
	rec := store.ImageJob{
		ID: uuid.NewString(), OwnerSub: in.OwnerSub, KeyID: in.Key.ID, TenantID: in.Key.TenantID,
		ProjectID: in.Key.ProjectID, ChainID: in.ChainID, Prompt: prompt, Params: string(params),
		Status: store.ImageJobQueued, CreatedAt: s.now(),
	}
	if err := s.d.Jobs.Create(ctx, rec); err != nil {
		s.d.Budget.Release(scope)
		return store.ImageJob{}, err
	}
	j := job{
		id: rec.ID, owner: in.OwnerSub, chainID: in.ChainID, scope: scope,
		targets: targets, limits: in.Limits, resolution: in.Resolution,
		shown: shown, enqueuedAt: s.now(),
		req: core.ImageRequest{
			Model: targets[0].Model, Prompt: prompt, N: n, Size: in.Size,
			AspectRatio: in.AspectRatio, Resolution: in.Resolution, ResponseFormat: "b64_json",
		},
	}
	select {
	case s.queue <- j:
	default:
		s.d.Budget.Release(scope)
		s.fail(rec.ID, "Antrean gambar sedang penuh, coba lagi sebentar lagi. Saldo tidak dipotong.")
		return store.ImageJob{}, ErrQueueFull
	}
	return rec, nil
}

// requirePrepaid fails closed unless the key has PAYG credit: a hard-cutoff
// USD budget on the key that never resets. Without one, Reserve would let
// the job through uncapped, and a periodic cap is not a balance.
func (s *Service) requirePrepaid(ctx context.Context, scope budget.Scope) error {
	if scope.APIKeyID == "" {
		return ErrInsufficientBalance
	}
	dec, err := s.d.Budget.Check(ctx, scope)
	if err != nil {
		return err
	}
	if !dec.Allowed {
		return ErrInsufficientBalance
	}
	for _, bu := range dec.BudgetUsage {
		b := bu.Budget
		if budget.IsKeyCredit(b) && b.ScopeID == scope.APIKeyID && b.HardCutoff && b.LimitMicros > 0 {
			return nil
		}
	}
	return fmt.Errorf("%w: no prepaid balance on this key", ErrInsufficientBalance)
}

// Run starts the workers and blocks until ctx is done and every worker has
// returned. Jobs derive their context from ctx, so cancelling it aborts
// them: each is released and failed, unbilled, before Run returns.
func (s *Service) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for range workerCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case j := <-s.queue:
					s.process(ctx, j)
				}
			}
		}()
	}
	wg.Wait()
	// Jobs still queued are failed now, while the store is open; process
	// aborts them because ctx is done.
	for {
		select {
		case j := <-s.queue:
			s.process(ctx, j)
		default:
			return
		}
	}
}

// Recover fails jobs a previous process left queued or running. Their
// reservations lived in that process's memory, so nothing is held. It
// assumes one instance serves image jobs; with more, a job another
// instance is still running is failed here, and since billing commits only
// with the succeeded state, that job then stays unbilled.
func (s *Service) Recover(ctx context.Context) error {
	n, err := s.d.Jobs.FailStale(ctx, staleMessage)
	if err != nil {
		return err
	}
	if n > 0 {
		s.log.Info("failed image jobs left by restart", "count", n)
	}
	return nil
}

// process runs one job under runCtx, the context of Run: a job is not
// started once it is done, and a running one is aborted, released and
// failed. Exactly one of Confirm or Release settles its reservation,
// including when the generator panics.
func (s *Service) process(runCtx context.Context, j job) {
	var once sync.Once
	settle := func(confirm bool, cost int64) {
		once.Do(func() {
			if confirm {
				s.d.Budget.Confirm(j.scope, cost)
			} else {
				s.d.Budget.Release(j.scope)
			}
		})
	}
	var written []store.ImageFile
	billed := false
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("image job panicked", "job", j.id, "billed", billed, "panic", r)
			if !billed {
				settle(false, 0)
				s.removeFiles(written)
				s.fail(j.id, "Terjadi kesalahan internal. Saldo tidak dipotong.")
			}
		}
		if s.onDone != nil {
			s.onDone(j.id, j.owner)
		}
	}()

	if runCtx.Err() != nil {
		settle(false, 0)
		s.fail(j.id, msgShutdown)
		return
	}
	// A job that waited too long may outlive its reservation (budget keeps
	// one for an hour), after which Confirm would hold nothing back.
	if s.now().Sub(j.enqueuedAt) > maxQueueWait {
		s.log.Warn("image job waited too long in queue", "job", j.id)
		settle(false, 0)
		s.fail(j.id, queueWaitMessage)
		return
	}

	ctx, cancel := context.WithTimeout(runCtx, jobTimeout)
	defer cancel()
	if err := s.d.Jobs.SetRunning(ctx, j.id); err != nil {
		s.log.Warn("image job not runnable", "job", j.id, "err", err)
		settle(false, 0)
		// ErrNotFound: failed by Recover or otherwise no longer queued.
		// Any other error leaves the job queued, so fail it to free the slot.
		if !errors.Is(err, store.ErrNotFound) {
			s.fail(j.id, s.abortMessage(runCtx, msgGenericFail))
		}
		return
	}

	req := j.req
	resp, step, err := s.d.Generator.GenerateImageTarget(ctx, &req, pipeline.MediaOptions{
		Targets: j.targets, TenantID: j.scope.TenantID, ProjectID: j.scope.ProjectID,
		APIKeyID: j.scope.APIKeyID, Limits: j.limits, Reserved: true,
	})
	if err != nil {
		settle(false, 0)
		s.log.Warn("image generation failed", "job", j.id, "err", err)
		s.fail(j.id, s.abortMessage(runCtx, failureMessage(err)))
		return
	}
	// Bill the exact step that answered at its own price. One that is
	// unpriced or above the shown price cannot be billed correctly: fail
	// closed and store nothing.
	price, ok := priceOf(step, j.resolution)
	if !ok || price.sell > j.shown {
		s.log.Error("image step not billable for this job", "job", j.id, "provider", step.Provider, "model", step.Model)
		settle(false, 0)
		s.fail(j.id, "Gagal membuat gambar. Saldo tidak dipotong.")
		return
	}
	var items []core.ImageData
	if resp != nil {
		items = resp.Data
	}
	if len(items) > j.req.N {
		items = items[:j.req.N]
	}
	lost := s.saveAll(ctx, j.owner, items, &written)
	files := written
	if lost > 0 {
		s.log.Warn("image lost after upstream success", "job", j.id, "lost", lost)
	}
	if len(files) == 0 {
		settle(false, 0)
		s.fail(j.id, "Penyedia tidak mengembalikan gambar yang bisa disimpan. Saldo tidak dipotong.")
		return
	}

	rec, cost, err := s.d.Meter.BuildRecord(meter.Event{
		RequestID: "img-" + j.id, TenantID: j.scope.TenantID, ProjectID: j.scope.ProjectID,
		APIKeyID: j.scope.APIKeyID, Provider: step.Provider, Model: step.Model, ChainID: j.chainID,
		Client: "portal", ImageCount: len(files),
		ImageBillMicro: float64(price.sell), ImageCostMicro: float64(price.cost),
	})
	if err != nil {
		s.log.Error("build image usage", "job", j.id, "err", err)
		settle(false, 0)
		s.removeFiles(files)
		s.fail(j.id, msgBillingFail)
		return
	}

	// The usage row and the succeeded state commit together. The
	// reservation stays held until the row is counted (Persisted), so the
	// spend is never uncounted in between.
	switch s.commitBilled(j, files, rec) {
	case outcomeBilled:
		billed = true
		s.d.Meter.Persisted(rec)
		settle(true, cost)
	case outcomeNotBilled:
		settle(false, 0)
		s.removeFiles(files)
	case outcomeUnknown:
		// Neither state could be confirmed. Spend reads see the usage row
		// if it committed, so the hold is dropped; the files stay in case
		// the job is serving them, and the sweeper removes them otherwise.
		s.log.Error("image job billing outcome unknown", "job", j.id, "key", j.scope.APIKeyID, "cost_micro", cost)
		settle(false, 0)
	}
}

type commitOutcome int

const (
	outcomeBilled commitOutcome = iota
	outcomeNotBilled
	outcomeUnknown
)

// commitBilled stores the job as succeeded together with its usage row,
// retrying transient errors. An error may hide a commit whose ack was lost,
// so a failed commit is settled by reading the job back.
func (s *Service) commitBilled(j job, files []store.ImageFile, rec store.UsageRecord) commitOutcome {
	finishedAt := s.now()
	var err error
	for attempt := 0; attempt <= finishRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(finishBackoff * time.Duration(attempt))
		}
		ctx, cancel := context.WithTimeout(context.Background(), settleTimeout)
		err = s.d.Jobs.FinishSucceededBilled(ctx, j.id, files, rec, finishedAt)
		cancel()
		if err == nil {
			return outcomeBilled
		}
		if errors.Is(err, store.ErrImageJobNotActive) {
			break
		}
	}
	s.log.Warn("finish billed image job", "job", j.id, "err", err)
	return s.resolveCommit(j)
}

// resolveCommit decides whether an unacknowledged billed finish committed.
// A succeeded job was billed. A running one is failed here; that update
// only applies while the job is running, so once it succeeds the billed
// commit can no longer happen. Any other state was never billed.
func (s *Service) resolveCommit(j job) commitOutcome {
	for attempt := 0; attempt <= finishRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(finishBackoff * time.Duration(attempt))
		}
		ctx, cancel := context.WithTimeout(context.Background(), settleTimeout)
		got, err := s.d.Jobs.Get(ctx, j.owner, j.id)
		if err == nil && got.Status == store.ImageJobRunning {
			err = s.d.Jobs.Finish(ctx, j.id, store.ImageJobFailed, msgBillingFail, 0, 0, 0, nil, s.now())
			cancel()
			if err == nil {
				return outcomeNotBilled
			}
			continue // succeeded or failed meanwhile: read it again
		}
		cancel()
		switch {
		case err == nil && got.Status == store.ImageJobSucceeded:
			return outcomeBilled
		case err == nil, errors.Is(err, store.ErrNotFound):
			return outcomeNotBilled
		}
		s.log.Warn("read image job after billing error", "job", j.id, "err", err)
	}
	return outcomeUnknown
}

// abortMessage is msg, or the shutdown message when Run is stopping.
func (s *Service) abortMessage(runCtx context.Context, msg string) string {
	if runCtx.Err() != nil {
		return msgShutdown
	}
	return msg
}

// fail marks a job failed with nothing billed. It runs on its own context,
// so it still works while Run is stopping.
func (s *Service) fail(id, msg string) {
	ctx, cancel := context.WithTimeout(context.Background(), failTimeout)
	defer cancel()
	if err := s.d.Jobs.Finish(ctx, id, store.ImageJobFailed, msg, 0, 0, 0, nil, s.now()); err != nil {
		s.log.Warn("mark image job failed", "job", id, "err", err)
	}
}

// Fixed failure messages shown to the user. Provider text never reaches the
// job row: it can name providers or models, or echo a masked upstream key.
const (
	msgModeration  = "Prompt ditolak oleh moderasi. Saldo tidak dipotong."
	msgBusy        = "Layanan sedang sibuk, coba lagi sebentar. Saldo tidak dipotong."
	msgTimeout     = "Pembuatan gambar terlalu lama. Saldo tidak dipotong."
	msgGenericFail = "Gambar gagal dibuat. Saldo tidak dipotong."
	msgBillingFail = "Gagal mencatat pemakaian. Saldo tidak dipotong."
	msgShutdown    = "Server dimulai ulang, gambar gagal dibuat. Saldo tidak dipotong."
)

// moderationWords mark a provider refusal of the prompt itself.
var moderationWords = []string{"moderation", "content policy", "content_policy", "safety", "content filter", "content_filter"}

// failureMessage picks the user message for a generation error by its class.
func failureMessage(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return msgTimeout
	}
	pe := core.AsProviderError(err)
	if pe == nil {
		return msgGenericFail
	}
	text := strings.ToLower(pe.Message)
	switch {
	case pe.Kind == core.ErrContentFilter || pe.Kind == core.ErrPolicyBlocked:
		return msgModeration
	case pe.Kind == core.ErrRateLimit || pe.StatusCode == http.StatusTooManyRequests:
		return msgBusy
	case pe.Kind == core.ErrTimeout || pe.StatusCode == http.StatusGatewayTimeout || pe.StatusCode == http.StatusRequestTimeout:
		return msgTimeout
	}
	for _, w := range moderationWords {
		if strings.Contains(text, w) {
			return msgModeration
		}
	}
	return msgGenericFail
}
