package backupjob

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/store"
)

// fakeTarget is an in-memory Target with injectable failures.
type fakeTarget struct {
	mu        sync.Mutex
	files     map[string][]byte
	putErr    error
	sizeDelta int64
	deleteErr error
	block     chan struct{} // Put waits for this to close (or ctx) when set
	entered   chan struct{} // closed when Put starts
}

func newFakeTarget() *fakeTarget { return &fakeTarget{files: map[string][]byte{}} }

func (f *fakeTarget) Put(ctx context.Context, name string, r io.Reader, size int64) error {
	if f.entered != nil {
		close(f.entered)
	}
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if f.putErr != nil {
		return f.putErr
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[name] = b
	return nil
}

func (f *fakeTarget) Size(_ context.Context, name string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.files[name]
	if !ok {
		return 0, errors.New("not found")
	}
	return int64(len(b)) + f.sizeDelta, nil
}

func (f *fakeTarget) List(context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for n := range f.files {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

func (f *fakeTarget) Delete(_ context.Context, name string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.files, name)
	return nil
}

func (f *fakeTarget) Test(context.Context) error { return nil }

type harness struct {
	db     *store.DB
	box    Opener
	dir    string
	target *fakeTarget
	now    time.Time
	audits []string
	r      *Runner
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{db: newDB(t), box: newSealer(t), dir: t.TempDir(), target: newFakeTarget(),
		now: time.Date(2026, 11, 1, 14, 0, 0, 0, time.UTC)}
	s := validS3()
	s.Keep = 7
	require.NoError(t, Save(context.Background(), h.db.Settings().Set, h.box, s))
	var mu sync.Mutex
	h.r = New(Deps{
		DB: h.db, Box: h.box, DataDir: h.dir, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Audit:     func(action, detail string) { mu.Lock(); h.audits = append(h.audits, action); mu.Unlock() },
		NewTarget: func(Settings) (Target, error) { return h.target, nil },
		Now:       func() time.Time { return h.now },
		Owner:     "test-owner",
	})
	return h
}

func (h *harness) status(t *testing.T) Status {
	t.Helper()
	st, err := h.r.Status(context.Background())
	require.NoError(t, err)
	return st
}

func tmpFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, _ := os.ReadDir(filepath.Join(dir, "tmp"))
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func TestRunSuccessRotates(t *testing.T) {
	h := newHarness(t)
	for d := 1; d <= 8; d++ {
		h.target.files[FileName(time.Date(2026, 10, d, 0, 0, 0, 0, time.UTC))] = []byte("old")
	}
	h.target.files["notes.txt"] = []byte("keep me")
	require.NoError(t, h.r.runOnce(context.Background()))

	names, _ := h.target.List(context.Background())
	var ours []string
	for _, n := range names {
		if IsBackupName(n) {
			ours = append(ours, n)
		}
	}
	require.Len(t, ours, 7)
	require.Contains(t, ours, FileName(h.now))
	require.Contains(t, names, "notes.txt")
	st := h.status(t)
	require.Equal(t, "success", st.History[0].Result)
	require.Equal(t, FileName(h.now), st.History[0].File)
	require.Positive(t, st.History[0].Size)
	require.Equal(t, h.now, st.State.LastSuccess)
	require.Empty(t, tmpFiles(t, h.dir))
}

func TestRunUploadFailureSkipsRotation(t *testing.T) {
	h := newHarness(t)
	for d := 1; d <= 9; d++ {
		h.target.files[FileName(time.Date(2026, 10, d, 0, 0, 0, 0, time.UTC))] = []byte("old")
	}
	h.target.putErr = errors.New("connection reset")
	require.Error(t, h.r.runOnce(context.Background()))
	require.Len(t, h.target.files, 9, "nothing deleted after a failed upload")
	st := h.status(t)
	require.Equal(t, "failed", st.History[0].Result)
	require.Contains(t, st.History[0].Note, "connection reset")
	require.Equal(t, h.now.Add(30*time.Minute), st.NextDue)
	require.Empty(t, tmpFiles(t, h.dir))
}

func TestRunVerifyMismatchFails(t *testing.T) {
	h := newHarness(t)
	h.target.sizeDelta = -1
	require.Error(t, h.r.runOnce(context.Background()))
	require.Equal(t, "failed", h.status(t).History[0].Result)
	require.Contains(t, h.status(t).History[0].Note, "size")
}

func TestRotationDeleteFailureStillSucceeds(t *testing.T) {
	h := newHarness(t)
	for d := 1; d <= 8; d++ {
		h.target.files[FileName(time.Date(2026, 10, d, 0, 0, 0, 0, time.UTC))] = []byte("old")
	}
	h.target.deleteErr = errors.New("permission denied")
	require.NoError(t, h.r.runOnce(context.Background()))
	rec := h.status(t).History[0]
	require.Equal(t, "success", rec.Result)
	require.Contains(t, rec.Note, "rotation")
}

func TestThreeFailuresAudit(t *testing.T) {
	h := newHarness(t)
	h.target.putErr = errors.New("down")
	for i := 0; i < 3; i++ {
		_ = h.r.runOnce(context.Background())
		h.now = h.now.Add(time.Hour)
	}
	require.True(t, h.status(t).Failing)
	require.Len(t, h.audits, 1)
	_ = h.r.runOnce(context.Background())
	require.Len(t, h.audits, 1, "one audit entry per failure streak")
	h.target.putErr = nil
	require.NoError(t, h.r.runOnce(context.Background()))
	require.False(t, h.status(t).Failing)
	require.Zero(t, h.status(t).State.ConsecutiveFailures)
}

func TestRunNowBusy(t *testing.T) {
	h := newHarness(t)
	h.target.block, h.target.entered = make(chan struct{}), make(chan struct{})
	require.NoError(t, h.r.RunNow(context.Background()))
	<-h.target.entered
	require.ErrorIs(t, h.r.RunNow(context.Background()), ErrBusy)
	require.True(t, h.status(t).Running)

	// Another replica holding the lease also blocks this one.
	other := New(Deps{DB: h.db, Box: h.box, DataDir: t.TempDir(), Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		NewTarget: func(Settings) (Target, error) { return h.target, nil }, Now: func() time.Time { return h.now }, Owner: "other"})
	require.ErrorIs(t, other.runOnce(context.Background()), ErrBusy)

	close(h.target.block)
	require.Eventually(t, func() bool { return !h.status(t).Running }, 5*time.Second, 20*time.Millisecond)
	require.Equal(t, "success", h.status(t).History[0].Result)
}

func TestCancelRecordsCancelled(t *testing.T) {
	h := newHarness(t)
	h.target.block, h.target.entered = make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.r.runOnce(ctx) }()
	<-h.target.entered
	cancel()
	require.Error(t, <-done)
	st := h.status(t)
	require.Equal(t, "cancelled", st.History[0].Result)
	require.Zero(t, st.State.ConsecutiveFailures, "a shutdown is not a destination failure")
	require.Empty(t, tmpFiles(t, h.dir))
}

// Review Focus 4: disabling during a run lets it finish and stops later runs.
func TestDisableMidRun(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.target.block, h.target.entered = make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- h.r.runOnce(ctx) }()
	<-h.target.entered

	s, err := Load(ctx, h.db.Settings().Get, h.box)
	require.NoError(t, err)
	s.Enabled = false
	require.NoError(t, Save(ctx, h.db.Settings().Set, h.box, s))
	close(h.target.block)
	require.NoError(t, <-done)
	require.Equal(t, "success", h.status(t).History[0].Result)

	h.now = h.now.Add(48 * time.Hour)
	h.target.block, h.target.entered = nil, nil
	h.r.tick(ctx)
	require.Len(t, h.status(t).History, 1, "no run after disable")
}

