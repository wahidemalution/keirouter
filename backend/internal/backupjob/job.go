package backupjob

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mydisha/keirouter/backend/internal/store"
)

// ErrBusy is returned when a backup is already running here or elsewhere.
var ErrBusy = errors.New("a backup is already running")

const (
	tickInterval    = time.Minute
	leaseTTL        = 5 * time.Minute
	failingAfter    = 3
	recordTimeout   = 10 * time.Second
	tempFilePrefix  = "keirouter-backup-"
	auditFailingKey = "backup.auto.failing"
)

// Deps wires a Runner. Now and NewTarget are injectable for tests.
type Deps struct {
	DB        *store.DB
	Box       Opener
	DataDir   string
	Log       *slog.Logger
	Audit     func(action, detail string)
	NewTarget func(Settings) (Target, error)
	Now       func() time.Time
	Owner     string
}

// Runner runs scheduled offsite backups.
type Runner struct {
	d          Deps
	mu         sync.Mutex
	running    bool
	inflight   sync.WaitGroup
	life       context.Context // Run's context; manual runs stop with it
	maxRaw     int64           // largest uncompressed backup Restore accepts
	bytesSent  atomic.Int64
	bytesTotal atomic.Int64
}

// Status is what the dashboard shows.
type Status struct {
	State      State       `json:"state"`
	NextDue    time.Time   `json:"next_due"`
	Running    bool        `json:"running"`
	BytesSent  int64       `json:"bytes_sent"`
	BytesTotal int64       `json:"bytes_total"`
	Failing    bool        `json:"failing"`
	History    []RunRecord `json:"history"`
}

// New builds a Runner.
func New(d Deps) *Runner {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.NewTarget == nil {
		d.NewTarget = NewTarget
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.Audit == nil {
		d.Audit = func(string, string) {}
	}
	return &Runner{d: d, maxRaw: MaxRestoreBytes}
}

// started reports whether Run has begun.
func (r *Runner) started() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.life != nil
}

func (r *Runner) tempDir() string { return filepath.Join(r.d.DataDir, "tmp") }

// Run cleans leftovers from a crash, then checks every minute until ctx
// ends. It returns only after a run it started has been recorded.
func (r *Runner) Run(ctx context.Context) {
	defer r.inflight.Wait()
	r.mu.Lock()
	r.life = ctx
	r.mu.Unlock()
	r.cleanTemp()
	if ctx.Err() != nil {
		return
	}
	r.tick(ctx)
	t := time.NewTicker(tickInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.tick(ctx)
		}
	}
}

func (r *Runner) cleanTemp() {
	entries, err := os.ReadDir(r.tempDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), tempFilePrefix) {
			_ = os.Remove(filepath.Join(r.tempDir(), e.Name()))
		}
	}
}

// tick starts a run in the background when one is due.
func (r *Runner) tick(ctx context.Context) {
	r.mu.Lock()
	busy := r.running
	r.mu.Unlock()
	if busy {
		return
	}
	s, err := Load(ctx, r.d.DB.Settings().Get, r.d.Box)
	if err != nil {
		r.d.Log.Warn("auto backup: load settings", "err", err)
		return
	}
	if !s.Enabled {
		return
	}
	st, err := loadState(ctx, r.d.DB.Settings())
	if err != nil {
		r.d.Log.Warn("auto backup: load state", "err", err)
		return
	}
	if r.d.Now().Before(NextDue(st, time.Duration(s.IntervalHours)*time.Hour)) {
		return
	}
	stop, err := r.begin(ctx)
	if err != nil {
		if !errors.Is(err, ErrBusy) {
			r.d.Log.Warn("auto backup: take lease", "err", err)
		}
		return
	}
	r.inflight.Add(1)
	go func() {
		defer r.inflight.Done()
		defer stop()
		if err := r.perform(ctx); err != nil {
			r.d.Log.Warn("auto backup failed", "err", err)
		}
	}()
}

