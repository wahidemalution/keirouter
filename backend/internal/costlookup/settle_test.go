package costlookup

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/store"
)

func provisional(cost, upstream int64) store.ReconcileRow {
	return store.ReconcileRow{ID: "r1", CostNanos: cost, UpstreamCostNanos: upstream,
		InputCostNanos: cost / 2, OutputCostNanos: cost - cost/2}
}

func TestSettleRealRequest(t *testing.T) {
	s, ok := Settle(provisional(150000, 100000), 17)
	require.True(t, ok)
	require.Equal(t, "r1", s.ID)
	require.Equal(t, int64(34000), s.UpstreamCostNanos)
	require.Equal(t, int64(51000), s.CostNanos)
	require.Equal(t, "reconciled", s.Status)
}

func TestSettleClampsMultiplier(t *testing.T) {
	s, ok := Settle(provisional(80000, 100000), 17)
	require.True(t, ok)
	require.Equal(t, int64(34000), s.CostNanos, "never bill below what the provider charged")
}

func TestSettleOverCeiling(t *testing.T) {
	s, ok := Settle(provisional(150000, 100000), 1000)
	require.True(t, ok)
	require.Equal(t, int64(2000000), s.UpstreamCostNanos)
	require.Equal(t, int64(3000000), s.CostNanos)
	require.Equal(t, "over_ceiling", s.Status)
}

func TestSettleRoundsUp(t *testing.T) {
	// mult = 100001/100000; 34000 × mult = 34000.34 → 34001.
	s, ok := Settle(provisional(100001, 100000), 17)
	require.True(t, ok)
	require.Equal(t, int64(34001), s.CostNanos)
}

func TestSettleLargeValuesDoNotOverflow(t *testing.T) {
	// $5,000 provisional at 1.5×, provider charged $4,000 (quota 2e9).
	s, ok := Settle(provisional(7_500_000_000_000, 5_000_000_000_000), 2_000_000_000)
	require.True(t, ok)
	require.Equal(t, int64(6_000_000_000_000), s.CostNanos)
}

func TestSettleUnknownMultiplierBillsAtLeastTheProviderCharge(t *testing.T) {
	// A row with no provisional upstream cost (catalog or chain priced) has no
	// known markup: it is billed at no less than what Subrouter charged.
	s, ok := Settle(provisional(500, 0), 17)
	require.True(t, ok)
	require.Equal(t, "unverified", s.Status)
	require.Equal(t, int64(34000), s.UpstreamCostNanos)
	require.Equal(t, int64(34000), s.CostNanos)

	s, _ = Settle(provisional(90000, 0), 17)
	require.Equal(t, int64(90000), s.CostNanos, "a higher provisional bill is kept")
}

func TestSettleBreakdownSumsToTotal(t *testing.T) {
	r := store.ReconcileRow{ID: "r1", CostNanos: 150000, UpstreamCostNanos: 100000,
		InputCostNanos: 33333, CachedCostNanos: 11111, CacheWriteCostNanos: 7, OutputCostNanos: 99999, ReasoningCostNanos: 5550}
	s, ok := Settle(r, 17)
	require.True(t, ok)
	sum := s.InputCostNanos + s.CachedCostNanos + s.CacheWriteCostNanos + s.OutputCostNanos + s.ReasoningCostNanos
	require.Equal(t, s.CostNanos, sum)
	require.Less(t, s.InputCostNanos, int64(33333))

	empty := store.ReconcileRow{ID: "r2", CostNanos: 150000, UpstreamCostNanos: 100000}
	s, ok = Settle(empty, 17)
	require.True(t, ok)
	require.Equal(t, s.CostNanos, s.OutputCostNanos)
}
