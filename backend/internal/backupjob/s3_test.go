package backupjob

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeS3 is an in-memory S3 that understands path-style and virtual-host
// requests, ListObjectsV2 paging (2 keys per page) and records each request.
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte // "bucket/key"
	reqs    []*http.Request
	deny    bool
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, r.Clone(context.Background()))
	if f.deny {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `<?xml version="1.0"?><Error><Code>AccessDenied</Code><Message>Access Denied</Message></Error>`)
		return
	}
	host, _, _ := net.SplitHostPort(r.Host)
	if host == "" {
		host = r.Host
	}
	path := strings.TrimPrefix(r.URL.EscapedPath(), "/")
	var bucket, key string
	if i := strings.Index(host, ".fake"); i > 0 { // virtual host: <bucket>.fake
		bucket, key = host[:i], path
	} else {
		bucket, key, _ = strings.Cut(path, "/")
	}
	full := bucket + "/" + key
	switch {
	case r.Method == http.MethodPut:
		b, _ := io.ReadAll(r.Body)
		f.objects[full] = b
	case r.Method == http.MethodHead:
		b, ok := f.objects[full]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	case r.Method == http.MethodDelete:
		delete(f.objects, full)
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2":
		prefix := r.URL.Query().Get("prefix")
		var keys []string
		for k := range f.objects {
			if b, rest, _ := strings.Cut(k, "/"); b == bucket && strings.HasPrefix(rest, prefix) {
				keys = append(keys, rest)
			}
		}
		sort.Strings(keys)
		start := 0
		if tok := r.URL.Query().Get("continuation-token"); tok != "" {
			start, _ = strconv.Atoi(tok)
		}
		end := min(start+2, len(keys))
		type content struct{ Key string }
		res := struct {
			XMLName               xml.Name `xml:"ListBucketResult"`
			IsTruncated           bool
			NextContinuationToken string    `xml:",omitempty"`
			Contents              []content `xml:"Contents"`
		}{IsTruncated: end < len(keys)}
		if res.IsTruncated {
			res.NextContinuationToken = strconv.Itoa(end)
		}
		for _, k := range keys[start:end] {
			res.Contents = append(res.Contents, content{Key: k})
		}
		_ = xml.NewEncoder(w).Encode(res)
	default:
		w.WriteHeader(http.StatusBadRequest)
	}
}

func newFakeS3(t *testing.T) (*fakeS3, *httptest.Server, *http.Client) {
	t.Helper()
	f := &fakeS3{objects: map[string][]byte{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	addr := srv.Listener.Addr().String()
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}}
	return f, srv, client
}

func s3Conf(pathStyle bool) S3Settings {
	return S3Settings{Endpoint: "http://fake:9000", Region: "us-east-1", Bucket: "kr", Prefix: "/backups",
		AccessKeyID: "AK", SecretAccessKey: "SK", PathStyle: pathStyle}
}

func TestS3PutSizeListDelete(t *testing.T) {
	_, _, client := newFakeS3(t)
	ctx := context.Background()
	tg := NewS3(s3Conf(true), client)
	body := []byte("backup-bytes")
	require.NoError(t, tg.Put(ctx, "keirouter-backup-20261101-000000.json.gz", bytes.NewReader(body), int64(len(body))))
	n, err := tg.Size(ctx, "keirouter-backup-20261101-000000.json.gz")
	require.NoError(t, err)
	require.EqualValues(t, len(body), n)
	names, err := tg.List(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"keirouter-backup-20261101-000000.json.gz"}, names)
	require.NoError(t, tg.Delete(ctx, "keirouter-backup-20261101-000000.json.gz"))
	names, err = tg.List(ctx)
	require.NoError(t, err)
	require.Empty(t, names)
}

