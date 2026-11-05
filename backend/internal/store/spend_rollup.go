package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// usage_spend_daily holds, per budget scope and UTC day, the same two sums
// the budget engine needs from usage_records: cost_nanos of rows with
// pricing_backfilled = 0, and prompt_tokens + completion_tokens of every row.
// Database triggers update it in the same transaction as each insert, update
// or delete on usage_records, so a budget check reads a few dozen day rows
// instead of every request a key ever made.
const spendRollupTable = "usage_spend_daily"

// spendRollupContribSQL expands each usage row into its contribution to the
// tenant, project and api_key scopes. Rebuild and verification share it, so
// both use exactly the rule the triggers apply.
const spendRollupContribSQL = `
SELECT s.kind AS scope_kind,
       CASE s.kind WHEN 'tenant' THEN u.tenant_id WHEN 'project' THEN u.project_id ELSE u.api_key_id END AS scope_id,
       substr(u.created_at, 1, 10) AS day,
       CASE WHEN u.pricing_backfilled = 0 THEN u.cost_nanos ELSE 0 END AS cost_nanos,
       u.prompt_tokens + u.completion_tokens AS tokens
FROM usage_records u
CROSS JOIN (SELECT 'tenant' AS kind UNION ALL SELECT 'project' UNION ALL SELECT 'api_key') s`

const spendRollupRebuildSQL = `
INSERT INTO usage_spend_daily (scope_kind, scope_id, day, cost_nanos, tokens)
SELECT scope_kind, scope_id, day, SUM(cost_nanos), SUM(tokens)
FROM (` + spendRollupContribSQL + `) c
WHERE scope_id IS NOT NULL
GROUP BY scope_kind, scope_id, day`

// spendRollupVerifySQL counts (scope, day) groups where the rollup and the
// usage rows disagree. Missing rows count as zero on either side.
const spendRollupVerifySQL = `
SELECT COUNT(*) FROM (
    SELECT scope_kind, scope_id, day
    FROM (
        SELECT scope_kind, scope_id, day, cost_nanos, tokens FROM usage_spend_daily
        UNION ALL
        SELECT scope_kind, scope_id, day, -cost_nanos, -tokens
        FROM (` + spendRollupContribSQL + `) c
        WHERE scope_id IS NOT NULL
    ) d
    GROUP BY scope_kind, scope_id, day
    HAVING SUM(cost_nanos) <> 0 OR SUM(tokens) <> 0
) m`

// skipSpendRollupSetting lets a transaction that rebuilds the rollup anyway
// (restore) skip the per-row trigger work on Postgres.
const skipSpendRollupSetting = "keirouter.skip_spend_rollup"

// postgresSpendRollupUpsert applies one statement's change per (scope, day);
// %s yields the contributing rows from the trigger's transition tables.
const postgresSpendRollupUpsert = `
    INSERT INTO usage_spend_daily AS d (scope_kind, scope_id, day, cost_nanos, tokens)
    SELECT kind, id, day, SUM(c), SUM(t)
    FROM (%s) x
    WHERE id IS NOT NULL
    GROUP BY kind, id, day
    HAVING SUM(c) <> 0 OR SUM(t) <> 0
    ORDER BY kind, id, day
    ON CONFLICT (scope_kind, scope_id, day) DO UPDATE
        SET cost_nanos = d.cost_nanos + EXCLUDED.cost_nanos,
            tokens = d.tokens + EXCLUDED.tokens;`

// postgresSpendRollupRows expands a transition table into contributions;
// %[1]s is the table and %[2]s is "" or "-".
const postgresSpendRollupRows = `
        SELECT s.kind,
               CASE s.kind WHEN 'tenant' THEN r.tenant_id WHEN 'project' THEN r.project_id ELSE r.api_key_id END AS id,
               substr(r.created_at, 1, 10) AS day,
               %[2]s(CASE WHEN r.pricing_backfilled = 0 THEN r.cost_nanos ELSE 0 END) AS c,
               %[2]s(r.prompt_tokens + r.completion_tokens) AS t
        FROM %[1]s r CROSS JOIN (VALUES ('tenant'), ('project'), ('api_key')) AS s(kind)`

