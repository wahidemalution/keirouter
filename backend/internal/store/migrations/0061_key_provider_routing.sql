-- Admin ceiling: providers this key may use. No rows = all providers.
CREATE TABLE IF NOT EXISTS api_key_allowed_providers (
    api_key_id  TEXT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    provider_id TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    PRIMARY KEY (api_key_id, provider_id)
);

-- Providers the customer turned off for one model. Storing the "off" set
-- means a provider the admin adds to a chain later is on by default.
CREATE TABLE IF NOT EXISTS api_key_model_disabled_providers (
    api_key_id  TEXT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    model       TEXT NOT NULL,
    provider_id TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
    PRIMARY KEY (api_key_id, model, provider_id)
);

-- Per-model options. final_fallback: 0 or no row = off, 1 = turned on by the
-- customer or admin (exempt from the minimum discount), 2 = kept from before
-- per-key routing (runs as before: a minimum discount still removes it).
CREATE TABLE IF NOT EXISTS api_key_model_options (
    api_key_id     TEXT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    model          TEXT NOT NULL,
    final_fallback INTEGER NOT NULL DEFAULT 0,
    updated_at     TEXT NOT NULL,
    PRIMARY KEY (api_key_id, model)
);

-- Decision B: keys that exist now keep the fallback their chains use today,
-- with value 2 so a minimum discount still removes it as it does today.
-- model matches store.NormalizeDiscountModel (trimmed, lower-cased).
INSERT INTO api_key_model_options (api_key_id, model, final_fallback, updated_at)
SELECT k.id, LOWER(TRIM(c.name)), 2, '2026-11-03T00:00:00Z'
FROM api_keys k
JOIN chains c ON c.tenant_id = k.tenant_id
WHERE c.fallback_provider <> '' AND c.fallback_model <> ''
ON CONFLICT (api_key_id, model) DO NOTHING;
