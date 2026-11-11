package gateway

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/mydisha/keirouter/backend/internal/currency"
	"github.com/mydisha/keirouter/backend/internal/hosting"
	"github.com/mydisha/keirouter/backend/internal/store"
)

func (s *Server) mountHostingOps(r chi.Router) {
	r.Get("/hosting/finance", s.adminHostingFinance)
	r.Post("/hosting/costs", s.adminCreateHostingCost)
	r.Put("/hosting/costs/{id}", s.adminUpdateHostingCost)
	r.Delete("/hosting/costs/{id}", s.adminDeleteHostingCost)
}

// ─── Finance ────────────────────────────────────────────────────────────────

func costPaymentView(c store.CostPayment) map[string]any {
	v := map[string]any{
		"id": c.ID, "name": c.Name, "category": c.Category, "provider": c.Provider,
		"currency": c.Currency, "amount_idr": c.AmountIDR, "usd_idr_rate": nil, "amount_usd": nil,
		"payment_method": c.PaymentMethod, "paid_at": c.PaidAt, "note": c.Note,
		"created_at": c.CreatedAt, "updated_at": c.UpdatedAt,
	}
	if c.Currency == store.CurrencyUSD {
		v["amount_usd"] = float64(c.AmountUSDCents) / 100
		v["usd_idr_rate"] = c.USDIDRRate
	}
	return v
}

