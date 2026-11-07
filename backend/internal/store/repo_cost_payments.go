package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Cost payment currencies.
const (
	CurrencyUSD = "USD"
	CurrencyIDR = "IDR"
)

// CostPayment is one paid operating cost (VPS rent, upstream AI credit, ...).
// PaidAt is when it was paid: the moment it was recorded, or noon of a
// back-dated day. A USD payment stores the USD->IDR rate
// of that moment; AmountIDR is fixed at save time and never re-converted.
type CostPayment struct {
	ID             string
	Name           string
	Category       string
	Provider       string
	Currency       string
	AmountUSDCents int64
	USDIDRRate     float64
	AmountIDR      int64
	PaymentMethod  string
	PaidAt         time.Time
	Note           string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// CostPaymentRepo persists cost payments.
type CostPaymentRepo struct{ db *DB }

// CostPayments returns the cost payment repository.
func (db *DB) CostPayments() *CostPaymentRepo { return &CostPaymentRepo{db: db} }

const costPaymentCols = `id, name, category, provider, currency, amount_usd_cents, usd_idr_rate, amount_idr, payment_method, paid_at, note, created_at, updated_at`

// Create inserts a payment.
func (r *CostPaymentRepo) Create(ctx context.Context, p CostPayment) error {
	q := r.db.rebind(`INSERT INTO cost_payments (` + costPaymentCols + `) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	_, err := r.db.sql.ExecContext(ctx, q, p.ID, p.Name, p.Category, p.Provider, p.Currency, p.AmountUSDCents, p.USDIDRRate,
		p.AmountIDR, p.PaymentMethod, formatTime(p.PaidAt), p.Note, formatTime(p.CreatedAt), formatTime(p.UpdatedAt))
	if err != nil {
		return fmt.Errorf("store: create cost payment: %w", err)
	}
	return nil
}

// Update replaces the editable fields, paid time included. The currency and
// rate stay as recorded.
func (r *CostPaymentRepo) Update(ctx context.Context, p CostPayment) error {
	q := r.db.rebind(`UPDATE cost_payments SET name = ?, category = ?, provider = ?, amount_usd_cents = ?, amount_idr = ?,
		payment_method = ?, paid_at = ?, note = ?, updated_at = ? WHERE id = ?`)
	res, err := r.db.sql.ExecContext(ctx, q, p.Name, p.Category, p.Provider, p.AmountUSDCents, p.AmountIDR,
		p.PaymentMethod, formatTime(p.PaidAt), p.Note, formatTime(time.Now()), p.ID)
	if err != nil {
		return fmt.Errorf("store: update cost payment: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes a payment.
func (r *CostPaymentRepo) Delete(ctx context.Context, id string) error {
	q := r.db.rebind(`DELETE FROM cost_payments WHERE id = ?`)
	res, err := r.db.sql.ExecContext(ctx, q, id)
	if err != nil {
		return fmt.Errorf("store: delete cost payment: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Get returns one payment.
func (r *CostPaymentRepo) Get(ctx context.Context, id string) (CostPayment, error) {
	q := r.db.rebind(`SELECT ` + costPaymentCols + ` FROM cost_payments WHERE id = ?`)
	p, err := scanCostPayment(r.db.sql.QueryRowContext(ctx, q, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return CostPayment{}, ErrNotFound
	}
	return p, err
}

// List returns every payment, newest first.
func (r *CostPaymentRepo) List(ctx context.Context) ([]CostPayment, error) {
	rows, err := r.db.sql.QueryContext(ctx, `SELECT `+costPaymentCols+` FROM cost_payments ORDER BY paid_at DESC, id`)
	if err != nil {
		return nil, fmt.Errorf("store: list cost payments: %w", err)
	}
	defer rows.Close()
	var out []CostPayment
	for rows.Next() {
		p, err := scanCostPayment(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func scanCostPayment(scan func(dest ...any) error) (CostPayment, error) {
	var p CostPayment
	var paid, created, updated string
	if err := scan(&p.ID, &p.Name, &p.Category, &p.Provider, &p.Currency, &p.AmountUSDCents, &p.USDIDRRate,
		&p.AmountIDR, &p.PaymentMethod, &paid, &p.Note, &created, &updated); err != nil {
		return CostPayment{}, err
	}
	p.PaidAt, p.CreatedAt, p.UpdatedAt = parseTime(paid), parseTime(created), parseTime(updated)
	return p, nil
}
