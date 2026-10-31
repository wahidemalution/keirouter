package core

import (
	"context"
	"sync"
)

// UpstreamMeta collects facts about one upstream attempt that only the HTTP
// layer sees, such as the request id a new-api upstream (Subrouter) returns
// in X-Oneapi-Request-Id. The pipeline creates one per attempt and the
// connector fills it. A nil holder is valid and ignores writes.
type UpstreamMeta struct {
	mu        sync.Mutex
	requestID string
	streamed  bool
}

// NewUpstreamMeta returns an empty holder.
func NewUpstreamMeta() *UpstreamMeta { return &UpstreamMeta{} }

// SetRequestID records the upstream's request id.
func (m *UpstreamMeta) SetRequestID(id string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.requestID = id
	m.mu.Unlock()
}

// RequestID returns the recorded upstream request id, "" when none.
func (m *UpstreamMeta) RequestID() string {
	if m == nil {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.requestID
}

// SetStreamed records that the upstream answered with an event stream.
func (m *UpstreamMeta) SetStreamed() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.streamed = true
	m.mu.Unlock()
}

// Streamed reports whether the upstream answered with an event stream. For
// Subrouter only a streamed response's request id matches its usage log.
func (m *UpstreamMeta) Streamed() bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.streamed
}

type upstreamMetaKey struct{}

// WithUpstreamMeta attaches m to ctx for the connector to fill.
func WithUpstreamMeta(ctx context.Context, m *UpstreamMeta) context.Context {
	return context.WithValue(ctx, upstreamMetaKey{}, m)
}

// UpstreamMetaFrom returns the holder attached to ctx, or nil.
func UpstreamMetaFrom(ctx context.Context) *UpstreamMeta {
	m, _ := ctx.Value(upstreamMetaKey{}).(*UpstreamMeta)
	return m
}
