package awssig

import (
	"strings"
	"testing"
	"time"
)

// The GET Object example from the AWS SigV4 documentation ("Example: GET
// Object", Signature Version 4 for Amazon S3).
func TestSignMatchesAWSExample(t *testing.T) {
	now := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
	creds := Credentials{AccessKeyID: "AKIAIOSFODNN7EXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}
	h, err := Sign("GET", "https://examplebucket.s3.amazonaws.com/test.txt", SHA256Hex(nil),
		map[string]string{"Range": "bytes=0-9"}, creds, "us-east-1", "s3", now)
	if err != nil {
		t.Fatal(err)
	}
	want := "Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
	if !strings.HasSuffix(h["Authorization"], want) {
		t.Fatalf("Authorization = %s", h["Authorization"])
	}
	if !strings.Contains(h["Authorization"], "SignedHeaders=host;range;x-amz-content-sha256;x-amz-date") {
		t.Fatalf("signed headers: %s", h["Authorization"])
	}
}

func TestSignUnsignedPayloadHeader(t *testing.T) {
	h, err := Sign("PUT", "https://b.s3.amazonaws.com/k", UnsignedPayload, nil,
		Credentials{AccessKeyID: "AK", SecretAccessKey: "SK"}, "us-east-1", "s3", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if h["X-Amz-Content-Sha256"] != UnsignedPayload {
		t.Fatalf("content sha = %q", h["X-Amz-Content-Sha256"])
	}
}
