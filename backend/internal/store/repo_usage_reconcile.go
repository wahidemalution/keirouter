package store

import (
	"context"
	"fmt"
	"time"
)

// ReconcileRow is a usage row billed provisionally, waiting for the provider's
// usage log to supply its real cost.
type ReconcileRow struct {
	ID, TenantID, ProjectID, APIKeyID, ChainID, Model, UpstreamRequestID string
	PromptTokens, CompletionTokens                                       int
	CostNanos, UpstreamCostNanos                                         int64
	InputCostNanos, CachedCostNanos, CacheWriteCostNanos                 int64
	OutputCostNanos, ReasoningCostNanos                                  int64
	CreatedAt                                                            time.Time
}

// ReconcileSettlement is the new cost of one row. Status is the row's new
// reconcile_status: reconciled, over_ceiling, or unverified (costs unchanged).
// UpstreamRequestID is the log entry that priced the row; it is stored so a
// later pass re-reading that entry does not count it as an orphan.
type ReconcileSettlement struct {
	ID, Status, UpstreamRequestID                        string
	CostNanos, UpstreamCostNanos                         int64
	InputCostNanos, CachedCostNanos, CacheWriteCostNanos int64
	OutputCostNanos, ReasoningCostNanos                  int64
}

// CostLookupOrphan is a provider usage-log entry no usage row accounts for:
// a cost the operator paid with no customer row to bill.
type CostLookupOrphan struct {
	Provider, RequestID, Model     string
	PromptTokens, CompletionTokens int
	Quota                          int64
	CreatedAt                      time.Time
}

// ListPendingReconcile returns up to limit pending rows of provider created
// at or after since, oldest first.
func (r *UsageRepo) ListPendingReconcile(ctx context.Context, provider string, since time.Time, limit int) ([]ReconcileRow, error) {
	q := r.db.rebind(`SELECT id, tenant_id, COALESCE(project_id, ''), COALESCE(api_key_id, ''), COALESCE(chain_id, ''), model, COALESCE(upstream_request_id, ''),
		prompt_tokens, completion_tokens, cost_nanos, upstream_cost_nanos,
		input_cost_nanos, cached_cost_nanos, cache_write_cost_nanos, output_cost_nanos, reasoning_cost_nanos, created_at
		FROM usage_records WHERE provider = ? AND reconcile_status = 'pending' AND created_at >= ?
		ORDER BY created_at, id LIMIT ?`)
	rows, err := r.db.sql.QueryContext(ctx, q, provider, formatTime(since), limit)
	if err != nil {
		return nil, fmt.Errorf("store: list pending reconcile: %w", err)
	}
	defer rows.Close()
	var out []ReconcileRow
	for rows.Next() {
		var row ReconcileRow
		var created string
		if err := rows.Scan(&row.ID, &row.TenantID, &row.ProjectID, &row.APIKeyID, &row.ChainID, &row.Model,
			&row.UpstreamRequestID, &row.PromptTokens, &row.CompletionTokens, &row.CostNanos, &row.UpstreamCostNanos,
			&row.InputCostNanos, &row.CachedCostNanos, &row.CacheWriteCostNanos, &row.OutputCostNanos,
			&row.ReasoningCostNanos, &created); err != nil {
			return nil, fmt.Errorf("store: scan pending reconcile: %w", err)
		}
		row.CreatedAt = parseTime(created)
		out = append(out, row)
	}
	return out, rows.Err()
}

