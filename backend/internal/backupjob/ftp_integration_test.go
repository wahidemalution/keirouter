package backupjob

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/httputil"
)

// TestFTPIntegration runs against a real server, for example:
//
//	docker run -d -p 2121:21 -p 21000-21010:21000-21010 -e USERS="kr|krpass" -e ADDRESS=127.0.0.1 delfer/alpine-ftp-server
//	KEIROUTER_TEST_FTP=ftp://kr:krpass@127.0.0.1:2121/backups go test ./internal/backupjob/ -run FTP
func TestFTPIntegration(t *testing.T) {
	raw := os.Getenv("KEIROUTER_TEST_FTP")
	if raw == "" {
		t.Skip("KEIROUTER_TEST_FTP is not set")
	}
	u, err := url.Parse(raw)
	require.NoError(t, err)
	port, _ := strconv.Atoi(u.Port())
	pass, _ := u.User.Password()
	httputil.SetAllowPrivateBaseURL(true)
	t.Cleanup(func() { httputil.SetAllowPrivateBaseURL(false) })

	conf := FTPSettings{Host: u.Hostname(), Port: port, TLS: TLSNone, Username: u.User.Username(), Password: pass,
		Folder: strings.TrimPrefix(u.Path, "/") + "/" + strconv.FormatInt(time.Now().UnixNano(), 36)}
	tg := NewFTP(conf)
	ctx := context.Background()

	require.NoError(t, tg.Test(ctx), "Test creates the folder and round-trips a probe")
	names, err := tg.List(ctx)
	require.NoError(t, err)
	require.Empty(t, names, "the probe is deleted")

	name := FileName(time.Now())
	body := bytes.Repeat([]byte("k"), 70_000)
	require.NoError(t, tg.Put(ctx, name, bytes.NewReader(body), int64(len(body))))
	n, err := tg.Size(ctx, name)
	require.NoError(t, err)
	require.EqualValues(t, len(body), n)
	names, err = tg.List(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{name}, names, "no .part file remains after a finished upload")

	// A cancelled upload never shows up under the final name.
	cctx, cancel := context.WithCancel(ctx)
	other := FileName(time.Now().Add(time.Hour))
	slow := NewLimitedReader(cctx, bytes.NewReader(make([]byte, 1<<20)), 64)
	time.AfterFunc(300*time.Millisecond, cancel)
	require.Error(t, tg.Put(cctx, other, slow, 1<<20))
	names, err = tg.List(ctx)
	require.NoError(t, err)
	require.False(t, slices.Contains(names, other), "cancelled upload must not appear as a backup: %v", names)

	require.NoError(t, tg.Delete(ctx, name))
	names, err = tg.List(ctx)
	require.NoError(t, err)
	require.False(t, slices.Contains(names, name))

	// Wrong password fails cleanly.
	bad := conf
	bad.Password = "nope"
	require.Error(t, NewFTP(bad).Test(ctx))
}
