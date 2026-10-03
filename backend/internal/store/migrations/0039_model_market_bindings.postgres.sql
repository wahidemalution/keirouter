CREATE TABLE IF NOT EXISTS model_market_bindings (
    tenant_id   TEXT NOT NULL,
    provider_id TEXT NOT NULL,
    model_id    TEXT NOT NULL,
    market_slug TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
    PRIMARY KEY (tenant_id, provider_id, model_id)
);