// RunNow starts a run in the background, or returns ErrBusy.
func (r *Runner) RunNow(ctx context.Context) error {
	r.mu.Lock()
	if r.life != nil {
		ctx = r.life
	} else {
		ctx = context.WithoutCancel(ctx)
	}
	r.mu.Unlock()
	stop, err := r.begin(ctx)
	if err != nil {
		return err
	}
	r.inflight.Add(1)
	go func() {
		defer r.inflight.Done()
		defer stop()
		if err := r.perform(ctx); err != nil {
			r.d.Log.Warn("auto backup failed", "err", err)
		}
	}()
	return nil
}

// Status reports the schedule, progress and recent runs.
func (r *Runner) Status(ctx context.Context) (Status, error) {
	s, err := Load(ctx, r.d.DB.Settings().Get, r.d.Box)
	if err != nil {
		return Status{}, err
	}
	st, err := loadState(ctx, r.d.DB.Settings())
	if err != nil {
		return Status{}, err
	}
	hist, err := loadHistory(ctx, r.d.DB.Settings())
	if err != nil {
		return Status{}, err
	}
	r.mu.Lock()
	running := r.running
	r.mu.Unlock()
	out := Status{
		State: st, Running: running, Failing: st.ConsecutiveFailures >= failingAfter, History: hist,
		NextDue: NextDue(st, time.Duration(s.IntervalHours)*time.Hour),
	}
	if running {
		out.BytesSent, out.BytesTotal = r.bytesSent.Load(), r.bytesTotal.Load()
	}
	if out.History == nil {
		out.History = []RunRecord{}
	}
	return out, nil
}

// runOnce performs one run synchronously: lease, build, upload, verify,
// rotate, record.
func (r *Runner) runOnce(ctx context.Context) error {
	stop, err := r.begin(ctx)
	if err != nil {
		return err
	}
	defer stop()
	return r.perform(ctx)
}

// begin marks the runner busy and takes the lease. The returned stop ends
// lease renewal, releases the lease and clears the busy flag.
func (r *Runner) begin(ctx context.Context) (func(), error) {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return nil, ErrBusy
	}
	r.running = true
	r.mu.Unlock()
	clear := func() {
		r.mu.Lock()
		r.running = false
		r.mu.Unlock()
	}

	settings := r.d.DB.Settings()
	ok, err := settings.ClaimLease(ctx, LeaseKey, r.d.Owner, r.d.Now(), leaseTTL)
	if err != nil || !ok {
		clear()
		if err == nil {
			err = ErrBusy
		}
		return nil, err
	}
	renewCtx, stopRenew := context.WithCancel(ctx)
	go r.renewLease(renewCtx)
	return func() {
		stopRenew()
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
		defer cancel()
		_ = settings.ReleaseLease(rctx, LeaseKey, r.d.Owner)
		clear()
	}, nil
}

// perform runs and records one backup; the caller holds the lease.
func (r *Runner) perform(ctx context.Context) error {
	r.bytesSent.Store(0)
	r.bytesTotal.Store(0)
	start := r.d.Now()
	wall := time.Now()
	rec := RunRecord{StartedAt: start, File: FileName(start)}
	size, note, runErr := r.execute(ctx, rec.File)
	rec.Size, rec.Note = size, note
	rec.DurationMs = time.Since(wall).Milliseconds()
	switch {
	case runErr == nil:
		rec.Result = ResultSuccess
	case ctx.Err() != nil:
		rec.Result = ResultCancelled
		rec.Note = "cancelled: " + runErr.Error()
	default:
		rec.Result = ResultFailed
		rec.Note = runErr.Error()
	}
	r.record(ctx, rec)
	return runErr
}

func (r *Runner) renewLease(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := r.d.DB.Settings().ClaimLease(ctx, LeaseKey, r.d.Owner, r.d.Now(), leaseTTL); err != nil && ctx.Err() == nil {
				r.d.Log.Warn("auto backup: renew lease", "err", err)
			}
		}
	}
}

