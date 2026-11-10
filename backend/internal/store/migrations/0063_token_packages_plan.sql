-- Dedicated plan carrying the rate limits (RPM, concurrency) of token package
-- keys. Fixed RFC3339 literals keep the statement portable across SQLite and
-- Postgres. The admin edits the limits on the Plans page.
INSERT INTO plans (id, tenant_id, name, description, limit_micros, limit_tokens, rpm_limit, tpm_limit, concurrency_limit, period, alert_pct, hard_cutoff, allowed_models, created_at, updated_at)
VALUES ('plan-token-packages', 'default', 'Paket Token', 'Rate limits for token package keys', 0, 0, 60, 0, 3, 'total', 80, 1, 'gpt-6-sol,gpt-6-terra,gpt-6-luna', '2026-11-10T00:00:00Z', '2026-11-10T00:00:00Z');
