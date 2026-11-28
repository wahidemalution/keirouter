package meter

import (
	"context"
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/store"
)

// A non-finite, negative or huge image price never reaches the usage table,
// so no image row can carry a negative or wrapped cost.
func TestRecordRefusesInvalidImageEvent(t *testing.T) {
	cases := map[string]Event{
		"nan bill":      imageEvent(1, math.NaN(), 100),
		"inf bill":      imageEvent(1, math.Inf(1), 100),
		"negative bill": imageEvent(1, -5, 100),
		"huge bill":     imageEvent(1, 1e300, 100),
		"nan cost":      imageEvent(1, 100, math.NaN()),
		"negative cost": imageEvent(1, 100, -1),
		"huge count":    imageEvent(1_000_000, 100, 50),
		"negative n":    imageEvent(-1, 100, 50),
	}
	for name, ev := range cases {
		u := &memUsage{rows: map[string]bool{}}
		m := New(u, nil, nil)
		var stored []store.UsageRecord
		m.SetRecordObserver(func(r store.UsageRecord) { stored = append(stored, r) })
		_, err := m.Record(context.Background(), ev)
		require.Error(t, err, name)
		require.Empty(t, u.rows, name)
		require.Empty(t, stored, name)
		_, _, err = m.BuildRecord(ev)
		require.Error(t, err, name)
	}
}

// BuildRecord yields the row Record writes, and writes nothing itself.
func TestBuildRecordMatchesRecord(t *testing.T) {
	u := &memUsage{rows: map[string]bool{}}
	m := New(u, nil, nil)
	var recorded store.UsageRecord
	m.SetRecordObserver(func(r store.UsageRecord) { recorded = r })
	ev := imageEvent(2, 8400, 4200)

	built, cost, err := m.BuildRecord(ev)
	require.NoError(t, err)
	require.Empty(t, u.rows, "BuildRecord persists nothing")
	require.Zero(t, recorded.ID, "and notifies no one")
	require.Equal(t, int64(16_800), cost)

	_, err = m.Record(context.Background(), ev)
	require.NoError(t, err)
	built.ID, built.CreatedAt, built.PricingAsOf = recorded.ID, recorded.CreatedAt, recorded.PricingAsOf
	require.Equal(t, recorded, built)
	require.Equal(t, "img-1", built.RequestID)
}

// Persisted counts an externally committed row exactly as a synchronous
// Record does: pending, then persisted, then the observers.
func TestPersistedNotifiesTrackersAndObservers(t *testing.T) {
	u := &memUsage{rows: map[string]bool{}}
	m := New(u, nil, nil)
	log := &persistedLog{u: u, pending: map[string]bool{}}
	m.AddUsageTracker(log)
	var observed []string
	m.AddRecordObserver(func(r store.UsageRecord) { observed = append(observed, r.ID) })

	rec, _, err := m.BuildRecord(imageEvent(1, 8400, 4200))
	require.NoError(t, err)
	u.rows[rec.ID] = true // committed by the caller's own transaction
	m.Persisted(rec)
	require.Equal(t, []string{rec.ID}, log.seen)
	require.Empty(t, log.discarded)
	require.Empty(t, log.early)
	require.Zero(t, log.open())
	require.Equal(t, []string{rec.ID}, observed)
}
