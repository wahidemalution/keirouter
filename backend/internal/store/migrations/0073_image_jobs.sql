-- Portal image generation jobs. owner_sub is the portal user's Google subject;
-- every read is scoped by it. Timestamps are RFC3339 UTC text like the rest of
-- the store, so string comparison orders them. expires_at is set when the job
-- finishes (finished_at + 24h).
CREATE TABLE IF NOT EXISTS image_jobs (
  id TEXT PRIMARY KEY,
  owner_sub TEXT NOT NULL,
  key_id TEXT NOT NULL DEFAULT '',
  tenant_id TEXT NOT NULL DEFAULT '',
  project_id TEXT NOT NULL DEFAULT '',
  chain_id TEXT NOT NULL DEFAULT '',
  prompt TEXT NOT NULL DEFAULT '',
  params TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  error TEXT NOT NULL DEFAULT '',
  image_count INTEGER NOT NULL DEFAULT 0,
  cost_micro BIGINT NOT NULL DEFAULT 0,
  upstream_cost_micro BIGINT NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  finished_at TEXT,
  expires_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_image_jobs_owner_created ON image_jobs (owner_sub, created_at);
CREATE INDEX IF NOT EXISTS idx_image_jobs_expires ON image_jobs (expires_at);
CREATE TABLE IF NOT EXISTS image_files (
  id TEXT PRIMARY KEY,
  job_id TEXT NOT NULL,
  path TEXT NOT NULL,
  mime TEXT NOT NULL DEFAULT '',
  bytes BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_image_files_job ON image_files (job_id);
