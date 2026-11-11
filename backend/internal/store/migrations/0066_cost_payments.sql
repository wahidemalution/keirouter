-- Operating costs become a payment history: one row per payment, dated when
-- it was recorded. hosting_costs (recurring monthly costs) held no production
-- data and is replaced. A USD payment keeps the USD->IDR rate of the moment it
-- was saved, so amount_idr never moves with later rates.
DROP TABLE IF EXISTS hosting_costs;
CREATE TABLE IF NOT EXISTS cost_payments (
  id                TEXT PRIMARY KEY,
  name              TEXT NOT NULL,
  category          TEXT NOT NULL,
  provider          TEXT NOT NULL DEFAULT '',
  currency          TEXT NOT NULL,
  amount_usd_cents  BIGINT NOT NULL DEFAULT 0,
  usd_idr_rate      DOUBLE PRECISION NOT NULL DEFAULT 0,
  amount_idr        BIGINT NOT NULL,
  payment_method    TEXT NOT NULL,
  paid_at           TEXT NOT NULL,
  note              TEXT NOT NULL DEFAULT '',
  created_at        TEXT NOT NULL,
  updated_at        TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_cost_payments_paid_at ON cost_payments(paid_at);