// Statement-level triggers apply one aggregated upsert per statement, so a
// batch insert touches each (scope, day) row once, and rows are locked in a
// fixed order so concurrent writers cannot deadlock on them.
var postgresSpendRollupInstall = []string{
	`DROP TRIGGER IF EXISTS usage_spend_rollup ON usage_records`,
	`CREATE OR REPLACE FUNCTION keirouter_spend_rollup_stmt() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
    IF current_setting('` + skipSpendRollupSetting + `', true) = 'on' THEN
        RETURN NULL;
    END IF;
    IF TG_OP = 'INSERT' THEN` +
		fmt.Sprintf(postgresSpendRollupUpsert, fmt.Sprintf(postgresSpendRollupRows, "new_rows", "")) + `
    ELSIF TG_OP = 'DELETE' THEN` +
		fmt.Sprintf(postgresSpendRollupUpsert, fmt.Sprintf(postgresSpendRollupRows, "old_rows", "-")) + `
    ELSE` +
		fmt.Sprintf(postgresSpendRollupUpsert, fmt.Sprintf(postgresSpendRollupRows, "new_rows", "")+
			"\n        UNION ALL"+fmt.Sprintf(postgresSpendRollupRows, "old_rows", "-")) + `
    END IF;
    RETURN NULL;
END
$fn$`,
	`CREATE OR REPLACE FUNCTION keirouter_spend_rollup_truncate() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
    DELETE FROM usage_spend_daily;
    RETURN NULL;
END
$fn$`,
	`DROP TRIGGER IF EXISTS usage_spend_rollup_ins ON usage_records`,
	`CREATE TRIGGER usage_spend_rollup_ins AFTER INSERT ON usage_records
REFERENCING NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE PROCEDURE keirouter_spend_rollup_stmt()`,
	`DROP TRIGGER IF EXISTS usage_spend_rollup_upd ON usage_records`,
	`CREATE TRIGGER usage_spend_rollup_upd AFTER UPDATE ON usage_records
REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE PROCEDURE keirouter_spend_rollup_stmt()`,
	`DROP TRIGGER IF EXISTS usage_spend_rollup_del ON usage_records`,
	`CREATE TRIGGER usage_spend_rollup_del AFTER DELETE ON usage_records
REFERENCING OLD TABLE AS old_rows FOR EACH STATEMENT EXECUTE PROCEDURE keirouter_spend_rollup_stmt()`,
	`DROP TRIGGER IF EXISTS usage_spend_rollup_truncate ON usage_records`,
	`CREATE TRIGGER usage_spend_rollup_truncate AFTER TRUNCATE ON usage_records
FOR EACH STATEMENT EXECUTE PROCEDURE keirouter_spend_rollup_truncate()`,
	`DROP FUNCTION IF EXISTS keirouter_spend_rollup_row()`,
	`DROP FUNCTION IF EXISTS keirouter_spend_rollup_add(TEXT, TEXT, TEXT, TEXT, BIGINT, BIGINT)`,
}

var postgresSpendRollupRemove = []string{
	`DROP TRIGGER IF EXISTS usage_spend_rollup ON usage_records`,
	`DROP TRIGGER IF EXISTS usage_spend_rollup_ins ON usage_records`,
	`DROP TRIGGER IF EXISTS usage_spend_rollup_upd ON usage_records`,
	`DROP TRIGGER IF EXISTS usage_spend_rollup_del ON usage_records`,
	`DROP TRIGGER IF EXISTS usage_spend_rollup_truncate ON usage_records`,
}

