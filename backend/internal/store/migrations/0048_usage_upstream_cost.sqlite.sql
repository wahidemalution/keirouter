ALTER TABLE usage_records ADD COLUMN upstream_cost_nanos INTEGER NOT NULL DEFAULT 0;
ALTER TABLE usage_records ADD COLUMN upstream_cost_micros INTEGER NOT NULL DEFAULT 0;
