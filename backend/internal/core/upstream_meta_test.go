package core

import (
	"context"
	"testing"
)

func TestUpstreamMetaRoundTrip(t *testing.T) {
	m := NewUpstreamMeta()
	ctx := WithUpstreamMeta(context.Background(), m)
	UpstreamMetaFrom(ctx).SetRequestID("abc")
	if got := m.RequestID(); got != "abc" {
		t.Fatalf("RequestID() = %q, want abc", got)
	}
	if UpstreamMetaFrom(context.Background()) != nil {
		t.Fatal("UpstreamMetaFrom on a bare context should be nil")
	}
	var nilMeta *UpstreamMeta
	nilMeta.SetRequestID("x")
	if nilMeta.RequestID() != "" {
		t.Fatal("nil holder must read as empty")
	}
}
