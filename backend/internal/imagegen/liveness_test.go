package imagegen

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/store"
)

// Files no job row points at (an unknown billing outcome, a crash between
// save and commit) are removed once older than 26h. Symlinks are never
// followed, and nothing outside the images directory is touched.
func TestSweepRemovesOrphanFiles(t *testing.T) {
	e := newEnv(t, nImages)
	now := time.Now()
	old := now.Add(-27 * time.Hour)
	ownerDir := filepath.Join(e.dataDir, "images", ownerHash("A"))
	require.NoError(t, os.MkdirAll(ownerDir, 0o700))

	write := func(path string, mtime time.Time) {
		require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))
		require.NoError(t, os.Chtimes(path, mtime, mtime))
	}
	stale := filepath.Join(ownerDir, "stale.png")
	fresh := filepath.Join(ownerDir, "fresh.png")
	write(stale, old)
	write(fresh, now.Add(-25*time.Hour))

	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "keep.png")
	write(outside, old)
	require.NoError(t, os.Symlink(outside, filepath.Join(ownerDir, "link.png")))
	require.NoError(t, os.Symlink(outsideDir, filepath.Join(e.dataDir, "images", "linkdir")))
	loose := filepath.Join(e.dataDir, "images", "loose.png")
	write(loose, old)

	require.NoError(t, e.svc.Sweep(context.Background(), now))
	_, err := os.Stat(stale)
	require.True(t, errors.Is(err, os.ErrNotExist), "a stale orphan is removed")
	_, err = os.Stat(fresh)
	require.NoError(t, err, "a file under 26h stays")
	_, err = os.Stat(outside)
	require.NoError(t, err, "a symlink target outside the images directory stays")
	_, err = os.Lstat(filepath.Join(ownerDir, "link.png"))
	require.NoError(t, err, "the symlink itself is not a regular file and stays")
	_, err = os.Stat(loose)
	require.NoError(t, err, "only files inside an owner directory are swept")
}

// A job left queued or running for 55 minutes holds a slot of its owner;
// the sweeper fails it, unbilled.
func TestSweepFailsStuckJobs(t *testing.T) {
	e := newEnv(t, nImages)
	ctx := context.Background()
	now := time.Now()
	jobs := e.db.ImageJobs()
	mk := func(id string, created time.Time, running bool) {
		require.NoError(t, jobs.Create(ctx, store.ImageJob{ID: id, OwnerSub: "A", KeyID: "k1", TenantID: store.DefaultTenantID,
			ChainID: "c", Prompt: "p", Params: "{}", Status: store.ImageJobQueued, CreatedAt: created}))
		if running {
			require.NoError(t, jobs.SetRunning(ctx, id))
		}
	}
	mk("old-queued", now.Add(-56*time.Minute), false)
	mk("old-running", now.Add(-2*time.Hour), true)
	mk("recent", now.Add(-10*time.Minute), true)

	require.NoError(t, e.svc.Sweep(ctx, now))
	for _, id := range []string{"old-queued", "old-running"} {
		got, err := jobs.Get(ctx, "A", id)
		require.NoError(t, err)
		require.Equal(t, store.ImageJobFailed, got.Status, id)
		require.Equal(t, msgGenericFail, got.Error)
	}
	got, err := jobs.Get(ctx, "A", "recent")
	require.NoError(t, err)
	require.Equal(t, store.ImageJobRunning, got.Status)
	n, err := jobs.CountActive(ctx, "A")
	require.NoError(t, err)
	require.Equal(t, 1, n, "the stuck jobs no longer hold slots")
}

// setRunningErrJobs fails SetRunning with a database error.
type setRunningErrJobs struct{ *store.ImageJobsRepo }

func (setRunningErrJobs) SetRunning(context.Context, string) error { return errors.New("database is locked") }

func TestSetRunningErrorFailsJob(t *testing.T) {
	e := newEnv(t, nImages)
	e.svc.d.Jobs = setRunningErrJobs{e.db.ImageJobs()}
	e.start()
	key := e.key("k1", priceStd, true)
	job := e.submitWait(input("A", key, 1))
	require.Equal(t, store.ImageJobFailed, job.Status, "the job does not stay queued")
	require.Zero(t, e.gen.callCount())
	require.Zero(t, e.spend("k1"))
	e.requireSettledOnce()
}

// lostCreateJobs inserts the job but reports an error, as when the insert
// commits and its ack is lost.
type lostCreateJobs struct{ *store.ImageJobsRepo }

func (j lostCreateJobs) Create(ctx context.Context, job store.ImageJob) error {
	if err := j.ImageJobsRepo.Create(ctx, job); err != nil {
		return err
	}
	return errors.New("connection reset")
}

func TestCreateErrorNeverOrphansJob(t *testing.T) {
	e := newEnv(t, nImages)
	e.svc.d.Jobs = lostCreateJobs{e.db.ImageJobs()}
	key := e.key("k1", priceStd, true)
	_, err := e.svc.Submit(context.Background(), input("A", key, 1))
	require.Error(t, err)
	jobs, err := e.db.ImageJobs().ListByOwner(context.Background(), "A", 10)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	require.Equal(t, store.ImageJobFailed, jobs[0].Status, "the inserted row is not left queued")
	n, err := e.db.ImageJobs().CountActive(context.Background(), "A")
	require.NoError(t, err)
	require.Zero(t, n)
	require.Zero(t, e.spend("k1"))
	e.requireSettledOnce()
}