// execute builds and ships one file. note carries warnings on success.
func (r *Runner) execute(ctx context.Context, name string) (size int64, note string, err error) {
	s, err := Load(ctx, r.d.DB.Settings().Get, r.d.Box)
	if err != nil {
		return 0, "", fmt.Errorf("load settings: %w", err)
	}
	complete := s
	complete.Enabled = true // a run, scheduled or manual, needs a complete destination
	if err := complete.Validate(); err != nil {
		return 0, "", err
	}
	target, err := r.d.NewTarget(s)
	if err != nil {
		return 0, "", err
	}

	if err := os.MkdirAll(r.tempDir(), 0o700); err != nil {
		return 0, "", fmt.Errorf("temp dir: %w", err)
	}
	path := filepath.Join(r.tempDir(), name)
	defer os.Remove(path)
	sum, size, err := r.writeBackup(ctx, path, s.Passphrase)
	if err != nil {
		return 0, "", fmt.Errorf("build: %w", err)
	}
	r.bytesTotal.Store(size)

	f, err := os.Open(path)
	if err != nil {
		return size, "", err
	}
	defer f.Close()
	uctx, cancel := context.WithTimeout(ctx, UploadTimeout(size, s.UploadLimitKBps))
	defer cancel()
	body := &countingReader{r: NewLimitedReader(uctx, f, s.UploadLimitKBps), n: &r.bytesSent}
	if h, ok := target.(PutHasher); ok {
		err = h.PutWithHash(uctx, name, body, size, sum)
	} else {
		err = target.Put(uctx, name, body, size)
	}
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			return size, "", fmt.Errorf("upload timed out after %s", UploadTimeout(size, s.UploadLimitKBps))
		}
		return size, "", fmt.Errorf("upload: %w", err)
	}
	remote, err := target.Size(ctx, name)
	if err != nil {
		return size, "", fmt.Errorf("verify: %w", err)
	}
	if remote != size {
		return size, "", fmt.Errorf("verify: remote size %d, local size %d", remote, size)
	}
	return size, r.rotate(ctx, target, s.Keep), nil
}

// rotate deletes old backups; failures become a note, not a failed run.
func (r *Runner) rotate(ctx context.Context, target Target, keep int) string {
	names, err := target.List(ctx)
	if err != nil {
		return "rotation skipped: " + err.Error()
	}
	var failed []string
	for _, n := range SelectForDeletion(names, keep) {
		if err := target.Delete(ctx, n); err != nil {
			failed = append(failed, n+": "+err.Error())
		}
	}
	if len(failed) > 0 {
		return fmt.Sprintf("rotation: %d delete(s) failed: %s", len(failed), strings.Join(failed, "; "))
	}
	return ""
}

func (r *Runner) writeBackup(ctx context.Context, path, passphrase string) (string, int64, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	cw := &countingWriter{w: io.MultiWriter(f, h)}
	zw := gzip.NewWriter(cw)
	raw := &countingWriter{w: zw}
	_, berr := Build(ctx, r.d.DB, r.d.Box, passphrase, raw)
	zerr := zw.Close()
	ferr := f.Close()
	if err := errors.Join(berr, zerr, ferr); err != nil {
		return "", 0, err
	}
	if raw.n > r.maxRaw {
		return "", 0, fmt.Errorf("backup is %d bytes uncompressed, too large to restore (limit %d)", raw.n, r.maxRaw)
	}
	return hex.EncodeToString(h.Sum(nil)), cw.n, nil
}

// record stores the run in history and state, even after ctx is cancelled.
func (r *Runner) record(ctx context.Context, rec RunRecord) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
	defer cancel()
	settings := r.d.DB.Settings()
	st, err := loadState(rctx, settings)
	if err != nil {
		st = State{}
	}
	st.LastAttempt = rec.StartedAt
	switch rec.Result {
	case ResultSuccess:
		st.LastSuccess, st.LastFailed, st.ConsecutiveFailures = rec.StartedAt, false, 0
		st.LastSize, st.LastFile = rec.Size, rec.File
	case ResultFailed:
		st.LastFailed = true
		st.ConsecutiveFailures++
		if st.ConsecutiveFailures == failingAfter {
			r.d.Audit(auditFailingKey, fmt.Sprintf("%d scheduled backups failed in a row: %s", failingAfter, rec.Note))
		}
	case ResultCancelled:
		st.LastFailed = true
	}
	if err := saveJSON(rctx, settings, StateKey, st); err != nil {
		r.d.Log.Warn("auto backup: save state", "err", err)
	}
	if err := prependHistory(rctx, settings, rec); err != nil {
		r.d.Log.Warn("auto backup: save history", "err", err)
	}
}

type countingReader struct {
	r io.Reader
	n *atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}
