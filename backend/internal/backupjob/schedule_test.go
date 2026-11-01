package backupjob

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNextDue(t *testing.T) {
	T := time.Date(2026, 11, 1, 14, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	require.True(t, NextDue(State{}, day).IsZero(), "never run: due now")
	require.Equal(t, T.Add(day), NextDue(State{LastSuccess: T, LastAttempt: T}, day))
	require.Equal(t, T.Add(30*time.Minute), NextDue(State{LastSuccess: T.Add(-day), LastAttempt: T, LastFailed: true}, day))
	require.Equal(t, T.Add(30*time.Minute), NextDue(State{LastAttempt: T, LastFailed: true}, time.Hour))
	require.Equal(t, T.Add(2*time.Hour).Add(day), NextDue(State{LastSuccess: T.Add(2 * time.Hour), LastAttempt: T.Add(2 * time.Hour)}, day),
		"a success after a failure clears the retry")
}

func TestFileNameRoundTrip(t *testing.T) {
	name := FileName(time.Date(2026, 11, 1, 14, 0, 5, 0, time.FixedZone("WIB", 7*3600)))
	require.Equal(t, "keirouter-backup-20261101-070005.json.gz", name)
	require.True(t, IsBackupName(name))
	for _, bad := range []string{"keirouter-backup-latest.json.gz", name + ".part", "x" + name, "keirouter-backup-20261101-070005.json", "other/" + name} {
		require.False(t, IsBackupName(bad), bad)
	}
}

// Review Focus 2: rotation only ever deletes this feature's own files.
func TestSelectForDeletion(t *testing.T) {
	var names []string
	for d := 9; d >= 1; d-- {
		names = append(names, FileName(time.Date(2026, 10, d, 0, 0, 0, 0, time.UTC)))
	}
	names = append(names, "keirouter-backup-latest.json.gz", FileName(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))+".part", "notes.txt")
	got := SelectForDeletion(names, 7)
	require.Equal(t, []string{
		"keirouter-backup-20261001-000000.json.gz",
		"keirouter-backup-20261002-000000.json.gz",
	}, got)
	require.Empty(t, SelectForDeletion(names[:3], 7))
}

// Review Focus 3: a slow upload of a large file must get enough time.
func TestUploadTimeout(t *testing.T) {
	require.Equal(t, 2*time.Hour, UploadTimeout(5<<30, 0))
	require.Equal(t, 10*time.Minute, UploadTimeout(100<<20, 10_000))
	want := time.Duration(float64(2<<30) / float64(64*1024) * 1.5 * float64(time.Second))
	require.Equal(t, want, UploadTimeout(2<<30, 64))
	require.Greater(t, UploadTimeout(2<<30, 64), 13*time.Hour)
}

func TestLimitedReaderRate(t *testing.T) {
	src := bytes.Repeat([]byte("x"), 256<<10)
	start := time.Now()
	n, err := io.Copy(io.Discard, NewLimitedReader(context.Background(), bytes.NewReader(src), 128))
	elapsed := time.Since(start)
	require.NoError(t, err)
	require.EqualValues(t, len(src), n)
	require.GreaterOrEqual(t, elapsed, 1700*time.Millisecond)
	require.LessOrEqual(t, elapsed, 2600*time.Millisecond)
}

func TestLimitedReaderUnlimitedIsPassThrough(t *testing.T) {
	r := bytes.NewReader([]byte("abc"))
	require.Same(t, r, NewLimitedReader(context.Background(), r, 0))
}

func TestLimitedReaderStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	_, err := io.Copy(io.Discard, NewLimitedReader(ctx, bytes.NewReader(make([]byte, 1<<20)), 64))
	require.True(t, errors.Is(err, context.Canceled), "err = %v", err)
}
