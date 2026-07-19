CREATE INDEX IF NOT EXISTS jobs_project_sync_updated
  ON jobs (project_id, sync_status, updated_at, id);

CREATE INDEX IF NOT EXISTS jobs_project_desired_created
  ON jobs (project_id, desired_state, created_at, id);

CREATE INDEX IF NOT EXISTS jobs_project_observed_created
  ON jobs (project_id, observed_state, created_at, id);

CREATE INDEX IF NOT EXISTS jobs_project_namespace_created
  ON jobs (project_id, namespace, created_at, id);

CREATE INDEX IF NOT EXISTS jobs_project_created
  ON jobs (project_id, created_at, id);

CREATE INDEX IF NOT EXISTS jobs_project_updated
  ON jobs (project_id, updated_at, id);

CREATE INDEX IF NOT EXISTS job_events_job_cursor
  ON job_events (job_id, id DESC);
