-- Operator-managed display provider categories for the public model catalog.
-- id is the slug shown as provider_id; label is the human display name.
CREATE TABLE IF NOT EXISTS provider_categories (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL,
    label      TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_provider_categories_tenant
    ON provider_categories(tenant_id);
