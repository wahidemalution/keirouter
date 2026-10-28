package hosting

import (
	"strings"
	"time"

	"github.com/mydisha/keirouter/backend/internal/store"
)

// Zone is the time zone the finance views bucket days and months in: the
// server's local zone, so they line up with the dashboard clock. Tests pin it.
var Zone = time.Local

// MonthKey formats t as YYYY-MM in Zone.
func MonthKey(t time.Time) string { return t.In(Zone).Format("2006-01") }

// RefundRequired reports whether a settled order was paid for a deleted
// instance and must be returned, so it is not revenue.
func RefundRequired(o store.PaymentOrder) bool {
	return strings.HasPrefix(o.Reason, "refund_required")
}

// CostInMonth returns what c costs in the month starting at monthStart (the
// first day of the month, in Zone).
func CostInMonth(c store.HostingCost, monthStart time.Time) int64 {
	start, err := time.ParseInLocation("2006-01-02", c.StartDate, Zone)
	if err != nil {
		return 0
	}
	monthEnd := monthStart.AddDate(0, 1, 0)
	if c.Period == store.CostOneTime {
		if !start.Before(monthStart) && start.Before(monthEnd) {
			return c.AmountIDR
		}
		return 0
	}
	if !start.Before(monthEnd) {
		return 0
	}
	if c.EndDate != "" {
		end, err := time.ParseInLocation("2006-01-02", c.EndDate, Zone)
		if err == nil && end.Before(monthStart) {
			return 0
		}
	}
	return c.AmountIDR
}

// MonthFinance is one month of hosting revenue and cost.
type MonthFinance struct {
	Month      string `json:"month"`
	RevenueIDR int64  `json:"revenue_idr"`
	CostIDR    int64  `json:"cost_idr"`
	ProfitIDR  int64  `json:"profit_idr"`
	Orders     int    `json:"orders"`
	RefundIDR  int64  `json:"refund_idr"`
}

// Months builds the last n months (oldest first) ending with the month of
// now. Revenue is settled hosting orders by paid month; refunds are paid
// orders for deleted instances.
func Months(orders []store.PaymentOrder, costs []store.HostingCost, now time.Time, n int) []MonthFinance {
	if n < 1 {
		n = 1
	}
	cur := now.In(Zone)
	first := time.Date(cur.Year(), cur.Month(), 1, 0, 0, 0, 0, Zone).AddDate(0, -(n - 1), 0)
	out := make([]MonthFinance, n)
	idx := map[string]int{}
	for i := 0; i < n; i++ {
		m := first.AddDate(0, i, 0)
		out[i].Month = m.Format("2006-01")
		idx[out[i].Month] = i
		for _, c := range costs {
			out[i].CostIDR += CostInMonth(c, m)
		}
	}
	for _, o := range orders {
		if o.Product != store.ProductHosting || (o.Status != store.PaymentCompleted && o.Status != store.PaymentManual) {
			continue
		}
		paid := o.CreatedAt
		if t, err := time.Parse(time.RFC3339, o.PaidAt); err == nil {
			paid = t
		}
		i, ok := idx[MonthKey(paid)]
		if !ok {
			continue
		}
		if RefundRequired(o) {
			out[i].RefundIDR += o.AmountIDR
			continue
		}
		out[i].RevenueIDR += o.AmountIDR
		out[i].Orders++
	}
	for i := range out {
		out[i].ProfitIDR = out[i].RevenueIDR - out[i].CostIDR
	}
	return out
}
