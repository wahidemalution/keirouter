// Package awssig signs HTTP requests with AWS Signature Version 4. It is
// shared by the Bedrock/SageMaker connectors and the S3 backup target.
package awssig

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"sort"
	"strings"
	"time"
)

// UnsignedPayload is the payload hash S3 accepts when the body is streamed
// without hashing it up front.
const UnsignedPayload = "UNSIGNED-PAYLOAD"

// Credentials is one AWS identity used to sign a request.
type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

// SHA256Hex returns the lowercase hex SHA-256 of b.
func SHA256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Sign returns headers plus X-Amz-Date, X-Amz-Content-Sha256 (S3 only),
// X-Amz-Security-Token (when present) and Authorization. It signs host and
// every header passed in, so content negotiation cannot be altered in flight.
func Sign(method, rawURL, payloadHash string, headers map[string]string, c Credentials, region, service string, now time.Time) (map[string]string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	now = now.UTC()
	amzDate := now.Format("20060102T150405Z")
	date := now.Format("20060102")

	out := make(map[string]string, len(headers)+4)
	for k, v := range headers {
		out[k] = v
	}
	out["X-Amz-Date"] = amzDate
	if service == "s3" {
		out["X-Amz-Content-Sha256"] = payloadHash
	}
	if c.SessionToken != "" {
		out["X-Amz-Security-Token"] = c.SessionToken
	}

	canon := make(map[string]string, len(out)+1)
	for k, v := range out {
		canon[strings.ToLower(k)] = strings.Join(strings.Fields(v), " ")
	}
	canon["host"] = u.Host
	names := make([]string, 0, len(canon))
	for k := range canon {
		names = append(names, k)
	}
	sort.Strings(names)
	var ch strings.Builder
	for _, k := range names {
		ch.WriteString(k)
		ch.WriteByte(':')
		ch.WriteString(canon[k])
		ch.WriteByte('\n')
	}
	signedHeaders := strings.Join(names, ";")

	canonicalRequest := strings.Join([]string{
		method,
		canonicalURI(u, service),
		canonicalQuery(u),
		ch.String(),
		signedHeaders,
		payloadHash,
	}, "\n")

	scope := date + "/" + region + "/" + service + "/aws4_request"
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + SHA256Hex([]byte(canonicalRequest))

	kDate := hmacSHA256([]byte("AWS4"+c.SecretAccessKey), date)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	kSigning := hmacSHA256(kService, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(kSigning, stringToSign))

	out["Authorization"] = "AWS4-HMAC-SHA256 Credential=" + c.AccessKeyID + "/" + scope +
		", SignedHeaders=" + signedHeaders + ", Signature=" + signature
	return out, nil
}

// canonicalURI URI-encodes each path segment of the already-escaped path a
// second time, as every service except S3 expects.
func canonicalURI(u *url.URL, service string) string {
	p := u.EscapedPath()
	if p == "" {
		return "/"
	}
	if service == "s3" {
		return p
	}
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = URIEncode(s)
	}
	return strings.Join(segs, "/")
}

func canonicalQuery(u *url.URL) string {
	q := u.Query()
	if len(q) == 0 {
		return ""
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vals := append([]string(nil), q[k]...)
		sort.Strings(vals)
		for _, v := range vals {
			parts = append(parts, URIEncode(k)+"="+URIEncode(v))
		}
	}
	return strings.Join(parts, "&")
}

// URIEncode percent-encodes everything except RFC 3986 unreserved bytes.
func URIEncode(s string) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexDigits[c>>4])
		b.WriteByte(hexDigits[c&15])
	}
	return b.String()
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}