// sqliteSpendRollupApply is the upsert body shared by the SQLite triggers;
// %[1]s is NEW or OLD and %[2]s is "" or "-".
const sqliteSpendRollupApply = `
    INSERT INTO usage_spend_daily (scope_kind, scope_id, day, cost_nanos, tokens)
    SELECT v.kind, v.id, substr(%[1]s.created_at, 1, 10),
           %[2]s(CASE WHEN %[1]s.pricing_backfilled = 0 THEN %[1]s.cost_nanos ELSE 0 END),
           %[2]s(%[1]s.prompt_tokens + %[1]s.completion_tokens)
    FROM (SELECT 'tenant' AS kind, %[1]s.tenant_id AS id
          UNION ALL SELECT 'project', %[1]s.project_id
          UNION ALL SELECT 'api_key', %[1]s.api_key_id) AS v
    WHERE v.id IS NOT NULL
      AND ((CASE WHEN %[1]s.pricing_backfilled = 0 THEN %[1]s.cost_nanos ELSE 0 END) <> 0
           OR (%[1]s.prompt_tokens + %[1]s.completion_tokens) <> 0)
    ON CONFLICT (scope_kind, scope_id, day) DO UPDATE
        SET cost_nanos = cost_nanos + excluded.cost_nanos,
            tokens = tokens + excluded.tokens;`

var sqliteSpendRollupInstall = []string{
	`DROP TRIGGER IF EXISTS usage_spend_rollup_insert`,
	`DROP TRIGGER IF EXISTS usage_spend_rollup_delete`,
	`DROP TRIGGER IF EXISTS usage_spend_rollup_update`,
	`CREATE TRIGGER usage_spend_rollup_insert AFTER INSERT ON usage_records BEGIN` +
		fmt.Sprintf(sqliteSpendRollupApply, "NEW", "") + `
END`,
	`CREATE TRIGGER usage_spend_rollup_delete AFTER DELETE ON usage_records BEGIN` +
		fmt.Sprintf(sqliteSpendRollupApply, "OLD", "-") + `
END`,
	`CREATE TRIGGER usage_spend_rollup_update
AFTER UPDATE OF tenant_id, project_id, api_key_id, created_at, cost_nanos, pricing_backfilled, prompt_tokens, completion_tokens
ON usage_records BEGIN` +
		fmt.Sprintf(sqliteSpendRollupApply, "OLD", "-") +
		fmt.Sprintf(sqliteSpendRollupApply, "NEW", "") + `
END`,
}

var sqliteSpendRollupRemove = []string{
	`DROP TRIGGER IF EXISTS usage_spend_rollup_insert`,
	`DROP TRIGGER IF EXISTS usage_spend_rollup_delete`,
	`DROP TRIGGER IF EXISTS usage_spend_rollup_update`,
}

// SpendRollupEnabled reports whether spend queries read usage_spend_daily.
func (db *DB) SpendRollupEnabled() bool { return db.spendRollup.Load() }

// EnsureSpendRollup installs the triggers that maintain usage_spend_daily,
// checks the table against usage_records and rebuilds it if they differ. Only
// after that succeeds do spend queries switch to the rollup. With enabled
// false it removes the triggers and spend queries sum usage_records.
//
// Writes to usage_records are blocked for the duration, which is one scan of
// the table. It returns the number of (scope, day) groups that were wrong.
func (db *DB) EnsureSpendRollup(ctx context.Context, enabled bool) (int, error) {
	db.spendRollup.Store(false)
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: spend rollup: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmts := sqliteSpendRollupRemove
	if db.dialect == DialectPostgres {
		stmts = postgresSpendRollupRemove
	}
	if enabled {
		stmts = sqliteSpendRollupInstall
		if db.dialect == DialectPostgres {
			stmts = postgresSpendRollupInstall
		}
	}
	if db.dialect == DialectPostgres {
		// Hold off writers so the verify and rebuild below see every row the
		// triggers did not.
		if _, err := tx.ExecContext(ctx, `LOCK TABLE usage_records IN SHARE ROW EXCLUSIVE MODE`); err != nil {
			return 0, fmt.Errorf("store: spend rollup lock: %w", err)
		}
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return 0, fmt.Errorf("store: spend rollup triggers: %w", err)
		}
	}
	if !enabled {
		return 0, tx.Commit()
	}
	bad, err := verifySpendRollup(ctx, tx)
	if err != nil {
		return 0, err
	}
	if bad > 0 {
		if err := rebuildSpendRollup(ctx, tx); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: spend rollup commit: %w", err)
	}
	db.spendRollup.Store(true)
	return bad, nil
}

