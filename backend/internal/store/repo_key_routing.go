package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// KeyRouting is a key's provider routing settings.
type KeyRouting struct {
	// AllowedProviders is the admin ceiling; empty means every provider.
	AllowedProviders []string
	// DisabledProviders holds, per normalized model, the providers the
	// customer turned off.
	DisabledProviders map[string][]string
	// FinalFallback holds the final-fallback setting per normalized model
	// (FinalFallbackOff, FinalFallbackChosen or FinalFallbackKept). A missing
	// entry means off.
	FinalFallback map[string]int
}

// KeyRouting loads a key's provider ceiling, per-model disabled providers and
// per-model final-fallback choices.
func (r *APIKeyRepo) KeyRouting(ctx context.Context, keyID string) (KeyRouting, error) {
	out := KeyRouting{DisabledProviders: map[string][]string{}, FinalFallback: map[string]int{}}

	q := r.db.rebind(`SELECT provider_id FROM api_key_allowed_providers WHERE api_key_id = ? ORDER BY provider_id`)
	rows, err := r.db.sql.QueryContext(ctx, q, keyID)
	if err != nil {
		return KeyRouting{}, fmt.Errorf("store: list allowed providers: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return KeyRouting{}, fmt.Errorf("store: scan allowed provider: %w", err)
		}
		out.AllowedProviders = append(out.AllowedProviders, p)
	}
	if err := rows.Err(); err != nil {
		return KeyRouting{}, err
	}

	q = r.db.rebind(`SELECT model, provider_id FROM api_key_model_disabled_providers WHERE api_key_id = ? ORDER BY model, provider_id`)
	drows, err := r.db.sql.QueryContext(ctx, q, keyID)
	if err != nil {
		return KeyRouting{}, fmt.Errorf("store: list disabled providers: %w", err)
	}
	defer drows.Close()
	for drows.Next() {
		var model, p string
		if err := drows.Scan(&model, &p); err != nil {
			return KeyRouting{}, fmt.Errorf("store: scan disabled provider: %w", err)
		}
		model = NormalizeDiscountModel(model)
		out.DisabledProviders[model] = append(out.DisabledProviders[model], p)
	}
	if err := drows.Err(); err != nil {
		return KeyRouting{}, err
	}

	q = r.db.rebind(`SELECT model, final_fallback FROM api_key_model_options WHERE api_key_id = ?`)
	orows, err := r.db.sql.QueryContext(ctx, q, keyID)
	if err != nil {
		return KeyRouting{}, fmt.Errorf("store: list model options: %w", err)
	}
	defer orows.Close()
	for orows.Next() {
		var model string
		var setting int
		if err := orows.Scan(&model, &setting); err != nil {
			return KeyRouting{}, fmt.Errorf("store: scan model option: %w", err)
		}
		out.FinalFallback[NormalizeDiscountModel(model)] = setting
	}
	return out, orows.Err()
}

// SetAllowedProviders replaces a key's provider ceiling. Entries are trimmed,
// deduplicated and empties dropped; an empty result removes the ceiling.
func (r *APIKeyRepo) SetAllowedProviders(ctx context.Context, keyID string, providers []string) error {
	seen := map[string]struct{}{}
	clean := make([]string, 0, len(providers))
	for _, p := range providers {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		clean = append(clean, p)
	}

	tx, err := r.db.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: set allowed providers: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, r.db.rebind(`DELETE FROM api_key_allowed_providers WHERE api_key_id = ?`), keyID); err != nil {
		return fmt.Errorf("store: clear allowed providers: %w", err)
	}
	ins := r.db.rebind(`INSERT INTO api_key_allowed_providers (api_key_id, provider_id, created_at) VALUES (?, ?, ?)`)
	now := formatTime(time.Now())
	for _, p := range clean {
		if _, err := tx.ExecContext(ctx, ins, keyID, p, now); err != nil {
			return fmt.Errorf("store: insert allowed provider: %w", err)
		}
	}
	return tx.Commit()
}

// SetModelProviderEnabled turns one provider on or off for one model of a key.
// On deletes the row, so a missing row means on.
func (r *APIKeyRepo) SetModelProviderEnabled(ctx context.Context, keyID, model, provider string, enabled bool) error {
	model = NormalizeDiscountModel(model)
	if enabled {
		q := r.db.rebind(`DELETE FROM api_key_model_disabled_providers WHERE api_key_id = ? AND model = ? AND provider_id = ?`)
		if _, err := r.db.sql.ExecContext(ctx, q, keyID, model, provider); err != nil {
			return fmt.Errorf("store: enable model provider: %w", err)
		}
		return nil
	}
	q := r.db.rebind(`INSERT INTO api_key_model_disabled_providers (api_key_id, model, provider_id, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (api_key_id, model, provider_id) DO UPDATE SET updated_at = excluded.updated_at`)
	if _, err := r.db.sql.ExecContext(ctx, q, keyID, model, provider, formatTime(time.Now())); err != nil {
		return fmt.Errorf("store: disable model provider: %w", err)
	}
	return nil
}

// SetModelFinalFallback stores whether the chain's last-resort fallback may
// run for a key and model. A write is an explicit choice, so on stores
// FinalFallbackChosen, never FinalFallbackKept.
func (r *APIKeyRepo) SetModelFinalFallback(ctx context.Context, keyID, model string, on bool) error {
	flag := FinalFallbackOff
	if on {
		flag = FinalFallbackChosen
	}
	q := r.db.rebind(`INSERT INTO api_key_model_options (api_key_id, model, final_fallback, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (api_key_id, model) DO UPDATE SET final_fallback = excluded.final_fallback, updated_at = excluded.updated_at`)
	if _, err := r.db.sql.ExecContext(ctx, q, keyID, NormalizeDiscountModel(model), flag, formatTime(time.Now())); err != nil {
		return fmt.Errorf("store: set model final fallback: %w", err)
	}
	return nil
}
