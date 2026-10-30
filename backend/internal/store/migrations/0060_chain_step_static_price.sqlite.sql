ALTER TABLE chain_steps ADD COLUMN static_input_per_m REAL NOT NULL DEFAULT 0;
ALTER TABLE chain_steps ADD COLUMN static_output_per_m REAL NOT NULL DEFAULT 0;
ALTER TABLE chain_steps ADD COLUMN static_cache_read_per_m REAL;
ALTER TABLE chain_steps ADD COLUMN static_cache_write_per_m REAL;
ALTER TABLE chain_steps ADD COLUMN markup_percent REAL;
