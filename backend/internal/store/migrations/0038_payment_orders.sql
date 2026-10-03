-- Payment gateway orders for portal credit top-ups. The FX rate and the
-- credited micro-USD are locked at creation so the webhook and manual-approve
-- paths credit exactly the quoted amount with no re-conversion. Status
-- transitions are guarded to make crediting exactly-once.
CREATE TABLE IF NOT EXISTS payment_orders (
  id                   TEXT PRIMARY KEY,
  tenant_id            TEXT NOT NULL,
  key_id               TEXT NOT NULL,
  google_sub           TEXT NOT NULL DEFAULT '',
  amount_idr           BIGINT NOT NULL,
  credit_micros        BIGINT NOT NULL,
  fx_rate_micros       BIGINT NOT NULL,
  status               TEXT NOT NULL,
  provider             TEXT NOT NULL DEFAULT 'sumopod',
  provider_payment_id  TEXT NOT NULL DEFAULT '',
  payment_link_url     TEXT NOT NULL DEFAULT '',
  idempotency_key      TEXT NOT NULL DEFAULT '',
  actor                TEXT NOT NULL DEFAULT '',
  reason               TEXT NOT NULL DEFAULT '',
  created_at           TEXT NOT NULL,
  updated_at           TEXT NOT NULL,
  paid_at              TEXT NOT NULL DEFAULT '',
  expires_at           TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_payment_orders_status ON payment_orders(status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_payment_orders_key ON payment_orders(key_id, created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_payment_orders_provider_id
  ON payment_orders(provider, provider_payment_id) WHERE provider_payment_id <> '';
CREATE UNIQUE INDEX IF NOT EXISTS idx_payment_orders_idem
  ON payment_orders(idempotency_key) WHERE idempotency_key <> '';
