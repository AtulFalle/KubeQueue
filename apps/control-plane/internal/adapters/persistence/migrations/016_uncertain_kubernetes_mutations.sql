CREATE TABLE IF NOT EXISTS reconciliation_mutations (
  job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  operation TEXT NOT NULL,
  request_identity TEXT NOT NULL,
  attempt_identity TEXT NOT NULL,
  attempt_id TEXT NOT NULL,
  generation BIGINT NOT NULL,
  state TEXT NOT NULL,
  error_class TEXT NOT NULL,
  started_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  observed_at TEXT NULL,
  PRIMARY KEY (job_id, operation, request_identity),
  CHECK (generation > 0),
  CHECK (state IN ('IN_FLIGHT', 'OBSERVATION_REQUIRED', 'SUCCEEDED', 'READY')),
  CHECK (LENGTH(operation) BETWEEN 1 AND 64),
  CHECK (LENGTH(attempt_identity) BETWEEN 1 AND 128),
  CHECK (LENGTH(request_identity) BETWEEN 1 AND 256),
  CHECK (LENGTH(attempt_id) BETWEEN 1 AND 64),
  CHECK (LENGTH(error_class) <= 64),
  CHECK (LENGTH(started_at) <= 40),
  CHECK (LENGTH(updated_at) <= 40),
  CHECK (observed_at IS NULL OR LENGTH(observed_at) <= 40)
);

CREATE INDEX IF NOT EXISTS idx_reconciliation_mutations_state
  ON reconciliation_mutations(state, updated_at);
