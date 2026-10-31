package meter

import (
	"context"
	"testing"

	"github.com/mydisha/keirouter/backend/internal/core"
)

func costLookupEvent() Event {
	return Event{
		TenantID: "default", Provider: "custom-openai-subrouter", Model: "deepseek-v4.1-flash",
		Status: "success", CostLookup: true, Streamed: true, UpstreamRequestID: "rid",
		StaticPrice: true, MarketRateIn: 0.03, MarketRateOut: 0.12,
		UpstreamRateIn: 0.02, UpstreamRateOut: 0.08,
		Usage: core.Usage{PromptTokens: 100, CompletionTokens: 10},
	}
}

func recordOne(t *testing.T, ev Event) (rowStatus, rowID string) {
	t.Helper()
	cs := &captureStore{}
	if _, err := New(cs, nil, nil).Record(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if len(cs.rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(cs.rows))
	}
	return cs.rows[0].ReconcileStatus, cs.rows[0].UpstreamRequestID
}

func TestRecordMarksCostLookupPending(t *testing.T) {
	status, id := recordOne(t, costLookupEvent())
	if status != "pending" || id != "rid" {
		t.Fatalf("got status %q id %q, want pending rid", status, id)
	}
}

func TestRecordDropsNonStreamUpstreamID(t *testing.T) {
	ev := costLookupEvent()
	ev.Streamed = false
	status, id := recordOne(t, ev)
	if status != "pending" || id != "" {
		t.Fatalf("got status %q id %q, want pending and no id", status, id)
	}
}

func TestRecordNoPendingWhen(t *testing.T) {
	cases := map[string]func(*Event){
		"cost lookup off":             func(e *Event) { e.CostLookup = false },
		"zero tokens, no streamed id": func(e *Event) { e.Usage = core.Usage{}; e.Streamed = false; e.UpstreamRequestID = "" },
		"error, no tokens, no id": func(e *Event) {
			e.Status = "error"
			e.ErrorKind = "upstream"
			e.Usage = core.Usage{}
			e.Streamed = false
		},
		"cache hit": func(e *Event) { e.CacheHit = true; e.Status = "" },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			ev := costLookupEvent()
			mut(&ev)
			if status, _ := recordOne(t, ev); status != "" {
				t.Fatalf("status = %q, want empty", status)
			}
		})
	}
}

// A stream the client cancelled (or that failed) after Subrouter started
// generating is still charged by Subrouter; its row must be reconciled too.
func TestRecordCancelledStreamPending(t *testing.T) {
	for _, status := range []string{"client_cancelled", "error"} {
		t.Run(status, func(t *testing.T) {
			ev := costLookupEvent()
			ev.Status = status
			ev.Usage = core.Usage{}
			got, id := recordOne(t, ev)
			if got != "pending" || id != "rid" {
				t.Fatalf("got status %q id %q, want pending rid", got, id)
			}
		})
	}
	ev := costLookupEvent()
	ev.Status = "error"
	ev.Streamed = false
	if got, _ := recordOne(t, ev); got != "pending" {
		t.Fatalf("a failed row with tokens must be pending, got %q", got)
	}
}
