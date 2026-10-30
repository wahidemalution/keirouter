package connectors

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mydisha/keirouter/backend/internal/core"
)

func TestSendRequestReportsResponseHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Si-Provider-Family", "inferhub")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	for _, headerBudget := range []bool{false, true} {
		var got string
		ctx := core.WithResponseHeaderObserver(context.Background(), func(h http.Header) {
			got = h.Get("X-Si-Provider-Family")
		})
		if headerBudget {
			ctx = core.WithResponseHeaderTimeout(ctx, 5*time.Second)
		} else {
			// A deadline and no header budget takes the plain client.Do path.
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
		}
		resp, err := sendRequest(ctx, "p", "m", jsonRequest(ctx, http.MethodPost, srv.URL, []byte(`{}`), nil))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if got != "inferhub" {
			t.Fatalf("header budget %v: observed %q, want inferhub", headerBudget, got)
		}
	}
}