func TestTickRunsWhenDue(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.r.tick(ctx)
	require.Eventually(t, func() bool { st := h.status(t); return !st.Running && len(st.History) == 1 }, 5*time.Second, 20*time.Millisecond)
	h.now = h.now.Add(23 * time.Hour)
	h.r.tick(ctx)
	time.Sleep(100 * time.Millisecond)
	require.Len(t, h.status(t).History, 1, "not due before the interval")
	h.now = h.now.Add(2 * time.Hour)
	h.r.tick(ctx)
	require.Eventually(t, func() bool { return len(h.status(t).History) == 2 }, 5*time.Second, 20*time.Millisecond)
}

func TestStartupCleansTemp(t *testing.T) {
	h := newHarness(t)
	tmp := filepath.Join(h.dir, "tmp")
	require.NoError(t, os.MkdirAll(tmp, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "keirouter-backup-x.json.gz"), []byte("stale"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "unrelated.txt"), []byte("keep"), 0o600))
	s, _ := Load(context.Background(), h.db.Settings().Get, h.box)
	s.Enabled = false
	require.NoError(t, Save(context.Background(), h.db.Settings().Set, h.box, s))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h.r.Run(ctx)
	require.Equal(t, []string{"unrelated.txt"}, tmpFiles(t, h.dir))
}

