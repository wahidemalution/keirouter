package hosting

import (
	"testing"
	"time"

	"github.com/mydisha/keirouter/backend/internal/store"
	"github.com/stretchr/testify/require"
)

// WIB pins Zone so results do not depend on the test host.
var WIB = time.FixedZone("WIB", 7*3600)

func init() { Zone = WIB }

func TestMonths(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, WIB)
	orders := []store.PaymentOrder{
		{Product: store.ProductHosting, Status: store.PaymentCompleted, AmountIDR: 50000, PaidAt: "2026-10-02T03:00:00Z"},
		// 2026-09-30T18:00Z is 1 Oct 01:00 WIB: counts in October.
		{Product: store.ProductHosting, Status: store.PaymentManual, AmountIDR: 50000, PaidAt: "2026-09-30T18:00:00Z"},
		{Product: store.ProductHosting, Status: store.PaymentCompleted, AmountIDR: 50000, PaidAt: "2026-09-10T00:00:00Z"},
		{Product: store.ProductHosting, Status: store.PaymentCompleted, AmountIDR: 50000, PaidAt: "2026-10-05T00:00:00Z", Reason: "refund_required: instance was deleted"},
		{Product: store.ProductHosting, Status: store.PaymentPending, AmountIDR: 50000},
		{Product: store.ProductCredit, Status: store.PaymentCompleted, AmountIDR: 999999, PaidAt: "2026-10-03T00:00:00Z"},
	}
	costs := []store.CostPayment{
		{AmountIDR: 120000, PaidAt: time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)},
		// 2026-09-30T18:00Z is 1 Oct 01:00 WIB: counts in October.
		{AmountIDR: 5000, PaidAt: time.Date(2026, 9, 30, 18, 0, 0, 0, time.UTC)},
		{AmountIDR: 7000, PaidAt: time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)},
		{AmountIDR: 999, PaidAt: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)},
	}
	m := Months(orders, costs, now, 2)
	require.Len(t, m, 2)
	require.Equal(t, "2026-09", m[0].Month)
	require.EqualValues(t, 50000, m[0].RevenueIDR)
	require.Equal(t, "2026-10", m[1].Month)
	require.EqualValues(t, 100000, m[1].RevenueIDR)
	require.Equal(t, 2, m[1].Orders)
	require.EqualValues(t, 50000, m[1].RefundIDR)
	require.EqualValues(t, 7000, m[0].CostIDR)
	require.EqualValues(t, 125000, m[1].CostIDR)
	require.EqualValues(t, -25000, m[1].ProfitIDR)
}
