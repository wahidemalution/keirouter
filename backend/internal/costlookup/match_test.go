package costlookup

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/store"
)

var t0 = time.Unix(1793458259, 0).UTC()

func row(id, upstreamID string, at time.Time) store.ReconcileRow {
	return store.ReconcileRow{ID: id, Model: "deepseek-v4.1-flash", UpstreamRequestID: upstreamID,
		PromptTokens: 872, CompletionTokens: 515, CreatedAt: at}
}

func item(requestID string, at time.Time) LogItem {
	return LogItem{CreatedAt: at.Unix(), ModelName: "deepseek-v4.1-flash", PromptTokens: 872,
		CompletionTokens: 515, Quota: 17, RequestID: requestID}
}

func TestMatchByRequestIDFirst(t *testing.T) {
	rows := []store.ReconcileRow{row("r1", "X", t0)}
	items := []LogItem{item("Y", t0.Add(time.Second)), item("X", t0.Add(8*time.Second))}

	pairs, unmatched := Match(rows, items)
	require.Len(t, pairs, 1)
	require.Equal(t, "r1", pairs[0].Row.ID)
	require.Equal(t, "X", pairs[0].Item.RequestID)
	require.Equal(t, []LogItem{items[0]}, unmatched)
}

func TestMatchRowWithIDNeverUsesFingerprint(t *testing.T) {
	rows := []store.ReconcileRow{row("r1", "X", t0)}
	items := []LogItem{item("Y", t0)}

	pairs, unmatched := Match(rows, items)
	require.Empty(t, pairs, "a row whose id is not in the page waits for its own entry")
	require.Len(t, unmatched, 1)
}

func TestMatchFingerprintNearestAndOnce(t *testing.T) {
	rows := []store.ReconcileRow{row("r1", "", t0), row("r2", "", t0)}
	other := item("M", t0)
	other.ModelName = "glm-5.3-flash"
	items := []LogItem{item("C", t0.Add(11*time.Second)), item("B", t0.Add(5*time.Second)), other, item("A", t0.Add(2*time.Second))}

	pairs, unmatched := Match(rows, items)
	require.Len(t, pairs, 2)
	require.Equal(t, "r1", pairs[0].Row.ID)
	require.Equal(t, "A", pairs[0].Item.RequestID)
	require.Equal(t, "r2", pairs[1].Row.ID)
	require.Equal(t, "B", pairs[1].Item.RequestID)
	got := map[string]bool{}
	for _, u := range unmatched {
		got[u.RequestID] = true
	}
	require.Equal(t, map[string]bool{"C": true, "M": true}, got)
}

func TestMatchFingerprintNeedsEqualTokens(t *testing.T) {
	rows := []store.ReconcileRow{row("r1", "", t0)}
	it := item("A", t0)
	it.CompletionTokens = 514
	pairs, _ := Match(rows, []LogItem{it})
	require.Empty(t, pairs)
}
