-- Append-only ledger of manual token limit adjustments on token package keys,
-- the token counterpart of key_limit_adjustments. delta_tokens is signed: an
-- adjustment can raise or lower the limit. used_tokens is the key's usage at
-- the moment of the change, so tokens left before/after can be derived.
CREATE TABLE IF NOT EXISTS key_token_adjustments (
  id                   TEXT PRIMARY KEY,
  tenant_id            TEXT NOT NULL,
  key_id               TEXT NOT NULL,
  budget_id            TEXT NOT NULL,
  delta_tokens         BIGINT NOT NULL,
  reason               TEXT NOT NULL DEFAULT '',
  limit_before_tokens  BIGINT NOT NULL,
  limit_after_tokens   BIGINT NOT NULL,
  used_tokens          BIGINT NOT NULL,
  idempotency_key      TEXT NOT NULL DEFAULT '',
  actor                TEXT NOT NULL DEFAULT '',
  created_at           TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_key_token_adjustments_key ON key_token_adjustments(key_id, created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_key_token_adjustments_idem
  ON key_token_adjustments(key_id, idempotency_key) WHERE idempotency_key <> '';
