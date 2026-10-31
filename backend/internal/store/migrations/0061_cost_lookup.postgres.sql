ALTER TABLE custom_providers ADD COLUMN IF NOT EXISTS cost_lookup TEXT NOT NULL DEFAULT '';
ALTER TABLE custom_providers ADD COLUMN IF NOT EXISTS cost_lookup_base_url TEXT NOT NULL DEFAULT '';
ALTER TABLE custom_providers ADD COLUMN IF NOT EXISTS cost_lookup_user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE custom_providers ADD COLUMN IF NOT EXISTS cost_lookup_token TEXT NOT NULL DEFAULT '';
ALTER TABLE custom_providers ADD COLUMN IF NOT EXISTS cost_lookup_token_name TEXT NOT NULL DEFAULT '';
ALTER TABLE custom_providers ADD COLUMN IF NOT EXISTS cost_lookup_status TEXT NOT NULL DEFAULT '';
ALTER TABLE usage_records ADD COLUMN IF NOT EXISTS upstream_request_id TEXT NOT NULL DEFAULT '';
ALTER TABLE usage_records ADD COLUMN IF NOT EXISTS reconcile_status TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_usage_reconcile ON usage_records(provider, reconcile_status, created_at);
CREATE INDEX IF NOT EXISTS idx_usage_upstream_request ON usage_records(provider, upstream_request_id);
CREATE TABLE IF NOT EXISTS cost_lookup_orphans (
    provider TEXT NOT NULL,
    request_id TEXT NOT NULL,
    model TEXT NOT NULL DEFAULT '',
    prompt_tokens INTEGER NOT NULL DEFAULT 0,
    completion_tokens INTEGER NOT NULL DEFAULT 0,
    quota BIGINT NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    PRIMARY KEY (provider, request_id)
);
CREATE INDEX IF NOT EXISTS idx_cost_lookup_orphans_time ON cost_lookup_orphans(provider, created_at);
