package imagegen

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/core"
	"github.com/mydisha/keirouter/backend/internal/dispatch"
	"github.com/mydisha/keirouter/backend/internal/meter"
	"github.com/mydisha/keirouter/backend/internal/pipeline"
	"github.com/mydisha/keirouter/backend/internal/store"
)

// brokenBillingJobs never manages to record a succeeded, billed job.
type brokenBillingJobs struct {
	*store.ImageJobsRepo
}

func (f *brokenBillingJobs) FinishSucceededBilled(context.Context, string, []store.ImageFile, store.UsageRecord, time.Time) error {
	return errors.New("database is locked")
}

// A job whose billed finish never commits is failed and never billed: the
// usage row and the succeeded state are one transaction.
func TestBillingCommitFailureNeverBills(t *testing.T) {
	old := finishBackoff
	finishBackoff = time.Millisecond
	t.Cleanup(func() { finishBackoff = old })

	e := newEnv(t, nImages)
	e.svc.d.Jobs = &brokenBillingJobs{ImageJobsRepo: e.db.ImageJobs()}
	e.start()
	key := e.key("k1", priceStd, true)
	job := e.submitWait(input("A", key, 1))
	require.Equal(t, store.ImageJobFailed, job.Status)
	require.Empty(t, job.Files)
	require.Empty(t, e.ownerFiles("A"), "unbilled files are removed")
	require.Zero(t, e.spend("k1"), "a job shown as failed is never charged")
	e.requireSettledOnce()
}

// dupIDMeter gives every image row an id that already exists in the usage
// table, so the usage insert fails inside the billed-finish transaction.
type dupIDMeter struct{ *meter.Meter }

func (m dupIDMeter) BuildRecord(ev meter.Event) (store.UsageRecord, int64, error) {
	rec, cost, err := m.Meter.BuildRecord(ev)
	rec.ID = "dup"
	return rec, cost, err
}

// recordingMeter keeps the rows it built.
type recordingMeter struct {
	*meter.Meter
	mu   sync.Mutex
	rows []store.UsageRecord
}

func (m *recordingMeter) BuildRecord(ev meter.Event) (store.UsageRecord, int64, error) {
	rec, cost, err := m.Meter.BuildRecord(ev)
	m.mu.Lock()
	m.rows = append(m.rows, rec)
	m.mu.Unlock()
	return rec, cost, err
}

// The image usage row carries the generation latency and the job's
// request id.
func TestImageUsageRowCarriesLatency(t *testing.T) {
	e := newEnv(t, func(ctx context.Context, req *core.ImageRequest, opts pipeline.MediaOptions) (*core.ImageResponse, dispatch.Target, error) {
		time.Sleep(30 * time.Millisecond)
		return nImages(ctx, req, opts)
	})
	m := &recordingMeter{Meter: meter.New(e.db.Usage(), nil, nil)}
	e.svc.d.Meter = m
	e.start()
	key := e.key("k1", priceStd, true)
	job := e.submitWait(input("A", key, 1))
	require.Equal(t, store.ImageJobSucceeded, job.Status)
	m.mu.Lock()
	defer m.mu.Unlock()
	require.Len(t, m.rows, 1)
	require.GreaterOrEqual(t, m.rows[0].UpstreamLatencyMS, 30)
	require.Equal(t, "img-"+job.ID, m.rows[0].RequestID)
	require.Equal(t, 1, m.rows[0].ImageCount)
}

func TestUsageInsertFailureInsideTxNeverBills(t *testing.T) {
	old := finishBackoff
	finishBackoff = time.Millisecond
	t.Cleanup(func() { finishBackoff = old })

	e := newEnv(t, nImages)
	m := meter.New(e.db.Usage(), nil, nil)
	m.AddUsageTracker(e.bud.Engine)
	e.svc.d.Meter = dupIDMeter{m}
	require.NoError(t, e.db.Usage().Record(context.Background(), store.UsageRecord{
		ID: "dup", TenantID: store.DefaultTenantID, APIKeyID: "someone-else", CreatedAt: time.Now(),
	}))
	e.start()
	key := e.key("k1", 2*priceStd, true)
	job := e.submitWait(input("A", key, 2))
	require.Equal(t, store.ImageJobFailed, job.Status, "the job never became succeeded")
	require.Equal(t, msgBillingFail, job.Error)
	require.Empty(t, job.Files)
	require.Empty(t, e.ownerFiles("A"))
	require.Zero(t, e.spend("k1"))
	e.requireSettledOnce()
}