// SettleReconciled writes each settlement in one transaction. A row is only
// updated while still pending, so settling the same row twice bills once.
// It returns the ids that were updated.
func (r *UsageRepo) SettleReconciled(ctx context.Context, settlements []ReconcileSettlement) ([]string, error) {
	if len(settlements) == 0 {
		return nil, nil
	}
	tx, err := r.db.sql.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: settle reconcile: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	q := r.db.rebind(`UPDATE usage_records SET
		cost_nanos = ?, cost_micros = ?, upstream_cost_nanos = ?, upstream_cost_micros = ?,
		input_cost_nanos = ?, cached_cost_nanos = ?, cache_write_cost_nanos = ?,
		output_cost_nanos = ?, reasoning_cost_nanos = ?,
		pricing_source = 'upstream_log', pricing_match_kind = 'upstream_log', reconcile_status = ?,
		upstream_request_id = CASE WHEN ? = '' THEN upstream_request_id ELSE ? END
		WHERE id = ? AND reconcile_status = 'pending'`)
	var applied []string
	for _, s := range settlements {
		res, err := tx.ExecContext(ctx, q,
			s.CostNanos, nanosToMicros(s.CostNanos), s.UpstreamCostNanos, nanosToMicros(s.UpstreamCostNanos),
			s.InputCostNanos, s.CachedCostNanos, s.CacheWriteCostNanos, s.OutputCostNanos, s.ReasoningCostNanos,
			s.Status, s.UpstreamRequestID, s.UpstreamRequestID, s.ID)
		if err != nil {
			return nil, fmt.Errorf("store: settle reconcile %s: %w", s.ID, err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			applied = append(applied, s.ID)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("store: commit reconcile: %w", err)
	}
	return applied, nil
}

// ExpirePendingReconcile marks pending rows of provider created before
// before as unverified; they keep their provisional (ceiling) bill.
func (r *UsageRepo) ExpirePendingReconcile(ctx context.Context, provider string, before time.Time) (int64, error) {
	q := r.db.rebind(`UPDATE usage_records SET reconcile_status = 'unverified'
		WHERE provider = ? AND reconcile_status = 'pending' AND created_at < ?`)
	res, err := r.db.sql.ExecContext(ctx, q, provider, formatTime(before))
	if err != nil {
		return 0, fmt.Errorf("store: expire pending reconcile: %w", err)
	}
	return res.RowsAffected()
}

// InsertCostLookupOrphans stores orphans, skipping request ids already
// stored, and returns how many were new.
func (r *UsageRepo) InsertCostLookupOrphans(ctx context.Context, orphans []CostLookupOrphan) (int, error) {
	q := r.db.rebind(`INSERT INTO cost_lookup_orphans
		(provider, request_id, model, prompt_tokens, completion_tokens, quota, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT (provider, request_id) DO NOTHING`)
	inserted := 0
	for _, o := range orphans {
		res, err := r.db.sql.ExecContext(ctx, q, o.Provider, o.RequestID, o.Model,
			o.PromptTokens, o.CompletionTokens, o.Quota, formatTime(o.CreatedAt))
		if err != nil {
			return inserted, fmt.Errorf("store: insert cost lookup orphan: %w", err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			inserted++
		}
	}
	return inserted, nil
}

// KnownUpstreamRequestIDs returns which of ids are already stored on a usage
// row of provider.
func (r *UsageRepo) KnownUpstreamRequestIDs(ctx context.Context, provider string, ids []string) (map[string]bool, error) {
	known := map[string]bool{}
	const chunk = 500
	for i := 0; i < len(ids); i += chunk {
		part := ids[i:min(i+chunk, len(ids))]
		args := make([]any, 0, len(part)+1)
		args = append(args, provider)
		marks := make([]byte, 0, len(part)*2)
		for j, id := range part {
			if j > 0 {
				marks = append(marks, ',')
			}
			marks = append(marks, '?')
			args = append(args, id)
		}
		q := r.db.rebind(`SELECT upstream_request_id FROM usage_records WHERE provider = ? AND upstream_request_id IN (` + string(marks) + `)`)
		rows, err := r.db.sql.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, fmt.Errorf("store: known upstream request ids: %w", err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, fmt.Errorf("store: scan upstream request id: %w", err)
			}
			known[id] = true
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return known, nil
}

// OverCeilingGroup counts rows of one chain and model whose logged cost
// exceeded the step's ceiling price.
type OverCeilingGroup struct {
	ChainID string `json:"chain_id"`
	Model   string `json:"model"`
	Count   int    `json:"count"`
}

// ReconcileDaySummary aggregates one provider's reconciliation over [from, to).
type ReconcileDaySummary struct {
	UpstreamCostNanos               int64
	Pending, Reconciled, Unverified int
	OverCeiling                     []OverCeilingGroup
	OrphanCount                     int
	OrphanQuota                     int64
}

// ReconcileDaySummary sums the recorded upstream cost and counts rows by
// reconcile status, plus orphans, for provider over [from, to).
func (r *UsageRepo) ReconcileDaySummary(ctx context.Context, provider string, from, to time.Time) (ReconcileDaySummary, error) {
	var out ReconcileDaySummary
	f, t := formatTime(from), formatTime(to)
	q := r.db.rebind(`SELECT COALESCE(SUM(upstream_cost_nanos), 0),
		COALESCE(SUM(CASE WHEN reconcile_status = 'pending' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN reconcile_status IN ('reconciled', 'over_ceiling') THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN reconcile_status = 'unverified' THEN 1 ELSE 0 END), 0)
		FROM usage_records WHERE provider = ? AND created_at >= ? AND created_at < ?`)
	if err := r.db.sql.QueryRowContext(ctx, q, provider, f, t).Scan(&out.UpstreamCostNanos, &out.Pending, &out.Reconciled, &out.Unverified); err != nil {
		return out, fmt.Errorf("store: reconcile summary: %w", err)
	}
	q = r.db.rebind(`SELECT COALESCE(chain_id, ''), model, COUNT(*) FROM usage_records
		WHERE provider = ? AND reconcile_status = 'over_ceiling' AND created_at >= ? AND created_at < ?
		GROUP BY COALESCE(chain_id, ''), model ORDER BY COALESCE(chain_id, ''), model`)
	rows, err := r.db.sql.QueryContext(ctx, q, provider, f, t)
	if err != nil {
		return out, fmt.Errorf("store: over ceiling groups: %w", err)
	}
	for rows.Next() {
		var g OverCeilingGroup
		if err := rows.Scan(&g.ChainID, &g.Model, &g.Count); err != nil {
			rows.Close()
			return out, fmt.Errorf("store: scan over ceiling: %w", err)
		}
		out.OverCeiling = append(out.OverCeiling, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	q = r.db.rebind(`SELECT COUNT(*), COALESCE(SUM(quota), 0) FROM cost_lookup_orphans
		WHERE provider = ? AND created_at >= ? AND created_at < ?`)
	if err := r.db.sql.QueryRowContext(ctx, q, provider, f, t).Scan(&out.OrphanCount, &out.OrphanQuota); err != nil {
		return out, fmt.Errorf("store: orphan summary: %w", err)
	}
	return out, nil
}

// TryAdvisoryLock takes a session-level Postgres advisory lock on a dedicated
// connection without waiting. ok is false when another session holds it.
// SQLite runs in one process, so the lock is always granted there.
func (db *DB) TryAdvisoryLock(ctx context.Context, id int64) (release func(), ok bool, err error) {
	if db.dialect != DialectPostgres {
		return func() {}, true, nil
	}
	conn, err := db.sql.Conn(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("store: advisory lock conn: %w", err)
	}
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", id).Scan(&ok); err != nil {
		_ = conn.Close()
		return nil, false, fmt.Errorf("store: advisory lock: %w", err)
	}
	if !ok {
		_ = conn.Close()
		return nil, false, nil
	}
	return func() {
		_, _ = conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", id)
		_ = conn.Close()
	}, true, nil
}
