-- Per-package key rate limits for token packages. NULL follows the token
-- plan. Orders copy the package's limits at checkout like the other package
-- fields, and a subscription stores the limits its purchases add up to (see
-- tokenpkg.Apply); NULL there also follows the plan.
ALTER TABLE token_packages ADD COLUMN rpm_limit INTEGER;
ALTER TABLE token_packages ADD COLUMN concurrency_limit INTEGER;
ALTER TABLE token_subscriptions ADD COLUMN rpm_limit INTEGER;
ALTER TABLE token_subscriptions ADD COLUMN concurrency_limit INTEGER;
ALTER TABLE payment_orders ADD COLUMN token_rpm_limit INTEGER;
ALTER TABLE payment_orders ADD COLUMN token_concurrency_limit INTEGER;
