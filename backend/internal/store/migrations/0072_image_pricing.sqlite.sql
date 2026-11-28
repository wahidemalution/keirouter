ALTER TABLE chain_steps ADD COLUMN static_per_image REAL NOT NULL DEFAULT 0;
ALTER TABLE chain_steps ADD COLUMN static_per_image_2k REAL;
ALTER TABLE usage_records ADD COLUMN image_count INTEGER NOT NULL DEFAULT 0;
