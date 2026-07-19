CREATE TABLE IF NOT EXISTS setup_bootstrap (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  safe_prefix TEXT NOT NULL,
  keyed_digest TEXT NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('AVAILABLE', 'PENDING', 'REVOKED')),
  claim_fingerprint TEXT NULL,
  created_at TEXT NOT NULL,
  claimed_at TEXT NULL,
  revoked_at TEXT NULL
);

CREATE TABLE IF NOT EXISTS setup_claims (
  id TEXT PRIMARY KEY,
  installation_id TEXT NOT NULL REFERENCES installations(id),
  identity_provider_id TEXT NOT NULL REFERENCES identity_providers(id),
  mapping_type TEXT NOT NULL CHECK (mapping_type IN ('GROUP', 'DOMAIN')),
  mapping_value TEXT NOT NULL,
  project_id TEXT NOT NULL REFERENCES projects(id),
  status TEXT NOT NULL CHECK (status IN ('PENDING_LOGIN', 'COMPLETED')),
  completed_by_principal_id TEXT NULL REFERENCES principals(id),
  created_at TEXT NOT NULL,
  completed_at TEXT NULL,
  UNIQUE (installation_id)
);

CREATE TABLE IF NOT EXISTS installation_admission_policy (
  installation_id TEXT PRIMARY KEY REFERENCES installations(id),
  global_concurrency INTEGER NOT NULL CHECK (global_concurrency BETWEEN 1 AND 10000),
  namespace_concurrency INTEGER NOT NULL CHECK (namespace_concurrency BETWEEN 1 AND 10000),
  queue_capacity INTEGER NOT NULL CHECK (queue_capacity BETWEEN 1 AND 1000000),
  minimum_priority INTEGER NOT NULL CHECK (minimum_priority BETWEEN -1000 AND 1000),
  maximum_priority INTEGER NOT NULL CHECK (maximum_priority BETWEEN -1000 AND 1000),
  maximum_delay_seconds INTEGER NOT NULL CHECK (maximum_delay_seconds BETWEEN 0 AND 31536000),
  CHECK (namespace_concurrency <= global_concurrency),
  CHECK (minimum_priority <= maximum_priority)
);

CREATE TABLE IF NOT EXISTS project_quotas (
  project_id TEXT PRIMARY KEY REFERENCES projects(id),
  maximum_running_jobs INTEGER NOT NULL CHECK (maximum_running_jobs BETWEEN 1 AND 10000),
  maximum_queued_jobs INTEGER NOT NULL CHECK (maximum_queued_jobs BETWEEN 1 AND 1000000)
);
