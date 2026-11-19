package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// KeyTokenAdjustmentRepo persists append-only manual token limit corrections.
type KeyTokenAdjustmentRepo struct{ db *DB }

// TokenAdjustments returns the key token adjustment repository.
func (db *DB) TokenAdjustments() *KeyTokenAdjustmentRepo {
	return &KeyTokenAdjustmentRepo{db: db}
}

const keyTokenAdjustmentSelectCols = `id, tenant_id, key_id, budget_id, delta_tokens, reason,
	limit_before_tokens, limit_after_tokens, used_tokens, idempotency_key, actor, created_at`

// CreateOnTx inserts an adjustment record within an existing transaction.
func (r *KeyTokenAdjustmentRepo) CreateOnTx(ctx context.Context, tx *sql.Tx, a KeyTokenAdjustment) error {
	q := r.db.rebind(`INSERT INTO key_token_adjustments
		(id, tenant_id, key_id, budget_id, delta_tokens, reason, limit_before_tokens,
		 limit_after_tokens, used_tokens, idempotency_key, actor, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	_, err := tx.ExecContext(ctx, q, a.ID, a.TenantID, a.KeyID, a.BudgetID, a.DeltaTokens,
		a.Reason, a.LimitBeforeTokens, a.LimitAfterTokens, a.UsedTokens, a.IdempotencyKey,
		a.Actor, formatTime(a.CreatedAt))
	if err != nil {
		return fmt.Errorf("store: create key token adjustment: %w", err)
	}
	return nil
}

// ListByKey returns a key's token adjustments, newest first, with the same
// ordering caveat as KeyLimitAdjustmentRepo.ListByKey.
func (r *KeyTokenAdjustmentRepo) ListByKey(ctx context.Context, keyID string) ([]KeyTokenAdjustment, error) {
	q := r.db.rebind(`SELECT ` + keyTokenAdjustmentSelectCols + ` FROM key_token_adjustments WHERE key_id = ? ORDER BY created_at DESC, id DESC`)
	rows, err := r.db.sql.QueryContext(ctx, q, keyID)
	if err != nil {
		return nil, fmt.Errorf("store: list key token adjustments: %w", err)
	}
	defer rows.Close()

	var out []KeyTokenAdjustment
	for rows.Next() {
		a, err := scanKeyTokenAdjustment(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetByIdempotencyKey returns the adjustment matching (keyID, idem), or ErrNotFound.
func (r *KeyTokenAdjustmentRepo) GetByIdempotencyKey(ctx context.Context, keyID, idem string) (KeyTokenAdjustment, error) {
	q := r.db.rebind(`SELECT ` + keyTokenAdjustmentSelectCols + ` FROM key_token_adjustments WHERE key_id = ? AND idempotency_key = ?`)
	a, err := scanKeyTokenAdjustment(r.db.sql.QueryRowContext(ctx, q, keyID, idem).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return KeyTokenAdjustment{}, ErrNotFound
	}
	return a, err
}

func scanKeyTokenAdjustment(scan func(dest ...any) error) (KeyTokenAdjustment, error) {
	var (
		a       KeyTokenAdjustment
		created string
	)
	err := scan(&a.ID, &a.TenantID, &a.KeyID, &a.BudgetID, &a.DeltaTokens, &a.Reason,
		&a.LimitBeforeTokens, &a.LimitAfterTokens, &a.UsedTokens, &a.IdempotencyKey, &a.Actor, &created)
	if err != nil {
		return KeyTokenAdjustment{}, err
	}
	a.CreatedAt = parseTime(created)
	return a, nil
}
