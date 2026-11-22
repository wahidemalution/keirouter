package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/mydisha/keirouter/backend/internal/crypto"
)

// TokenPackageKind says what a package grants.
type TokenPackageKind string

const (
	TokenPackageTokens TokenPackageKind = "tokens" // a token allowance plus validity days
	TokenPackageDays   TokenPackageKind = "days"   // extra days on an active package
)

// TokenPackage is one sellable prepaid token package.
type TokenPackage struct {
	ID       string
	TenantID string
	Name     string
	Kind     TokenPackageKind
	Tokens   int64
	Days     int
	PriceIDR int64
	Enabled  bool
	Featured bool
	Sort     int
	// RPMLimit and ConcurrencyLimit are the package's own key limits; nil
	// follows the token plan.
	RPMLimit         *int64
	ConcurrencyLimit *int64
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// TokenPackageRepo persists token packages.
type TokenPackageRepo struct{ db *DB }

// TokenPackages returns the token package repository.
func (db *DB) TokenPackages() *TokenPackageRepo { return &TokenPackageRepo{db: db} }

const tokenPackageCols = `id, tenant_id, name, kind, tokens, days, price_idr, enabled, featured, sort, created_at, updated_at, rpm_limit, concurrency_limit`

// List returns every package ordered for display.
func (r *TokenPackageRepo) List(ctx context.Context) ([]TokenPackage, error) {
	rows, err := r.db.sql.QueryContext(ctx, `SELECT `+tokenPackageCols+` FROM token_packages ORDER BY sort, price_idr`)
	if err != nil {
		return nil, fmt.Errorf("store: list token packages: %w", err)
	}
	defer rows.Close()
	var out []TokenPackage
	for rows.Next() {
		p, err := scanTokenPackage(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("store: scan token package: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Get returns one package by id.
func (r *TokenPackageRepo) Get(ctx context.Context, id string) (TokenPackage, error) {
	return r.getOne(ctx, r.db.sql, id)
}

// GetOnTx returns one package by id within a transaction.
func (r *TokenPackageRepo) GetOnTx(ctx context.Context, tx *sql.Tx, id string) (TokenPackage, error) {
	return r.getOne(ctx, tx, id)
}

type sqlQueryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (r *TokenPackageRepo) getOne(ctx context.Context, q sqlQueryRower, id string) (TokenPackage, error) {
	row := q.QueryRowContext(ctx, r.db.rebind(`SELECT `+tokenPackageCols+` FROM token_packages WHERE id = ?`), id)
	p, err := scanTokenPackage(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return TokenPackage{}, ErrNotFound
	}
	return p, err
}

// Create inserts a package.
func (r *TokenPackageRepo) Create(ctx context.Context, p TokenPackage) error {
	q := r.db.rebind(`INSERT INTO token_packages (` + tokenPackageCols + `)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	_, err := r.db.sql.ExecContext(ctx, q,
		p.ID, p.TenantID, p.Name, string(p.Kind), p.Tokens, p.Days, p.PriceIDR,
		boolToInt(p.Enabled), boolToInt(p.Featured), p.Sort, formatTime(p.CreatedAt), formatTime(p.UpdatedAt),
		nullInt64Ptr(p.RPMLimit), nullInt64Ptr(p.ConcurrencyLimit))
	if err != nil {
		if isUniqueViolation(err) {
			return ErrAlreadyExists
		}
		return fmt.Errorf("store: create token package: %w", err)
	}
	return nil
}

// Update rewrites a package's editable fields.
func (r *TokenPackageRepo) Update(ctx context.Context, p TokenPackage) error {
	q := r.db.rebind(`UPDATE token_packages SET name = ?, kind = ?, tokens = ?, days = ?, price_idr = ?,
		enabled = ?, featured = ?, sort = ?, rpm_limit = ?, concurrency_limit = ?, updated_at = ? WHERE id = ?`)
	res, err := r.db.sql.ExecContext(ctx, q,
		p.Name, string(p.Kind), p.Tokens, p.Days, p.PriceIDR,
		boolToInt(p.Enabled), boolToInt(p.Featured), p.Sort,
		nullInt64Ptr(p.RPMLimit), nullInt64Ptr(p.ConcurrencyLimit), formatTime(p.UpdatedAt), p.ID)
	if err != nil {
		return fmt.Errorf("store: update token package: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes a package.
func (r *TokenPackageRepo) Delete(ctx context.Context, id string) error {
	res, err := r.db.sql.ExecContext(ctx, r.db.rebind(`DELETE FROM token_packages WHERE id = ?`), id)
	if err != nil {
		return fmt.Errorf("store: delete token package: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func scanTokenPackage(scan func(dest ...any) error) (TokenPackage, error) {
	var (
		p                 TokenPackage
		kind              string
		enabled, featured int
		created, updated  string
		rpm, conc         sql.NullInt64
	)
	if err := scan(&p.ID, &p.TenantID, &p.Name, &kind, &p.Tokens, &p.Days, &p.PriceIDR,
		&enabled, &featured, &p.Sort, &created, &updated, &rpm, &conc); err != nil {
		return TokenPackage{}, err
	}
	p.RPMLimit, p.ConcurrencyLimit = int64PtrOf(rpm), int64PtrOf(conc)
	p.Kind = TokenPackageKind(kind)
	p.Enabled = enabled == 1
	p.Featured = featured == 1
	p.CreatedAt = parseTime(created)
	p.UpdatedAt = parseTime(updated)
	return p, nil
}

// TokenSubscription ties a portal user to their token key and its expiry.
type TokenSubscription struct {
	GoogleSub string
	KeyID     string
	SealedKey crypto.Sealed
	ExpiresAt time.Time
	// RPMLimit and ConcurrencyLimit are the key limits the subscription's
	// purchases add up to (tokenpkg.Apply); nil follows the token plan.
	RPMLimit         *int64
	ConcurrencyLimit *int64
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// TokenSubscriptionRepo persists token subscriptions.
type TokenSubscriptionRepo struct{ db *DB }

// TokenSubscriptions returns the token subscription repository.
func (db *DB) TokenSubscriptions() *TokenSubscriptionRepo { return &TokenSubscriptionRepo{db: db} }

const tokenSubCols = `google_sub, key_id, sealed_dek, sealed_ct, expires_at, created_at, updated_at, rpm_limit, concurrency_limit`

// GetBySub returns the subscription of a Google subject.
func (r *TokenSubscriptionRepo) GetBySub(ctx context.Context, sub string) (TokenSubscription, error) {
	return r.getOne(ctx, r.db.sql, `google_sub`, sub)
}

// GetBySubOnTx is GetBySub within a transaction.
func (r *TokenSubscriptionRepo) GetBySubOnTx(ctx context.Context, tx *sql.Tx, sub string) (TokenSubscription, error) {
	return r.getOne(ctx, tx, `google_sub`, sub)
}

// GetByKey returns the subscription that owns an API key.
func (r *TokenSubscriptionRepo) GetByKey(ctx context.Context, keyID string) (TokenSubscription, error) {
	return r.getOne(ctx, r.db.sql, `key_id`, keyID)
}

func (r *TokenSubscriptionRepo) getOne(ctx context.Context, q sqlQueryRower, col, arg string) (TokenSubscription, error) {
	row := q.QueryRowContext(ctx, r.db.rebind(`SELECT `+tokenSubCols+` FROM token_subscriptions WHERE `+col+` = ?`), arg)
	s, err := scanTokenSubscription(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return TokenSubscription{}, ErrNotFound
	}
	return s, err
}

// CreateOnTx inserts a subscription within a transaction.
func (r *TokenSubscriptionRepo) CreateOnTx(ctx context.Context, tx *sql.Tx, s TokenSubscription) error {
	q := r.db.rebind(`INSERT INTO token_subscriptions (` + tokenSubCols + `) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	_, err := tx.ExecContext(ctx, q, s.GoogleSub, s.KeyID, s.SealedKey.WrappedDEK, s.SealedKey.Ciphertext,
		formatTime(s.ExpiresAt), formatTime(s.CreatedAt), formatTime(s.UpdatedAt),
		nullInt64Ptr(s.RPMLimit), nullInt64Ptr(s.ConcurrencyLimit))
	if err != nil {
		if isUniqueViolation(err) {
			return ErrAlreadyExists
		}
		return fmt.Errorf("store: create token subscription: %w", err)
	}
	return nil
}

// SetExpiresOnTx moves a subscription's expiry within a transaction.
func (r *TokenSubscriptionRepo) SetExpiresOnTx(ctx context.Context, tx *sql.Tx, sub string, at time.Time) error {
	q := r.db.rebind(`UPDATE token_subscriptions SET expires_at = ?, updated_at = ? WHERE google_sub = ?`)
	res, err := tx.ExecContext(ctx, q, formatTime(at), formatTime(time.Now()), sub)
	if err != nil {
		return fmt.Errorf("store: set token subscription expiry: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetLimitsOnTx stores a subscription's key rate limits within a
// transaction; nil follows the token plan.
func (r *TokenSubscriptionRepo) SetLimitsOnTx(ctx context.Context, tx *sql.Tx, sub string, rpm, concurrency *int64) error {
	q := r.db.rebind(`UPDATE token_subscriptions SET rpm_limit = ?, concurrency_limit = ?, updated_at = ? WHERE google_sub = ?`)
	res, err := tx.ExecContext(ctx, q, nullInt64Ptr(rpm), nullInt64Ptr(concurrency), formatTime(time.Now()), sub)
	if err != nil {
		return fmt.Errorf("store: set token subscription limits: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// int64PtrOf returns n's value, or nil when it is NULL.
func int64PtrOf(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
}

// SetSealedKeyByKey replaces the stored encrypted plaintext of the subscription
// that owns keyID, so a reveal after a key rotation returns the current key.
func (r *TokenSubscriptionRepo) SetSealedKeyByKey(ctx context.Context, keyID string, sealed crypto.Sealed) error {
	q := r.db.rebind(`UPDATE token_subscriptions SET sealed_dek = ?, sealed_ct = ?, updated_at = ? WHERE key_id = ?`)
	res, err := r.db.sql.ExecContext(ctx, q, sealed.WrappedDEK, sealed.Ciphertext, formatTime(time.Now()), keyID)
	if err != nil {
		return fmt.Errorf("store: set token subscription sealed key: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// List returns every subscription, newest first.
func (r *TokenSubscriptionRepo) List(ctx context.Context) ([]TokenSubscription, error) {
	rows, err := r.db.sql.QueryContext(ctx, `SELECT `+tokenSubCols+` FROM token_subscriptions ORDER BY created_at DESC, google_sub`)
	if err != nil {
		return nil, fmt.Errorf("store: list token subscriptions: %w", err)
	}
	defer rows.Close()
	var out []TokenSubscription
	for rows.Next() {
		s, err := scanTokenSubscription(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("store: scan token subscription: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func scanTokenSubscription(scan func(dest ...any) error) (TokenSubscription, error) {
	var (
		s                         TokenSubscription
		dek, ct                   string
		expires, created, updated string
		rpm, conc                 sql.NullInt64
	)
	if err := scan(&s.GoogleSub, &s.KeyID, &dek, &ct, &expires, &created, &updated, &rpm, &conc); err != nil {
		return TokenSubscription{}, err
	}
	s.RPMLimit, s.ConcurrencyLimit = int64PtrOf(rpm), int64PtrOf(conc)
	s.SealedKey = crypto.Sealed{WrappedDEK: dek, Ciphertext: ct}
	s.ExpiresAt = parseTime(expires)
	s.CreatedAt = parseTime(created)
	s.UpdatedAt = parseTime(updated)
	return s, nil
}

// DayTokens is the token count of one UTC day.
type DayTokens struct {
	Day    string // YYYY-MM-DD
	Tokens int64
}

// DailyTokens sums prompt plus completion tokens of every token package key
// per UTC day, for days on or after sinceDay (YYYY-MM-DD). Days without usage
// are left out. It counts tokens the same way the token budget does.
func (r *TokenSubscriptionRepo) DailyTokens(ctx context.Context, sinceDay string) ([]DayTokens, error) {
	q := `SELECT substr(u.created_at, 1, 10) AS day, CAST(SUM(u.prompt_tokens + u.completion_tokens) AS BIGINT)
		FROM usage_records u JOIN token_subscriptions s ON s.key_id = u.api_key_id
		WHERE u.created_at >= ? GROUP BY substr(u.created_at, 1, 10) ORDER BY day`
	if r.db.spendRollup.Load() {
		q = `SELECT d.day, CAST(SUM(d.tokens) AS BIGINT)
			FROM usage_spend_daily d JOIN token_subscriptions s ON s.key_id = d.scope_id
			WHERE d.scope_kind = 'api_key' AND d.day >= ? GROUP BY d.day ORDER BY d.day`
	}
	rows, err := r.db.sql.QueryContext(ctx, r.db.rebind(q), sinceDay)
	if err != nil {
		return nil, fmt.Errorf("store: token daily usage: %w", err)
	}
	defer rows.Close()
	var out []DayTokens
	for rows.Next() {
		var d DayTokens
		if err := rows.Scan(&d.Day, &d.Tokens); err != nil {
			return nil, fmt.Errorf("store: scan token daily usage: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ProviderUpstreamCost is what token package keys cost at one upstream
// provider before markup.
type ProviderUpstreamCost struct {
	Provider string
	// TotalNanos and RangeNanos sum upstream_cost_nanos all-time and since
	// the range start.
	TotalNanos int64
	RangeNanos int64
	// Pending counts requests whose upstream cost is still the provisional
	// ceiling, waiting for the provider's usage log.
	Pending int64
	// Unpriced counts requests that used tokens but recorded no upstream
	// cost, so the totals leave them out.
	Unpriced int64
}

// UpstreamCost sums the pre-markup upstream cost of every token package
// key's usage per provider, all-time and since since.
func (r *TokenSubscriptionRepo) UpstreamCost(ctx context.Context, since time.Time) ([]ProviderUpstreamCost, error) {
	q := r.db.rebind(`SELECT u.provider,
		CAST(COALESCE(SUM(u.upstream_cost_nanos), 0) AS BIGINT),
		CAST(COALESCE(SUM(CASE WHEN u.created_at >= ? THEN u.upstream_cost_nanos ELSE 0 END), 0) AS BIGINT),
		CAST(COALESCE(SUM(CASE WHEN u.reconcile_status = 'pending' THEN 1 ELSE 0 END), 0) AS BIGINT),
		CAST(COALESCE(SUM(CASE WHEN u.upstream_cost_nanos = 0 AND u.prompt_tokens + u.completion_tokens > 0 THEN 1 ELSE 0 END), 0) AS BIGINT)
		FROM usage_records u JOIN token_subscriptions s ON s.key_id = u.api_key_id
		WHERE u.provider <> ''
		GROUP BY u.provider ORDER BY u.provider`)
	rows, err := r.db.sql.QueryContext(ctx, q, formatTime(since))
	if err != nil {
		return nil, fmt.Errorf("store: token upstream cost: %w", err)
	}
	defer rows.Close()
	var out []ProviderUpstreamCost
	for rows.Next() {
		var c ProviderUpstreamCost
		if err := rows.Scan(&c.Provider, &c.TotalNanos, &c.RangeNanos, &c.Pending, &c.Unpriced); err != nil {
			return nil, fmt.Errorf("store: scan token upstream cost: %w", err)
		}
		if c.TotalNanos == 0 && c.Pending == 0 && c.Unpriced == 0 {
			continue
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
