package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/imagegen"
)

// flakyJobs fails FailStale a set number of times; no other method is used.
type flakyJobs struct {
	imagegen.Jobs
	failures, calls int
}

func (f *flakyJobs) FailStale(context.Context, string) (int, error) {
	f.calls++
	if f.calls <= f.failures {
		return 0, errors.New("database is locked")
	}
	return 0, nil
}

func TestNewImageGenRecoverRetries(t *testing.T) {
	old := recoverBackoff
	recoverBackoff = time.Millisecond
	t.Cleanup(func() { recoverBackoff = old })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	jobs := &flakyJobs{failures: 2}
	require.NotNil(t, newImageGen(context.Background(), imagegen.Deps{Jobs: jobs}, log), "recovers on the third attempt")
	require.Equal(t, 3, jobs.calls)

	jobs = &flakyJobs{failures: 3}
	require.Nil(t, newImageGen(context.Background(), imagegen.Deps{Jobs: jobs}, log), "still failing: images stay off")
	require.Equal(t, 3, jobs.calls)
}
