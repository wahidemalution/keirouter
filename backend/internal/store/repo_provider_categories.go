package store

import (
	"context"
	"fmt"
	"time"
)

// ProviderCategoryRepo persists operator-managed display provider categories.
type ProviderCategoryRepo struct{ db *DB }

// ProviderCategories returns the provider category repository.
func (db *DB) ProviderCategories() *ProviderCategoryRepo { return &ProviderCategoryRepo{db: db} }

const providerCategoryColumns = `id, tenant_id, label, created_at, updated_at`

// List returns all display provider categories for a tenant.
func (r *ProviderCategoryRepo) List(ctx context.Context, tenantID string) ([]ProviderCategory, error) {
	q := r.db.rebind(`SELECT ` + providerCategoryColumns + ` FROM provider_categories WHERE tenant_id = ? ORDER BY label`)
	rows, err := r.db.sql.QueryContext(ctx, q, tenantID)
	if err != nil {
		return nil, fmt.Errorf("store: list provider categories: %w", err)
	}
	defer rows.Close()

	var out []ProviderCategory
	for rows.Next() {
		var c ProviderCategory
		var created, updated string
		if err := rows.Scan(&c.ID, &c.TenantID, &c.Label, &created, &updated); err != nil {
			return nil, err
		}
		c.CreatedAt = parseTime(created)
		c.UpdatedAt = parseTime(updated)
		out = append(out, c)
	}
	return out, rows.Err()
}

// Create inserts a display provider category.
func (r *ProviderCategoryRepo) Create(ctx context.Context, c ProviderCategory) error {
	now := formatTime(time.Now())
	q := r.db.rebind(`INSERT INTO provider_categories (` + providerCategoryColumns + `)
		VALUES (?, ?, ?, ?, ?)`)
	_, err := r.db.sql.ExecContext(ctx, q, c.ID, c.TenantID, c.Label, now, now)
	if err != nil {
		return fmt.Errorf("store: create provider category: %w", err)
	}
	return nil
}

// Delete removes a display provider category by id.
func (r *ProviderCategoryRepo) Delete(ctx context.Context, id string) error {
	q := r.db.rebind(`DELETE FROM provider_categories WHERE id = ?`)
	res, err := r.db.sql.ExecContext(ctx, q, id)
	if err != nil {
		return fmt.Errorf("store: delete provider category: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