// adminHostingFinance returns monthly revenue, cost and profit for the last
// `months` months (default 12), a snapshot of the current month and the
// cost list.
func (s *Server) adminHostingFinance(w http.ResponseWriter, r *http.Request) {
	months := 12
	if v := r.URL.Query().Get("months"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 36 {
			writeError(w, http.StatusBadRequest, "months must be 1-36")
			return
		}
		months = n
	}
	ctx := r.Context()
	orders, err := s.db.PaymentOrders().ListByProduct(ctx, store.ProductHosting)
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	costs, err := s.db.CostPayments().List(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	instances, err := s.db.Hosting().ListAll(ctx, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	st, err := s.loadHostingSettings(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}

	now := time.Now()
	series := hosting.Months(orders, costs, now, months)
	cur := series[len(series)-1]
	counts := map[string]int{}
	running := 0
	var mrr int64 // monthly revenue of the running instances at today's prices
	for _, h := range instances {
		counts[string(h.Status)]++
		if h.Status == store.HostingActive || h.Status == store.HostingExpired {
			running++
			if p, ok := st.PeriodPrice(st.InstanceRegion(h.Region, h.NodeID)); ok {
				mrr += p
			}
		}
	}
	// Break-even uses the average price of what is running, or the cheapest
	// region when nothing runs yet.
	avgPrice := int64(0)
	if running > 0 {
		avgPrice = mrr / int64(running)
	} else {
		for _, rg := range st.Regions {
			if rg.Enabled && rg.PriceIDR > 0 && (avgPrice == 0 || rg.PriceIDR < avgPrice) {
				avgPrice = rg.PriceIDR
			}
		}
	}
	var refundOpen []map[string]any
	var totalRevenue, totalCost int64
	for _, m := range series {
		totalRevenue += m.RevenueIDR
		totalCost += m.CostIDR
	}
	for _, o := range orders {
		if o.Product == store.ProductHosting && hosting.RefundRequired(o) {
			refundOpen = append(refundOpen, map[string]any{
				"order_id": o.ID, "amount_idr": o.AmountIDR, "paid_at": o.PaidAt, "instance_id": o.HostingInstanceID,
			})
		}
	}
	var perInstance, breakEven int64
	if running > 0 {
		perInstance = cur.CostIDR / int64(running)
	}
	if avgPrice > 0 {
		breakEven = (cur.CostIDR + avgPrice - 1) / avgPrice
	}
	costViews := make([]map[string]any, 0, len(costs))
	for _, c := range costs {
		costViews = append(costViews, costPaymentView(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"months": series,
		"current": map[string]any{
			"month":                 cur.Month,
			"revenue_idr":           cur.RevenueIDR,
			"cost_idr":              cur.CostIDR,
			"profit_idr":            cur.ProfitIDR,
			"running_instances":     running,
			"mrr_idr":               mrr,
			"price_idr":             avgPrice,
			"cost_per_instance_idr": perInstance,
			"break_even_instances":  breakEven,
		},
		"totals":         map[string]any{"revenue_idr": totalRevenue, "cost_idr": totalCost, "profit_idr": totalRevenue - totalCost},
		"status_counts":  counts,
		"refunds_needed": refundOpen,
		"costs":          costViews,
		"note":           "Revenue is the gross order amount; SumoPod fees are not deducted.",
	})
}

type costPaymentRequest struct {
	Name     string `json:"name"`
	Category string `json:"category"`
	Provider string `json:"provider"`
	// Currency is USD or IDR; it is fixed once the payment is saved.
	Currency string `json:"currency"`
	// Amount is in Currency: dollars for USD, whole rupiah for IDR.
	Amount        float64 `json:"amount"`
	PaymentMethod string  `json:"payment_method"`
	Note          string  `json:"note"`
	// PaidDate is the day it was paid, YYYY-MM-DD in the server zone. Empty
	// keeps the current date: now on create, the saved date on update.
	PaidDate string `json:"paid_date"`
}

var (
	costPaymentCategories = map[string]bool{"vps": true, "upstream_ai": true, "domain": true, "software": true, "other": true}
	costPaymentMethods    = map[string]bool{"debit_card": true, "credit_card": true, "bank_transfer": true, "crypto": true}
)

// earliestCostDate is the oldest paid date accepted, to catch typos like 0202.
var earliestCostDate = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// paidAt resolves a YYYY-MM-DD paid date against the saved time prev. The
// same day keeps prev, today uses now, and any other day is set to noon in
// hosting.Zone so it stays in that day and month. Future days are refused.
func paidAt(date string, prev, now time.Time) (time.Time, error) {
	date = strings.TrimSpace(date)
	if date == "" {
		return prev, nil
	}
	d, err := time.ParseInLocation("2006-01-02", date, hosting.Zone)
	if err != nil {
		return time.Time{}, errors.New("paid_date must be a date as YYYY-MM-DD")
	}
	day := func(t time.Time) string { return t.In(hosting.Zone).Format("2006-01-02") }
	switch {
	case date > day(now):
		return time.Time{}, errors.New("paid_date cannot be in the future")
	case d.Before(earliestCostDate):
		return time.Time{}, errors.New("paid_date must be in 2000 or later")
	case !prev.IsZero() && date == day(prev):
		return prev, nil
	case date == day(now):
		return now, nil
	}
	return d.Add(12 * time.Hour).UTC(), nil
}

// maxCostAmountIDR bounds one payment, in rupiah.
const maxCostAmountIDR = 1_000_000_000_000

// apply validates b and writes it onto p. currency is the payment's currency
// (the request's on create, the stored one on update) and rate its USD->IDR
// rate; the amount in rupiah is derived from them.
func (b costPaymentRequest) apply(p *store.CostPayment, currency string, rate float64) error {
	p.Name, p.Category = strings.TrimSpace(b.Name), strings.TrimSpace(b.Category)
	p.Provider, p.PaymentMethod = strings.TrimSpace(b.Provider), strings.TrimSpace(b.PaymentMethod)
	p.Note = strings.TrimSpace(b.Note)
	switch {
	case p.Name == "" || len(p.Name) > 120:
		return errors.New("name is required (max 120 characters)")
	case !costPaymentCategories[p.Category]:
		return errors.New("category must be vps, upstream_ai, domain, software or other")
	case p.Category == "upstream_ai" && p.Provider == "":
		return errors.New("provider is required for an upstream AI payment")
	case len(p.Provider) > 120:
		return errors.New("provider must be 120 characters or fewer")
	case !costPaymentMethods[p.PaymentMethod]:
		return errors.New("payment_method must be debit_card, credit_card, bank_transfer or crypto")
	case len(p.Note) > 500:
		return errors.New("note must be 500 characters or fewer")
	case math.IsNaN(b.Amount) || math.IsInf(b.Amount, 0) || b.Amount <= 0:
		return errors.New("amount must be positive")
	}
	switch currency {
	case store.CurrencyUSD:
		cents := int64(math.Round(b.Amount * 100))
		if cents <= 0 {
			return errors.New("amount must be at least $0.01")
		}
		p.AmountUSDCents = cents
		p.AmountIDR = int64(math.Round(float64(cents) / 100 * rate))
	case store.CurrencyIDR:
		if b.Amount != math.Trunc(b.Amount) {
			return errors.New("an IDR amount must be whole rupiah")
		}
		p.AmountUSDCents, p.AmountIDR = 0, int64(b.Amount)
	default:
		return errors.New("currency must be USD or IDR")
	}
	if p.AmountIDR <= 0 || p.AmountIDR > maxCostAmountIDR {
		return errors.New("amount is out of range")
	}
	return nil
}

func (s *Server) adminCreateHostingCost(w http.ResponseWriter, r *http.Request) {
	var body costPaymentRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	ccy := strings.ToUpper(strings.TrimSpace(body.Currency))
	var rate float64
	if ccy == store.CurrencyUSD {
		var ok bool
		if rate, _, ok = currency.Resolve(s.currencySvc.Load(r.Context())); !ok {
			writeError(w, http.StatusBadRequest, "no USD to IDR rate is available yet: refresh it or set an override in Currency settings")
			return
		}
	}
	// Stored to the second, so the response matches what a later read returns.
	now := time.Now().UTC().Truncate(time.Second)
	paid, err := paidAt(body.PaidDate, now, now)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	p := store.CostPayment{ID: uuid.NewString(), Currency: ccy, USDIDRRate: rate, PaidAt: paid, CreatedAt: now, UpdatedAt: now}
	if err := body.apply(&p, ccy, rate); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.db.CostPayments().Create(r.Context(), p); err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	writeJSON(w, http.StatusCreated, costPaymentView(p))
}

// adminUpdateHostingCost edits a payment. Its currency and rate stay as
// recorded, so a USD amount is re-converted at the saved rate. The paid date
// can move; without paid_date the saved paid time is kept.
func (s *Server) adminUpdateHostingCost(w http.ResponseWriter, r *http.Request) {
	var body costPaymentRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	p, err := s.db.CostPayments().Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "payment not found")
			return
		}
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	if c := strings.TrimSpace(body.Currency); c != "" && !strings.EqualFold(c, p.Currency) {
		writeError(w, http.StatusBadRequest, "the currency of a saved payment cannot change: delete it and add a new one")
		return
	}
	if p.PaidAt, err = paidAt(body.PaidDate, p.PaidAt, time.Now().UTC().Truncate(time.Second)); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := body.apply(&p, p.Currency, p.USDIDRRate); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.db.CostPayments().Update(r.Context(), p); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "payment not found")
			return
		}
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	updated, err := s.db.CostPayments().Get(r.Context(), p.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	writeJSON(w, http.StatusOK, costPaymentView(updated))
}

func (s *Server) adminDeleteHostingCost(w http.ResponseWriter, r *http.Request) {
	if err := s.db.CostPayments().Delete(r.Context(), chi.URLParam(r, "id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "payment not found")
			return
		}
		writeError(w, http.StatusInternalServerError, sanitizeError(s.log, err, "internal server error"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