func TestHistoryCapped(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < 25; i++ {
		require.NoError(t, h.r.runOnce(context.Background()))
		h.now = h.now.Add(time.Hour)
	}
	hist := h.status(t).History
	require.Len(t, hist, 20)
	require.True(t, hist[0].StartedAt.After(hist[19].StartedAt), "newest first")
}

func TestBackupFileRestores(t *testing.T) {
	h := newHarness(t)
	require.NoError(t, h.r.runOnce(context.Background()))
	raw := h.target.files[FileName(h.now)]
	require.NotEmpty(t, raw)
	require.Equal(t, []byte{0x1f, 0x8b}, raw[:2], "file is gzip")
}

// Shutdown: Run must not return while a run it started is still recording,
// or the app would close the database under it.
func TestRunWaitsForInflightRun(t *testing.T) {
	h := newHarness(t)
	h.target.block, h.target.entered = make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	returned := make(chan struct{})
	go func() { h.r.Run(ctx); close(returned) }()
	<-h.target.entered
	cancel()
	<-returned
	hist := h.status(t).History
	require.Len(t, hist, 1, "the cancelled run is recorded before Run returns")
	require.Equal(t, ResultCancelled, hist[0].Result)
}

// A manual run follows the runner's lifetime, not the HTTP request: it
// survives the request ending but stops on shutdown.
func TestRunNowCancelledOnShutdown(t *testing.T) {
	h := newHarness(t)
	s, _ := Load(context.Background(), h.db.Settings().Get, h.box)
	s.Enabled = false
	require.NoError(t, Save(context.Background(), h.db.Settings().Set, h.box, s))
	ctx, cancel := context.WithCancel(context.Background())
	returned := make(chan struct{})
	go func() { h.r.Run(ctx); close(returned) }()
	require.Eventually(t, func() bool { return h.r.started() }, 2*time.Second, 5*time.Millisecond)

	h.target.block, h.target.entered = make(chan struct{}), make(chan struct{})
	reqCtx, endRequest := context.WithCancel(context.Background())
	require.NoError(t, h.r.RunNow(reqCtx))
	endRequest()
	<-h.target.entered
	cancel()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown waited on a manual run that ignores cancellation")
	}
	require.Equal(t, ResultCancelled, h.status(t).History[0].Result)
}

// Review I-4: a file Restore would refuse must fail the run, not show as a
// good restore point.
func TestRunFailsWhenBackupExceedsRestoreLimit(t *testing.T) {
	h := newHarness(t)
	h.r.maxRaw = 100
	require.Error(t, h.r.runOnce(context.Background()))
	rec := h.status(t).History[0]
	require.Equal(t, ResultFailed, rec.Result)
	require.Contains(t, rec.Note, "too large to restore")
	require.Empty(t, h.target.files, "nothing uploaded")
	require.Empty(t, tmpFiles(t, h.dir))
}
