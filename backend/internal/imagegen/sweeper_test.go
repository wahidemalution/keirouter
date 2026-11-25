package imagegen

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/store"
)

func TestSweepRemovesFilesAndRows(t *testing.T) {
	e := newEnv(t, nImages)
	e.start()
	key := e.key("k1", priceStd, true)
	job := e.submitWait(input("A", key, 1))
	require.Len(t, job.Files, 1)
	path := job.Files[0].Path
	ctx := context.Background()

	require.NoError(t, e.svc.Sweep(ctx, time.Now().Add(23*time.Hour)))
	_, err := os.Stat(path)
	require.NoError(t, err, "not expired yet")

	require.NoError(t, e.svc.Sweep(ctx, time.Now().Add(25*time.Hour)))
	_, err = os.Stat(path)
	require.True(t, errors.Is(err, os.ErrNotExist))
	_, err = e.db.ImageJobs().Get(ctx, "A", job.ID)
	require.ErrorIs(t, err, store.ErrNotFound)
}

// A path outside the image directory is never removed, even if a row names it.
func TestSweepKeepsForeignPaths(t *testing.T) {
	e := newEnv(t, nImages)
	outside := t.TempDir() + "/keep.txt"
	require.NoError(t, os.WriteFile(outside, []byte("x"), 0o600))
	e.svc.removeFiles([]store.ImageFile{{ID: "f", Path: outside}})
	_, err := os.Stat(outside)
	require.NoError(t, err)
}
