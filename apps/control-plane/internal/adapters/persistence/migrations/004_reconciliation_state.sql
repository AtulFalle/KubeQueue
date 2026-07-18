ALTER TABLE jobs ADD COLUMN management_mode TEXT NOT NULL DEFAULT 'MANAGED';
ALTER TABLE jobs ADD COLUMN sync_status TEXT NOT NULL DEFAULT 'PENDING';
ALTER TABLE jobs ADD COLUMN resource_version TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN last_seen_at TEXT NULL;
ALTER TABLE jobs ADD COLUMN observed_at TEXT NULL;
ALTER TABLE jobs ADD COLUMN observed_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN observed_message TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN pending_action TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN last_error TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN reconcile_retries INTEGER NOT NULL DEFAULT 0;
ALTER TABLE jobs ADD COLUMN next_reconcile_at TEXT NULL;
ALTER TABLE jobs ADD COLUMN archived_at TEXT NULL;

UPDATE jobs
SET management_mode = 'OBSERVED'
WHERE id IN (SELECT job_id FROM job_events WHERE type = 'JOB_ADOPTED');

UPDATE jobs SET sync_status = 'STALE' WHERE kubernetes_uid <> '';

CREATE INDEX IF NOT EXISTS jobs_reconciliation_due
  ON jobs (sync_status, next_reconcile_at);
