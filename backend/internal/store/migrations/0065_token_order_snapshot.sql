-- Token orders copy the package at checkout so fulfilment grants what was
-- bought, even if the package is edited or deleted before payment. Orders
-- made before this keep token_kind = '' and fall back to the live package.
ALTER TABLE payment_orders ADD COLUMN token_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE payment_orders ADD COLUMN token_amount BIGINT NOT NULL DEFAULT 0;
ALTER TABLE payment_orders ADD COLUMN token_days INTEGER NOT NULL DEFAULT 0;
