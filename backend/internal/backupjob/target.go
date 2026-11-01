package backupjob

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/mydisha/keirouter/backend/internal/httputil"
)

// Target is an offsite destination. Names are bare file names; the target
// places them under its own folder or prefix.
type Target interface {
	Put(ctx context.Context, name string, r io.Reader, size int64) error
	Size(ctx context.Context, name string) (int64, error)
	// List returns the file names directly under the folder or prefix.
	List(ctx context.Context) ([]string, error)
	Delete(ctx context.Context, name string) error
	// Test proves the credentials can write and delete under the folder.
	Test(ctx context.Context) error
}

// PutHasher is a Target that can sign the upload with the file's SHA-256.
type PutHasher interface {
	PutWithHash(ctx context.Context, name string, r io.Reader, size int64, sha256hex string) error
}

// NewTarget builds the target the settings point at.
func NewTarget(s Settings) (Target, error) {
	switch s.Destination {
	case DestS3:
		return NewS3(s.S3, safeHTTPClient()), nil
	case DestFTP:
		return NewFTP(s.FTP), nil
	}
	return nil, fmt.Errorf("backupjob: unknown destination %q", s.Destination)
}

func safeHTTPClient() *http.Client {
	return &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           httputil.SafeDialer(30 * time.Second).DialContext,
		TLSHandshakeTimeout:   30 * time.Second,
		ResponseHeaderTimeout: 5 * time.Minute,
		ForceAttemptHTTP2:     true,
	}}
}

func probeName() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "keirouter-backup-probe-" + hex.EncodeToString(b) + ".txt"
}

const probeBody = "keirouter backup connection test\n"
