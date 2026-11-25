package imagegen

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/core"
	"github.com/mydisha/keirouter/backend/internal/dispatch"
	"github.com/mydisha/keirouter/backend/internal/pipeline"
	"github.com/mydisha/keirouter/backend/internal/store"
)

// Stopping Run mid-generation aborts the running jobs and fails the queued
// ones; none is billed and every reservation is released.
func TestRunCancelFailsJobsUnbilled(t *testing.T) {
	started := make(chan struct{}, 16)
	unblock := make(chan struct{})
	t.Cleanup(func() { close(unblock) })
	e := newEnv(t, func(ctx context.Context, _ *core.ImageRequest, _ pipeline.MediaOptions) (*core.ImageResponse, dispatch.Target, error) {
		started <- struct{}{}
		select {
		case <-ctx.Done():
			return nil, dispatch.Target{}, ctx.Err()
		case <-unblock:
			return nil, dispatch.Target{}, fmt.Errorf("test ended")
		}
	})
	key := e.key("k1", 100*priceStd, true)
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { e.svc.Run(ctx); close(stopped) }()

	var ids []string
	owners := map[string]string{}
	for i := range workerCount + 2 {
		owner := fmt.Sprintf("owner-%d", i)
		job, err := e.svc.Submit(context.Background(), input(owner, key, 1))
		require.NoError(t, err)
		ids = append(ids, job.ID)
		owners[job.ID] = owner
	}
	for range workerCount {
		select {
		case <-started:
		case <-time.After(10 * time.Second):
			t.Fatal("workers never started generating")
		}
	}

	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
	for _, id := range ids {
		got, err := e.db.ImageJobs().Get(context.Background(), owners[id], id)
		require.NoError(t, err)
		require.Equal(t, store.ImageJobFailed, got.Status, id)
		require.Equal(t, msgShutdown, got.Error)
	}
	require.Zero(t, e.spend("k1"))
	e.requireSettledOnce()
}
