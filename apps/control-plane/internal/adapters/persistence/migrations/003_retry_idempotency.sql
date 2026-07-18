CREATE UNIQUE INDEX IF NOT EXISTS jobs_single_retry_per_attempt
  ON jobs (parent_id) WHERE parent_id <> '';
