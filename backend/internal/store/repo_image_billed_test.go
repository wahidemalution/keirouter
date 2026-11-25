package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func imageUsage(id, key string, cost int64) UsageRecord {
	return UsageRecord{ID: id, RequestID: "img-j1", TenantID: DefaultTenantID, APIKeyID: key,
		Provider: "openai", Model: "gpt-image-1", Status: "success", UsageSource: "provider",
		CostMicros: cost, CostNanos: cost * 1000, UpstreamCostMicros: cost / 2, UpstreamCostNanos: cost / 2 * 1000,
		ImageCount: 2, CreatedAt: time.Now().UTC()}
}

func keySpend(t *testing.T, db *DB, key string) int64 {
	t.Helper()
	spent, _, err := db.Usage().SpendAndTokens(context.Background(), ScopeAPIKey, key, time.Time{})
	require.NoError(t, err)
	return spent
}

func runningJob(t *testing.T, r *ImageJobsRepo, id string) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, r.Create(ctx, newImageJob(id, "A")))
	require.NoError(t, r.SetRunning(ctx, id))
}

var billedFiles = []ImageFile{{ID: "f1", Path: "p1", Mime: "image/png", Bytes: 3}, {ID: "f2", Path: "p2", Mime: "image/png", Bytes: 3}}

func TestFinishSucceededBilledCommitsBoth(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	r := db.ImageJobs()
	runningJob(t, r, "j1")
	at := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, r.FinishSucceededBilled(ctx, "j1", billedFiles, imageUsage("u1", "k", 16_800), at))

	got, err := r.Get(ctx, "A", "j1")
	require.NoError(t, err)
	require.Equal(t, ImageJobSucceeded, got.Status)
	require.Equal(t, 2, got.ImageCount)
	require.Equal(t, int64(16_800), got.CostMicro)
	require.Equal(t, int64(8_400), got.UpstreamCostMicro)
	require.Len(t, got.Files, 2)
	require.True(t, got.ExpiresAt.Equal(at.Add(24*time.Hour)))
	require.Equal(t, int64(16_800), keySpend(t, db, "k"))
}

// A usage insert that fails inside the transaction leaves the job running
// with no files and nothing billed.
func TestFinishSucceededBilledUsageFailureRollsBack(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	r := db.ImageJobs()
	runningJob(t, r, "j1")
	// A row with the same id makes the usage insert fail after the job
	// update and file inserts ran.
	require.NoError(t, db.Usage().Record(ctx, imageUsage("dup", "other", 1)))

	err := r.FinishSucceededBilled(ctx, "j1", billedFiles, imageUsage("dup", "k", 16_800), time.Now())
	require.Error(t, err)
	got, err := r.Get(ctx, "A", "j1")
	require.NoError(t, err)
	require.Equal(t, ImageJobRunning, got.Status)
	require.Empty(t, got.Files)
	require.Zero(t, got.CostMicro)
	require.Zero(t, keySpend(t, db, "k"))
}

func TestFinishSucceededBilledNeedsRunningJob(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	r := db.ImageJobs()
	require.NoError(t, r.Create(ctx, newImageJob("queued", "A")))
	require.ErrorIs(t, r.FinishSucceededBilled(ctx, "queued", billedFiles, imageUsage("u1", "k", 100), time.Now()), ErrImageJobNotActive)

	runningJob(t, r, "failed")
	require.NoError(t, r.Finish(ctx, "failed", ImageJobFailed, "x", 0, 0, 0, nil, time.Now()))
	require.ErrorIs(t, r.FinishSucceededBilled(ctx, "failed", billedFiles, imageUsage("u2", "k", 100), time.Now()), ErrImageJobNotActive)
	require.ErrorIs(t, r.FinishSucceededBilled(ctx, "missing", nil, imageUsage("u3", "k", 100), time.Now()), ErrImageJobNotActive)
	require.Zero(t, keySpend(t, db, "k"), "no usage row for a job that did not finish")

	got, err := r.Get(ctx, "A", "failed")
	require.NoError(t, err)
	require.Equal(t, ImageJobFailed, got.Status)
	require.Empty(t, got.Files)
}
