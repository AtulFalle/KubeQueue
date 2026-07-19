ALTER TABLE jobs ADD COLUMN idempotency_key TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX IF NOT EXISTS jobs_creator_idempotency_key
  ON jobs (creator_principal_id, idempotency_key)
  WHERE creator_principal_id IS NOT NULL AND idempotency_key <> '';