// CheckSpendRollup compares usage_spend_daily with usage_records in one
// consistent snapshot without blocking writers. If they differ it rebuilds
// the rollup under a write lock and returns how many groups were wrong.
func (db *DB) CheckSpendRollup(ctx context.Context) (int, error) {
	if !db.spendRollup.Load() {
		return 0, nil
	}
	opts := &sql.TxOptions{ReadOnly: true}
	if db.dialect == DialectPostgres {
		opts.Isolation = sql.LevelRepeatableRead
	}
	tx, err := db.sql.BeginTx(ctx, opts)
	if err != nil {
		return 0, fmt.Errorf("store: spend rollup check: %w", err)
	}
	bad, err := verifySpendRollup(ctx, tx)
	_ = tx.Rollback()
	if err != nil || bad == 0 {
		return 0, err
	}
	if _, err := db.EnsureSpendRollup(ctx, true); err != nil {
		return bad, err
	}
	return bad, nil
}

func verifySpendRollup(ctx context.Context, tx *sql.Tx) (int, error) {
	var bad int
	if err := tx.QueryRowContext(ctx, spendRollupVerifySQL).Scan(&bad); err != nil {
		return 0, fmt.Errorf("store: spend rollup verify: %w", err)
	}
	return bad, nil
}

func rebuildSpendRollup(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM `+spendRollupTable); err != nil {
		return fmt.Errorf("store: spend rollup clear: %w", err)
	}
	if _, err := tx.ExecContext(ctx, spendRollupRebuildSQL); err != nil {
		return fmt.Errorf("store: spend rollup rebuild: %w", err)
	}
	return nil
}

// spendQuery returns SQL yielding (cost_nanos, tokens) for one budget scope
// since the given time, with its arguments. With the rollup on, whole days
// come from usage_spend_daily and a since that falls inside a day adds that
// day's remaining usage rows. The sums equal the usage_records query exactly
// because the rollup buckets rows by the first 10 characters of created_at,
// the date part of the RFC 3339 timestamps the store writes.
func (r *UsageRepo) spendQuery(kind BudgetScope, column, scopeID string, since time.Time) (string, []any) {
	const sums = `SELECT CAST(COALESCE(SUM(c), 0) AS BIGINT) AS c, CAST(COALESCE(SUM(t), 0) AS BIGINT) AS t FROM (%s) s`
	raw := `SELECT CASE WHEN pricing_backfilled = 0 THEN cost_nanos ELSE 0 END AS c, prompt_tokens + completion_tokens AS t
		FROM usage_records WHERE ` + column + ` = ? AND created_at >= ?`
	if !r.db.spendRollup.Load() {
		return fmt.Sprintf(sums, raw), []any{scopeID, formatTime(since)}
	}
	rollup := `SELECT cost_nanos AS c, tokens AS t FROM usage_spend_daily WHERE scope_kind = ? AND scope_id = ?`
	if since.IsZero() {
		return fmt.Sprintf(sums, rollup), []any{string(kind), scopeID}
	}
	since = since.UTC()
	day := since.Format("2006-01-02")
	dayStart := time.Date(since.Year(), since.Month(), since.Day(), 0, 0, 0, 0, time.UTC)
	if since.Equal(dayStart) {
		return fmt.Sprintf(sums, rollup+` AND day >= ?`), []any{string(kind), scopeID, day}
	}
	edge := raw + ` AND created_at < ?`
	return fmt.Sprintf(sums, rollup+` AND day > ? UNION ALL `+edge),
		[]any{string(kind), scopeID, day, scopeID, formatTime(since), formatTime(dayStart.AddDate(0, 0, 1))}
}

// nanosToMicros is the budget rounding rule: half a micro rounds up.
func nanosToMicros(n int64) int64 { return (n + 500) / 1000 }
