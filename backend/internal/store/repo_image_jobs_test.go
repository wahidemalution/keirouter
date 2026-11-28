package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newImageJob(id, owner string) ImageJob {
	return ImageJob{ID: id, OwnerSub: owner, KeyID: "k", TenantID: DefaultTenantID, ChainID: "c",
		Prompt: "a cat", Params: `{"n":1}`, Status: ImageJobQueued, CreatedAt: time.Now()}
}

func TestImageJobOwnerIsolation(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	r := db.ImageJobs()
	t0 := time.Now().UTC().Truncate(time.Second)
	r.Now = func() time.Time { return t0 }
	require.NoError(t, r.Create(ctx, newImageJob("j1", "A")))
	require.NoError(t, r.Finish(ctx, "j1", ImageJobSucceeded, "", 1, 5, 4,
		[]ImageFile{{ID: "f1", Path: "p", Mime: "image/png", Bytes: 3}}, t0))

	got, err := r.Get(ctx, "A", "j1")
	require.NoError(t, err)
	require.Len(t, got.Files, 1)
	_, err = r.GetFile(ctx, "A", "f1")
	require.NoError(t, err)

	_, err = r.Get(ctx, "B", "j1")
	require.ErrorIs(t, err, ErrNotFound)
	_, err = r.GetFile(ctx, "B", "f1")
	require.ErrorIs(t, err, ErrNotFound)
	_, err = r.Delete(ctx, "B", "j1")
	require.ErrorIs(t, err, ErrNotFound)
	l, err := r.ListByOwner(ctx, "B", 10)
	require.NoError(t, err)
	require.Empty(t, l)

	files, err := r.Delete(ctx, "A", "j1")
	require.NoError(t, err)
	require.Len(t, files, 1)
	_, err = r.Get(ctx, "A", "j1")
	require.ErrorIs(t, err, ErrNotFound)
	_, err = r.GetFile(ctx, "A", "f1")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestImageJobExpiry(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	r := db.ImageJobs()
	tm := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	require.NoError(t, r.Create(ctx, newImageJob("j1", "A")))
	require.NoError(t, r.Finish(ctx, "j1", ImageJobSucceeded, "", 1, 5, 4,
		[]ImageFile{{ID: "f1", Path: "p", Mime: "image/png", Bytes: 3}}, tm))
	got, err := r.Get(ctx, "A", "j1")
	require.NoError(t, err)
	require.True(t, got.ExpiresAt.Equal(tm.Add(24*time.Hour)))

	exp, err := r.ListExpired(ctx, tm.Add(24*time.Hour))
	require.NoError(t, err)
	require.Empty(t, exp)
	exp, err = r.ListExpired(ctx, tm.Add(24*time.Hour+time.Second))
	require.NoError(t, err)
	require.Len(t, exp, 1)
	require.Len(t, exp[0].Files, 1)

	r.Now = func() time.Time { return tm.Add(time.Hour) }
	_, err = r.GetFile(ctx, "A", "f1")
	require.NoError(t, err)
	r.Now = func() time.Time { return tm.Add(24*time.Hour + time.Second) }
	_, err = r.GetFile(ctx, "A", "f1")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestImageJobListOrder(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	r := db.ImageJobs()
	a, b := newImageJob("a", "A"), newImageJob("b", "A")
	a.CreatedAt = time.Now().Add(-time.Hour)
	require.NoError(t, r.Create(ctx, a))
	require.NoError(t, r.Create(ctx, b))
	l, err := r.ListByOwner(ctx, "A", 10)
	require.NoError(t, err)
	require.Equal(t, []string{"b", "a"}, []string{l[0].ID, l[1].ID})
}

func TestCountActive(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	r := db.ImageJobs()
	require.NoError(t, r.Create(ctx, newImageJob("j1", "A")))
	require.NoError(t, r.Create(ctx, newImageJob("j2", "A")))
	require.NoError(t, r.Create(ctx, newImageJob("j3", "B")))
	require.NoError(t, r.SetRunning(ctx, "j2"))
	n, err := r.CountActive(ctx, "A")
	require.NoError(t, err)
	require.Equal(t, 2, n)
	require.NoError(t, r.Finish(ctx, "j1", ImageJobFailed, "boom", 0, 0, 0, nil, time.Now()))
	n, err = r.CountActive(ctx, "A")
	require.NoError(t, err)
	require.Equal(t, 1, n)
}

func TestFailStale(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	r := db.ImageJobs()
	tn := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	r.Now = func() time.Time { return tn }
	require.NoError(t, r.Create(ctx, newImageJob("j1", "A")))
	require.NoError(t, r.Create(ctx, newImageJob("j2", "A")))
	require.NoError(t, r.Create(ctx, newImageJob("j3", "A")))
	require.NoError(t, r.SetRunning(ctx, "j2"))
	require.NoError(t, r.Finish(ctx, "j3", ImageJobSucceeded, "", 1, 1, 1, nil, time.Now()))
	n, err := r.FailStale(ctx, "restarted")
	require.NoError(t, err)
	require.Equal(t, 2, n)
	j, err := r.Get(ctx, "A", "j1")
	require.NoError(t, err)
	require.Equal(t, ImageJobFailed, j.Status)
	require.Equal(t, "restarted", j.Error)
	require.True(t, j.ExpiresAt.Equal(tn.Add(24*time.Hour)))
	require.ErrorIs(t, r.Finish(ctx, "j1", ImageJobSucceeded, "", 1, 1, 1, nil, tn), ErrImageJobNotActive)
	j, _ = r.Get(ctx, "A", "j3")
	require.Equal(t, ImageJobSucceeded, j.Status)
}

func TestImageJobFinishOnce(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	r := db.ImageJobs()
	t1 := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	require.NoError(t, r.Create(ctx, newImageJob("j1", "A")))
	require.Error(t, r.Finish(ctx, "j1", ImageJobQueued, "", 0, 0, 0, nil, t1))
	require.NoError(t, r.Finish(ctx, "j1", ImageJobSucceeded, "", 1, 5, 4,
		[]ImageFile{{ID: "f1", Path: "p", Mime: "image/png", Bytes: 3}}, t1))

	err := r.Finish(ctx, "j1", ImageJobSucceeded, "", 9, 99, 98,
		[]ImageFile{{ID: "f2", Path: "q", Mime: "image/png", Bytes: 1}}, t1.Add(time.Hour))
	require.ErrorIs(t, err, ErrImageJobNotActive)
	got, err := r.Get(ctx, "A", "j1")
	require.NoError(t, err)
	require.Equal(t, 1, got.ImageCount)
	require.Equal(t, int64(5), got.CostMicro)
	require.Len(t, got.Files, 1)
	require.True(t, got.ExpiresAt.Equal(t1.Add(24*time.Hour)))
}

func TestImageJobDeleteActive(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	r := db.ImageJobs()
	require.NoError(t, r.Create(ctx, newImageJob("j1", "A")))
	_, err := r.Delete(ctx, "A", "j1")
	require.ErrorIs(t, err, ErrImageJobActive)
	require.NoError(t, r.SetRunning(ctx, "j1"))
	_, err = r.Delete(ctx, "A", "j1")
	require.ErrorIs(t, err, ErrImageJobActive)
	_, err = r.Delete(ctx, "B", "j1")
	require.ErrorIs(t, err, ErrNotFound)
	_, err = r.Get(ctx, "A", "j1")
	require.NoError(t, err)
}

func TestImageJobSetRunningGuard(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	r := db.ImageJobs()
	require.ErrorIs(t, r.SetRunning(ctx, "nope"), ErrNotFound)
	require.NoError(t, r.Create(ctx, newImageJob("j1", "A")))
	require.NoError(t, r.SetRunning(ctx, "j1"))
	require.ErrorIs(t, r.SetRunning(ctx, "j1"), ErrNotFound)
}

func TestImageJobListFilesBatched(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	r := db.ImageJobs()
	for _, id := range []string{"a", "b"} {
		require.NoError(t, r.Create(ctx, newImageJob(id, "A")))
		require.NoError(t, r.Finish(ctx, id, ImageJobSucceeded, "", 2, 1, 1, []ImageFile{
			{ID: id + "1", Path: "p", Mime: "image/png", Bytes: 1},
			{ID: id + "2", Path: "p", Mime: "image/png", Bytes: 1}}, time.Now()))
	}
	l, err := r.ListByOwner(ctx, "A", 10)
	require.NoError(t, err)
	require.Len(t, l, 2)
	for _, j := range l {
		require.Len(t, j.Files, 2)
		require.Equal(t, j.ID, j.Files[0].JobID)
	}
}
