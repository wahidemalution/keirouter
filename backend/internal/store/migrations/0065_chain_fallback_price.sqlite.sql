ALTER TABLE chains ADD COLUMN fallback_input_per_m REAL NOT NULL DEFAULT 0;
ALTER TABLE chains ADD COLUMN fallback_output_per_m REAL NOT NULL DEFAULT 0;
ALTER TABLE chains ADD COLUMN fallback_cache_read_per_m REAL;
ALTER TABLE chains ADD COLUMN fallback_cache_write_per_m REAL;
ALTER TABLE chains ADD COLUMN fallback_markup_percent REAL;
