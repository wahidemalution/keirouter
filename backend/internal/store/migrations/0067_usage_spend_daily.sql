-- Per-day spend totals per budget scope, kept in step with usage_records by
-- triggers that the application installs at startup (EnsureSpendRollup).
-- Budget checks sum these rows instead of every usage row. cost_nanos counts
-- only rows with pricing_backfilled = 0, matching the budget spend rule.
CREATE TABLE IF NOT EXISTS usage_spend_daily (
    scope_kind TEXT NOT NULL,
    scope_id   TEXT NOT NULL,
    day        TEXT NOT NULL,
    cost_nanos BIGINT NOT NULL DEFAULT 0,
    tokens     BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (scope_kind, scope_id, day)
);