func TestS3SignsRequests(t *testing.T) {
	f, _, client := newFakeS3(t)
	ctx := context.Background()
	tg := NewS3(s3Conf(true), client)
	require.NoError(t, tg.Put(ctx, "a.json.gz", strings.NewReader("x"), 1))
	_, _ = tg.List(ctx)
	require.NotEmpty(t, f.reqs)
	for _, r := range f.reqs {
		require.True(t, strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=AK/"), r.Header.Get("Authorization"))
		require.NotEmpty(t, r.Header.Get("X-Amz-Content-Sha256"))
	}
}

func TestS3PutWithHashSignsPayload(t *testing.T) {
	f, _, client := newFakeS3(t)
	tg := NewS3(s3Conf(true), client).(PutHasher)
	const sum = "2d711642b726b04401627ca9fbac32f5c8530fb1903cc4db02258717921a4881"
	require.NoError(t, tg.PutWithHash(context.Background(), "a.json.gz", strings.NewReader("x"), 1, sum))
	require.Equal(t, sum, f.reqs[0].Header.Get("X-Amz-Content-Sha256"))
}

// Review Focus 5: prefix input variants map to exactly one slash.
func TestS3PathStyleVsVirtualHost(t *testing.T) {
	f, _, client := newFakeS3(t)
	ctx := context.Background()
	require.NoError(t, NewS3(s3Conf(true), client).Put(ctx, "a.json.gz", strings.NewReader("x"), 1))
	require.Equal(t, "fake:9000", f.reqs[0].Host)
	require.Equal(t, "/kr/backups/a.json.gz", f.reqs[0].URL.Path)

	virt := s3Conf(false)
	virt.Prefix = "backups/"
	require.NoError(t, NewS3(virt, client).Put(ctx, "b.json.gz", strings.NewReader("x"), 1))
	require.Equal(t, "kr.fake:9000", f.reqs[1].Host)
	require.Equal(t, "/backups/b.json.gz", f.reqs[1].URL.Path)

	root := s3Conf(true)
	root.Prefix = ""
	require.NoError(t, NewS3(root, client).Put(ctx, "c.json.gz", strings.NewReader("x"), 1))
	require.Equal(t, "/kr/c.json.gz", f.reqs[2].URL.Path)
}

func TestS3DefaultEndpointIsAWS(t *testing.T) {
	conf := s3Conf(false)
	conf.Endpoint, conf.Region = "", "ap-southeast-3"
	got := NewS3(conf, nil).(*s3Target).objectURL("a.json.gz")
	require.Equal(t, "https://kr.s3.ap-southeast-3.amazonaws.com/backups/a.json.gz", got)
}

func TestS3ListPagesAndPrefix(t *testing.T) {
	f, _, client := newFakeS3(t)
	for i := 0; i < 5; i++ {
		f.objects[fmt.Sprintf("kr/backups/keirouter-backup-2026110%d-000000.json.gz", i)] = []byte("x")
	}
	f.objects["kr/other/keirouter-backup-20200101-000000.json.gz"] = []byte("x")
	f.objects["kr/backups/nested/keirouter-backup-20200101-000000.json.gz"] = []byte("x")
	names, err := NewS3(s3Conf(true), client).List(context.Background())
	require.NoError(t, err)
	require.Len(t, names, 5)
	for _, n := range names {
		require.False(t, strings.Contains(n, "/"), n)
	}
}

func TestS3TestProbe(t *testing.T) {
	f, _, client := newFakeS3(t)
	require.NoError(t, NewS3(s3Conf(true), client).Test(context.Background()))
	require.Empty(t, f.objects)
	require.Contains(t, f.reqs[0].URL.Path, "/kr/backups/keirouter-backup-probe-")
	require.Equal(t, http.MethodPut, f.reqs[0].Method)
}

func TestS3ErrorBody(t *testing.T) {
	f, _, client := newFakeS3(t)
	f.deny = true
	err := NewS3(s3Conf(true), client).Put(context.Background(), "a.json.gz", strings.NewReader("x"), 1)
	require.ErrorContains(t, err, "AccessDenied")
	require.ErrorContains(t, err, "403")
}
