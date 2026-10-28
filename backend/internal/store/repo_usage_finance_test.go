package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func finRec(id, provider, chain string, cost, upstream int64, backfilled bool, at time.Time) UsageRecord {
	return UsageRecord{ID: id, TenantID: DefaultTenantID, APIKeyID: "k-" + id, Provider: provider, ChainID: chain,
		Model: "m", Status: "success", CostNanos: cost, UpstreamCostNanos: upstream,
		PricingStatus: "priced", PricingSource: "official", PricingBackfilled: backfilled, CreatedAt: at}
}

func TestCostBasisGroupsByProviderChain(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	since := now.Add(-time.Hour)
	require.NoError(t, db.Usage().RecordBatch(ctx, []UsageRecord{
		finRec("a", "p1", "c1", 15e9, 10e9, false, now),
		finRec("b", "p1", "c1", 3e9, 0, false, now),
		finRec("c", "p1", "c1", 7e9, 5e9, true, now),
		finRec("d", "p1", "c1", 9e9, 9e9, false, now.Add(-2*time.Hour)),
	}))
	rows, err := db.Usage().CostBasis(ctx, DefaultTenantID, since)
	require.NoError(t, err)
	require.Equal(t, []CostBasisRow{{Provider: "p1", ChainID: "c1", Requests: 2,
		BilledNanos: 18e9, UpstreamNanos: 10e9, UnknownBilledNanos: 3e9}}, rows)
}

func TestCostBasisSeriesBucketsByZoneDay(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wib := int64(7 * 3600)
	// 2026-10-10 18:30 UTC is 01:30 on Oct 11 in WIB, so it lands in the next WIB day.
	day := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	require.NoError(t, db.Usage().RecordBatch(ctx, []UsageRecord{
		finRec("a", "p1", "c1", 15e9, 10e9, false, day.Add(10*time.Hour)),
		finRec("b", "p1", "c1", 3e9, 0, false, day.Add(16*time.Hour+59*time.Minute)),
		finRec("c", "p1", "c1", 4e9, 2e9, false, day.Add(18*time.Hour+30*time.Minute)),
		finRec("d", "p1", "c1", 9e9, 9e9, true, day.Add(11*time.Hour)),
	}))
	pts, err := db.Usage().CostBasisSeries(ctx, DefaultTenantID, day, 86400, wib)
	require.NoError(t, err)
	first := (day.Unix() + wib) / 86400
	require.Equal(t, []CostBasisPoint{
		{Bucket: first, CostBasisRow: CostBasisRow{ChainID: "c1", Requests: 2, BilledNanos: 18e9, UpstreamNanos: 10e9, UnknownBilledNanos: 3e9}},
		{Bucket: first + 1, CostBasisRow: CostBasisRow{ChainID: "c1", Requests: 1, BilledNanos: 4e9, UpstreamNanos: 2e9}},
	}, pts)
}

func TestPaidTotalsSince(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	mk := func(id string, st PaymentOrderStatus, paidAt string, created time.Time) {
		o := sampleOrder(id)
		o.Status, o.PaidAt, o.CreatedAt, o.UpdatedAt = st, paidAt, created, created
		require.NoError(t, db.PaymentOrders().Create(ctx, o))
	}
	old := now.Add(-48 * time.Hour)
	mk("o1", PaymentCompleted, formatTime(now), old)
	mk("o2", PaymentManual, "", now)
	mk("o3", PaymentPending, "", now)
	mk("o4", PaymentCompleted, formatTime(old), old)
	// A paid hosting order buys a service, not credit, so it is not counted.
	h := sampleOrder("o5")
	h.Status, h.PaidAt, h.Product, h.CreditMicros = PaymentCompleted, formatTime(now), ProductHosting, 0
	h.CreatedAt, h.UpdatedAt = now, now
	require.NoError(t, db.PaymentOrders().Create(ctx, h))
	got, err := db.PaymentOrders().PaidTotalsSince(ctx, now.Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, PaidOrderTotals{Count: 2, AmountIDR: 100000, CreditMicros: 6_200_000}, got)
}

func TestOutstandingPortalCredit(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, id := range []string{"ka", "kb"} {
		require.NoError(t, db.APIKeys().Create(ctx, APIKey{ID: id, TenantID: DefaultTenantID, Name: id,
			KeyHash: "h" + id, LookupHash: "l" + id, Display: "d", CreatedAt: now}))
		require.NoError(t, db.PortalUsers().Upsert(ctx, PortalUser{GoogleSub: "s" + id, Email: id + "@x.io",
			KeyID: id, CreatedAt: now, UpdatedAt: now}))
		require.NoError(t, db.Budgets().Create(ctx, Budget{ID: "b" + id, TenantID: DefaultTenantID,
			ScopeKind: ScopeAPIKey, ScopeID: id, LimitMicros: 10_000_000, Period: "total",
			AlertPct: 80, HardCutoff: true, CreatedAt: now, UpdatedAt: now}))
	}
	// ka spends 4e6 micros (4e9 nanos); kb overspends (12e9 nanos).
	ra := finRec("x", "p", "c", 4e9, 0, false, now)
	ra.APIKeyID = "ka"
	rb := finRec("y", "p", "c", 12e9, 0, false, now)
	rb.APIKeyID = "kb"
	require.NoError(t, db.Usage().RecordBatch(ctx, []UsageRecord{ra, rb}))
	got, err := db.Usage().OutstandingPortalCredit(ctx, DefaultTenantID)
	require.NoError(t, err)
	require.Equal(t, int64(6_000_000), got)
}
