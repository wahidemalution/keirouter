package meter

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/store"
)

// memUsage is a usage table in memory. failBatch makes RecordBatch fail and
// failID makes Record fail for that row.
type memUsage struct {
	mu        sync.Mutex
	rows      map[string]bool
	failBatch bool
	failID    string
	failAll   bool
}

func (m *memUsage) Record(_ context.Context, u store.UsageRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if u.ID == m.failID || m.failAll {
		return errors.New("write failed")
	}
	m.rows[u.ID] = true
	return nil
}

func (m *memUsage) RecordBatch(ctx context.Context, recs []store.UsageRecord) error {
	if m.failBatch {
		return errors.New("batch failed")
	}
	for _, r := range recs {
		if err := m.Record(ctx, r); err != nil {
			return err
		}
	}
	return nil
}

func (m *memUsage) has(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rows[id]
}

// persistedLog is a UsageTracker that checks each row is pending first,
// persisted only once stored, and ends exactly once.
type persistedLog struct {
	u         *memUsage
	mu        sync.Mutex
	pending   map[string]bool
	seen      []string
	discarded []string
	early     []string
}

func newPersistedLog(u *memUsage) *persistedLog {
	return &persistedLog{u: u, pending: map[string]bool{}}
}

func (p *persistedLog) UsagePending(r store.UsageRecord) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pending[r.ID] = true
}

func (p *persistedLog) end(r store.UsageRecord, persisted bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.pending[r.ID] {
		p.early = append(p.early, "not pending: "+r.ID)
	}
	delete(p.pending, r.ID)
	if persisted {
		p.seen = append(p.seen, r.ID)
		if !p.u.has(r.ID) {
			p.early = append(p.early, "not stored: "+r.ID)
		}
	} else {
		p.discarded = append(p.discarded, r.ID)
	}
}

func (p *persistedLog) UsagePersisted(r store.UsageRecord) { p.end(r, true) }
func (p *persistedLog) UsageDiscarded(r store.UsageRecord) { p.end(r, false) }

func (p *persistedLog) ids() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.seen...)
}

func (p *persistedLog) open() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.pending)
}

func TestPersistedObserverSync(t *testing.T) {
	u := &memUsage{rows: map[string]bool{}}
	m := New(u, nil, nil)
	log := newPersistedLog(u)
	m.AddUsageTracker(log)

	_, err := m.Record(context.Background(), Event{RequestID: "r1", TenantID: "t", APIKeyID: "k"})
	require.NoError(t, err)
	require.Len(t, log.ids(), 1)
	require.Empty(t, log.early)
	require.Zero(t, log.open())

	u.failID = "*"
	u.mu.Lock()
	u.failAll = true
	u.mu.Unlock()
	_, err = m.Record(context.Background(), Event{RequestID: "r2", TenantID: "t", APIKeyID: "k"})
	require.Error(t, err)
	require.Len(t, log.discarded, 1, "a failed synchronous write is discarded")
	require.Zero(t, log.open())
}

func TestPersistedObserverAsyncRunsAfterFlush(t *testing.T) {
	u := &memUsage{rows: map[string]bool{}}
	m := New(u, nil, nil)
	m.EnableAsync(AsyncConfig{Enabled: true, BatchSize: 100, FlushInterval: 50 * time.Millisecond, QueueSize: 100})
	log := newPersistedLog(u)
	m.AddUsageTracker(log)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err := m.Record(ctx, Event{RequestID: "r1", TenantID: "t", APIKeyID: "k"})
	require.NoError(t, err)
	require.Empty(t, log.ids(), "queued, not yet written")
	require.Equal(t, 1, log.open(), "pending while queued")

	m.StartAsync(ctx)
	require.Eventually(t, func() bool { return len(log.ids()) == 1 }, 2*time.Second, 5*time.Millisecond)
	require.Empty(t, log.early, "observed only once written")
	require.Zero(t, log.open())
	m.Close(time.Second)
}

func TestPersistedObserverSkipsUnwrittenRows(t *testing.T) {
	u := &memUsage{rows: map[string]bool{}, failBatch: true, failID: "bad"}
	m := New(u, nil, nil)
	m.EnableAsync(AsyncConfig{Enabled: true, BatchSize: 3, FlushInterval: time.Hour, QueueSize: 3, FullQueuePolicy: "drop"})
	log := newPersistedLog(u)
	m.AddUsageTracker(log)
	w := m.async
	ctx := context.Background()

	for _, id := range []string{"a", "bad", "b", "dropped"} {
		rec := store.UsageRecord{ID: id}
		log.UsagePending(rec)
		require.NoError(t, w.Record(ctx, rec))
	}
	w.Start(ctx)
	w.Close(2 * time.Second)

	require.ElementsMatch(t, []string{"a", "b"}, log.ids(), "failed and dropped rows are never persisted")
	require.ElementsMatch(t, []string{"bad", "dropped"}, log.discarded)
	require.Empty(t, log.early)
	require.Zero(t, log.open(), "every row ended exactly once")
	require.EqualValues(t, 1, w.Stats().Dropped)

	// After Close, writes are synchronous: persisted once stored.
	late := store.UsageRecord{ID: "late"}
	log.UsagePending(late)
	require.NoError(t, w.Record(ctx, late))
	require.Contains(t, log.ids(), "late")
	require.Zero(t, log.open())
}
