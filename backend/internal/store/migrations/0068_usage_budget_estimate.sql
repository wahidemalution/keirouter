-- The budget engine's pre-dispatch estimate for the request, kept next to the
-- billed usage so the estimate can be checked against what the upstream
-- reported. 0 = no estimate was made (no meter, or an empty prompt).
ALTER TABLE usage_records ADD COLUMN est_prompt_tokens BIGINT NOT NULL DEFAULT 0;
ALTER TABLE usage_records ADD COLUMN est_cost_micros BIGINT NOT NULL DEFAULT 0;
