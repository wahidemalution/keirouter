package backupjob

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mydisha/keirouter/backend/internal/awssig"
)

type s3Target struct {
	conf   S3Settings
	prefix string
	client *http.Client
	now    func() time.Time
}

// NewS3 returns an S3 or S3-compatible target. A nil client uses the
// SSRF-safe default.
func NewS3(conf S3Settings, client *http.Client) Target {
	if client == nil {
		client = safeHTTPClient()
	}
	return &s3Target{conf: conf, prefix: NormalizePrefix(conf.Prefix), client: client, now: time.Now}
}

// base is the bucket URL without a trailing slash.
func (t *s3Target) base() string {
	ep := strings.TrimRight(t.conf.Endpoint, "/")
	if ep == "" {
		ep = "https://s3." + t.conf.Region + ".amazonaws.com"
	}
	if t.conf.PathStyle {
		return ep + "/" + awssig.URIEncode(t.conf.Bucket)
	}
	u, err := url.Parse(ep)
	if err != nil {
		return ep
	}
	u.Host = t.conf.Bucket + "." + u.Host
	return strings.TrimRight(u.String(), "/")
}

func encodeKey(key string) string {
	segs := strings.Split(key, "/")
	for i, s := range segs {
		segs[i] = awssig.URIEncode(s)
	}
	return strings.Join(segs, "/")
}

func (t *s3Target) objectURL(name string) string {
	return t.base() + "/" + encodeKey(t.prefix+name)
}

func (t *s3Target) do(ctx context.Context, method, rawURL string, body io.Reader, size int64, payloadHash string, extra map[string]string) (*http.Response, error) {
	headers, err := awssig.Sign(method, rawURL, payloadHash, extra,
		awssig.Credentials{AccessKeyID: t.conf.AccessKeyID, SecretAccessKey: t.conf.SecretAccessKey},
		t.conf.Region, "s3", t.now())
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.ContentLength = size
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("s3: %s: %w", method, err)
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		return nil, s3Error(method, resp)
	}
	return resp, nil
}

func s3Error(method string, resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var e struct {
		Code    string
		Message string
	}
	if xml.Unmarshal(b, &e) == nil && e.Code != "" {
		return fmt.Errorf("s3: %s: %d %s: %s", method, resp.StatusCode, e.Code, e.Message)
	}
	return fmt.Errorf("s3: %s: %d %s", method, resp.StatusCode, http.StatusText(resp.StatusCode))
}

func (t *s3Target) Put(ctx context.Context, name string, r io.Reader, size int64) error {
	return t.PutWithHash(ctx, name, r, size, awssig.UnsignedPayload)
}

func (t *s3Target) PutWithHash(ctx context.Context, name string, r io.Reader, size int64, sha256hex string) error {
	resp, err := t.do(ctx, http.MethodPut, t.objectURL(name), r, size, sha256hex,
		map[string]string{"Content-Type": "application/gzip"})
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.Body.Close()
}

func (t *s3Target) Size(ctx context.Context, name string) (int64, error) {
	resp, err := t.do(ctx, http.MethodHead, t.objectURL(name), nil, 0, awssig.SHA256Hex(nil), nil)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return resp.ContentLength, nil
}

func (t *s3Target) Delete(ctx context.Context, name string) error {
	resp, err := t.do(ctx, http.MethodDelete, t.objectURL(name), nil, 0, awssig.SHA256Hex(nil), nil)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

func (t *s3Target) List(ctx context.Context) ([]string, error) {
	var names []string
	token := ""
	for {
		q := url.Values{"list-type": {"2"}}
		if t.prefix != "" {
			q.Set("prefix", t.prefix)
		}
		if token != "" {
			q.Set("continuation-token", token)
		}
		resp, err := t.do(ctx, http.MethodGet, t.base()+"/?"+q.Encode(), nil, 0, awssig.SHA256Hex(nil), nil)
		if err != nil {
			return nil, err
		}
		var res struct {
			IsTruncated           bool
			NextContinuationToken string
			Contents              []struct{ Key string }
		}
		err = xml.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&res)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("s3: list: %w", err)
		}
		for _, c := range res.Contents {
			rest := strings.TrimPrefix(c.Key, t.prefix)
			if rest != "" && !strings.Contains(rest, "/") {
				names = append(names, rest)
			}
		}
		if !res.IsTruncated || res.NextContinuationToken == "" {
			return names, nil
		}
		token = res.NextContinuationToken
	}
}

func (t *s3Target) Test(ctx context.Context) error {
	name := probeName()
	if err := t.PutWithHash(ctx, name, strings.NewReader(probeBody), int64(len(probeBody)), awssig.SHA256Hex([]byte(probeBody))); err != nil {
		return err
	}
	return t.Delete(ctx, name)
}
