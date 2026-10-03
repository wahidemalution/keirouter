package store

import (
	"context"
	"time"
)

type MarketBinding struct {
	TenantID   string    `json:"tenant_id"`
	ProviderID string    `json:"provider_id"`
	ModelID    string    `json:"model_id"`
	MarketSlug string    `json:"market_slug"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type MarketBindingRepo struct{ db *DB }

func (db *DB) MarketBindings() *MarketBindingRepo { return &MarketBindingRepo{db: db} }

func (r *MarketBindingRepo) List(ctx context.Context, tenantID string) ([]MarketBinding, error) {
	q := r.db.rebind(`SELECT tenant_id, provider_id, model_id, market_slug, updated_at FROM model_market_bindings WHERE tenant_id = ?`)
	rows, err := r.db.sql.QueryContext(ctx, q, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MarketBinding
	for rows.Next() {
		var b MarketBinding
		var ts string
		if err := rows.Scan(&b.TenantID, &b.ProviderID, &b.ModelID, &b.MarketSlug, &ts); err != nil {
			return nil, err
		}
		b.UpdatedAt, _ = time.Parse(time.RFC3339Nano, ts)
		out = append(out, b)
	}
	return out, rows.Err()
}

func (r *MarketBindingRepo) Upsert(ctx context.Context, b MarketBinding) error {
	q := r.db.rebind(`INSERT INTO model_market_bindings (tenant_id, provider_id, model_id, market_slug, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (tenant_id, provider_id, model_id) DO UPDATE SET market_slug = excluded.market_slug, updated_at = excluded.updated_at`)
	_, err := r.db.sql.ExecContext(ctx, q, b.TenantID, b.ProviderID, b.ModelID, b.MarketSlug, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (r *MarketBindingRepo) Delete(ctx context.Context, tenantID, providerID, modelID string) error {
	q := r.db.rebind(`DELETE FROM model_market_bindings WHERE tenant_id = ? AND provider_id = ? AND model_id = ?`)
	_, err := r.db.sql.ExecContext(ctx, q, tenantID, providerID, modelID)
	return err
}
