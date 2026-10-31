package costlookup

import (
	"math/big"

	"github.com/mydisha/keirouter/backend/internal/store"
)

// Settle prices row at the provider's logged quota. The row keeps the
// multiplier it was billed with (provisional cost / provisional upstream
// cost, at least 1), so the customer pays the real cost times the same
// markup, rounded up. ok is false when the provisional upstream cost is 0:
// the multiplier is unknown and no markup is guessed.
func Settle(row store.ReconcileRow, quota int64) (store.ReconcileSettlement, bool) {
	if row.UpstreamCostNanos <= 0 {
		return store.ReconcileSettlement{}, false
	}
	upstream := quota * QuotaNanos
	cost := upstream
	if row.CostNanos > row.UpstreamCostNanos {
		// ceil(upstream × cost / provisionalUpstream) without int64 overflow.
		n := new(big.Int).Mul(big.NewInt(upstream), big.NewInt(row.CostNanos))
		d := big.NewInt(row.UpstreamCostNanos)
		n.Add(n, new(big.Int).Sub(d, big.NewInt(1)))
		cost = n.Quo(n, d).Int64()
	}
	s := store.ReconcileSettlement{ID: row.ID, Status: "reconciled", CostNanos: cost, UpstreamCostNanos: upstream}
	if upstream > row.UpstreamCostNanos {
		s.Status = "over_ceiling"
	}
	rescale(&s, row)
	return s, true
}

// rescale splits the new total across the row's cost classes in their
// provisional proportions; output absorbs rounding so the parts sum exactly.
func rescale(s *store.ReconcileSettlement, row store.ReconcileRow) {
	old := row.InputCostNanos + row.CachedCostNanos + row.CacheWriteCostNanos + row.OutputCostNanos + row.ReasoningCostNanos
	if old <= 0 {
		s.OutputCostNanos = s.CostNanos
		return
	}
	part := func(v int64) int64 {
		n := new(big.Int).Mul(big.NewInt(v), big.NewInt(s.CostNanos))
		return n.Quo(n, big.NewInt(old)).Int64()
	}
	s.InputCostNanos = part(row.InputCostNanos)
	s.CachedCostNanos = part(row.CachedCostNanos)
	s.CacheWriteCostNanos = part(row.CacheWriteCostNanos)
	s.ReasoningCostNanos = part(row.ReasoningCostNanos)
	s.OutputCostNanos = s.CostNanos - s.InputCostNanos - s.CachedCostNanos - s.CacheWriteCostNanos - s.ReasoningCostNanos
}
