-- Prepaid token packages for the portal. A 'tokens' package grants a token
-- allowance valid for `days`; a 'days' package only extends an active one.
CREATE TABLE IF NOT EXISTS token_packages (
  id         TEXT PRIMARY KEY,
  tenant_id  TEXT NOT NULL,
  name       TEXT NOT NULL,
  kind       TEXT NOT NULL,
  tokens     BIGINT NOT NULL DEFAULT 0,
  days       INTEGER NOT NULL DEFAULT 15,
  price_idr  BIGINT NOT NULL DEFAULT 0,
  enabled    INTEGER NOT NULL DEFAULT 0,
  featured   INTEGER NOT NULL DEFAULT 0,
  sort       INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_token_packages_sort ON token_packages(sort, price_idr);

-- One token key per portal user. sealed_dek / sealed_ct hold the encrypted
-- plaintext key for reveal, like portal_users.
CREATE TABLE IF NOT EXISTS token_subscriptions (
  google_sub TEXT PRIMARY KEY REFERENCES portal_users(google_sub) ON DELETE CASCADE,
  key_id     TEXT NOT NULL UNIQUE REFERENCES api_keys(id) ON DELETE CASCADE,
  sealed_dek TEXT NOT NULL DEFAULT '',
  sealed_ct  TEXT NOT NULL DEFAULT '',
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

ALTER TABLE payment_orders ADD COLUMN token_package_id TEXT NOT NULL DEFAULT '';

INSERT INTO token_packages (id, tenant_id, name, kind, tokens, days, price_idr, enabled, featured, sort, created_at, updated_at) VALUES
  ('tp-20m',     'default', '20M Token',  'tokens',    20000000, 15,    59000, 0, 0, 10, '2026-11-10T00:00:00Z', '2026-11-10T00:00:00Z'),
  ('tp-50m',     'default', '50M Token',  'tokens',    50000000, 15,   139000, 0, 0, 20, '2026-11-10T00:00:00Z', '2026-11-10T00:00:00Z'),
  ('tp-100m',    'default', '100M Token', 'tokens',   100000000, 15,   259000, 0, 1, 30, '2026-11-10T00:00:00Z', '2026-11-10T00:00:00Z'),
  ('tp-250m',    'default', '250M Token', 'tokens',   250000000, 15,   599000, 0, 0, 40, '2026-11-10T00:00:00Z', '2026-11-10T00:00:00Z'),
  ('tp-500m',    'default', '500M Token', 'tokens',   500000000, 15,  1149000, 0, 0, 50, '2026-11-10T00:00:00Z', '2026-11-10T00:00:00Z'),
  ('tp-1b',      'default', '1B Token',   'tokens',  1000000000, 15,  2199000, 0, 0, 60, '2026-11-10T00:00:00Z', '2026-11-10T00:00:00Z'),
  ('tp-days-15', 'default', '+15 Hari',   'days',             0, 15,    15000, 0, 0, 70, '2026-11-10T00:00:00Z', '2026-11-10T00:00:00Z');
