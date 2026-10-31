package connectors

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mydisha/keirouter/backend/internal/core"
)

func TestSendRequestRecordsOneapiRequestID(t *testing.T) {
	const id = "20261031145028472218319QeMWIGBK"
	withHeader := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Oneapi-Request-Id", id)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer withHeader.Close()
	without := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer without.Close()

	t.Run("stream", func(t *testing.T) {
		m := core.NewUpstreamMeta()
		resp, err := openStream(core.WithUpstreamMeta(context.Background(), m), "test", "m", withHeader.URL, []byte(`{}`), nil)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if m.RequestID() != id {
			t.Fatalf("RequestID() = %q, want %q", m.RequestID(), id)
		}
		if !m.Streamed() {
			t.Fatal("openStream must mark the attempt as streamed")
		}
	})
	t.Run("json", func(t *testing.T) {
		m := core.NewUpstreamMeta()
		_, body, err := doJSONDecode(core.WithUpstreamMeta(context.Background(), m), "test", "m", withHeader.URL, []byte(`{}`), nil)
		if err != nil {
			t.Fatal(err)
		}
		body.Close()
		if m.RequestID() != id {
			t.Fatalf("RequestID() = %q, want %q", m.RequestID(), id)
		}
		if m.Streamed() {
			t.Fatal("a JSON response must not be marked streamed")
		}
	})
	t.Run("absent header", func(t *testing.T) {
		m := core.NewUpstreamMeta()
		_, body, err := doJSONDecode(core.WithUpstreamMeta(context.Background(), m), "test", "m", without.URL, []byte(`{}`), nil)
		if err != nil {
			t.Fatal(err)
		}
		body.Close()
		if m.RequestID() != "" {
			t.Fatalf("RequestID() = %q, want empty", m.RequestID())
		}
	})
}
