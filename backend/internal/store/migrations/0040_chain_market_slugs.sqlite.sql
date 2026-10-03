ALTER TABLE chains ADD COLUMN market_slugs TEXT NOT NULL DEFAULT '[]';
DROP TABLE IF EXISTS model_market_bindings;
