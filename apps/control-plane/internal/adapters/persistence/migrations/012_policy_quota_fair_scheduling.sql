ALTER TABLE projects ADD COLUMN scheduling_weight BIGINT NOT NULL DEFAULT 1
  CHECK (scheduling_weight BETWEEN 1 AND 1000000);
ALTER TABLE projects ADD COLUMN scheduling_version BIGINT NOT NULL DEFAULT 1
  CHECK (scheduling_version > 0);

CREATE TABLE IF NOT EXISTS policy_scopes (
  id TEXT PRIMARY KEY,
  installation_id TEXT NOT NULL REFERENCES installations(id),
  scope_key TEXT NOT NULL,
  scope_type TEXT NOT NULL CHECK (scope_type IN ('INSTALLATION', 'PROJECT', 'NAMESPACE')),
  project_id TEXT NULL REFERENCES projects(id),
  namespace TEXT NULL,
  current_version BIGINT NOT NULL CHECK (current_version > 0),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE (installation_id, scope_key),
  CHECK (
    (scope_type = 'INSTALLATION' AND project_id IS NULL AND namespace IS NULL) OR
    (scope_type = 'PROJECT' AND project_id IS NOT NULL AND namespace IS NULL) OR
    (scope_type = 'NAMESPACE' AND project_id IS NOT NULL AND namespace IS NOT NULL)
  )
);
CREATE INDEX IF NOT EXISTS policy_scopes_hierarchy
  ON policy_scopes (installation_id, scope_type, project_id, namespace);

CREATE TABLE IF NOT EXISTS policy_versions (
  policy_id TEXT NOT NULL REFERENCES policy_scopes(id) ON DELETE CASCADE,
  version BIGINT NOT NULL CHECK (version > 0),
  rules TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY (policy_id, version)
);

CREATE TABLE IF NOT EXISTS quota_usage (
  installation_id TEXT NOT NULL REFERENCES installations(id),
  scope_key TEXT NOT NULL,
  scope_type TEXT NOT NULL CHECK (scope_type IN ('INSTALLATION', 'PROJECT', 'NAMESPACE')),
  project_id TEXT NULL REFERENCES projects(id),
  namespace TEXT NULL,
  concurrent_jobs BIGINT NOT NULL DEFAULT 0 CHECK (concurrent_jobs >= 0),
  queued_jobs BIGINT NOT NULL DEFAULT 0 CHECK (queued_jobs >= 0),
  retained_jobs BIGINT NOT NULL DEFAULT 0 CHECK (retained_jobs >= 0),
  version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
  updated_at TEXT NOT NULL,
  PRIMARY KEY (installation_id, scope_key),
  CHECK (
    (scope_type = 'INSTALLATION' AND project_id IS NULL AND namespace IS NULL) OR
    (scope_type = 'PROJECT' AND project_id IS NOT NULL AND namespace IS NULL) OR
    (scope_type = 'NAMESPACE' AND project_id IS NOT NULL AND namespace IS NOT NULL)
  )
);

CREATE TABLE IF NOT EXISTS quota_reservations (
  installation_id TEXT NOT NULL REFERENCES installations(id),
  idempotency_key TEXT NOT NULL,
  job_id TEXT NOT NULL,
  project_id TEXT NOT NULL REFERENCES projects(id),
  namespace TEXT NOT NULL,
  policy_id TEXT NOT NULL,
  policy_version BIGINT NOT NULL CHECK (policy_version > 0),
  policy_scope_type TEXT NOT NULL CHECK (
    policy_scope_type IN ('INSTALLATION', 'PROJECT', 'NAMESPACE')
  ),
  policy_scope_project_id TEXT NULL,
  policy_scope_namespace TEXT NULL,
  demand TEXT NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('INTENT', 'RESERVED', 'RELEASED')),
  release_cause TEXT NULL CHECK (
    release_cause IS NULL OR release_cause IN ('COMPLETED', 'CANCELLED', 'FAILED')
  ),
  version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (installation_id, idempotency_key),
  FOREIGN KEY (policy_id, policy_version) REFERENCES policy_versions(policy_id, version)
);
CREATE INDEX IF NOT EXISTS quota_reservations_job
  ON quota_reservations (installation_id, job_id);
CREATE UNIQUE INDEX IF NOT EXISTS quota_reservations_unique_job
  ON quota_reservations (job_id);
CREATE INDEX IF NOT EXISTS quota_reservations_active
  ON quota_reservations (installation_id, state, updated_at);

CREATE TABLE IF NOT EXISTS scheduler_fairness_state (
  installation_id TEXT PRIMARY KEY REFERENCES installations(id),
  version BIGINT NOT NULL CHECK (version > 0),
  next_project_id TEXT NULL REFERENCES projects(id),
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS scheduler_project_deficits (
  installation_id TEXT NOT NULL REFERENCES installations(id),
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  deficit BIGINT NOT NULL CHECK (deficit >= 0),
  updated_at TEXT NOT NULL,
  PRIMARY KEY (installation_id, project_id)
);

CREATE TABLE IF NOT EXISTS admission_decisions (
  id TEXT PRIMARY KEY,
  installation_id TEXT NOT NULL REFERENCES installations(id),
  project_id TEXT NOT NULL REFERENCES projects(id),
  job_id TEXT NOT NULL,
  policy_id TEXT NOT NULL,
  policy_version BIGINT NOT NULL CHECK (policy_version > 0),
  policy_scope_type TEXT NOT NULL CHECK (
    policy_scope_type IN ('INSTALLATION', 'PROJECT', 'NAMESPACE')
  ),
  policy_scope_project_id TEXT NULL,
  policy_scope_namespace TEXT NULL,
  scheduling_policy_version TEXT NOT NULL,
  lane TEXT NOT NULL CHECK (lane IN ('STANDARD', 'EMERGENCY')),
  project_weight BIGINT NOT NULL CHECK (project_weight > 0),
  deficit_before BIGINT NOT NULL CHECK (deficit_before >= 0),
  deficit_after BIGINT NOT NULL CHECK (deficit_after >= 0),
  base_priority BIGINT NOT NULL,
  age BIGINT NOT NULL CHECK (age >= 0),
  aging_step BIGINT NOT NULL CHECK (aging_step > 0),
  effective_priority BIGINT NOT NULL,
  emergency_requested BIGINT NOT NULL DEFAULT 0 CHECK (emergency_requested IN (0, 1)),
  emergency_authorized BIGINT NOT NULL DEFAULT 0 CHECK (emergency_authorized IN (0, 1)),
  emergency_authorization TEXT NULL,
  quota_reservation_key TEXT NULL,
  decided_by TEXT NOT NULL,
  created_at TEXT NOT NULL,
  FOREIGN KEY (policy_id, policy_version) REFERENCES policy_versions(policy_id, version)
);
CREATE INDEX IF NOT EXISTS admission_decisions_installation_time
  ON admission_decisions (installation_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS admission_decisions_job
  ON admission_decisions (installation_id, job_id, created_at DESC);

-- Guarded setup in migration 011 stored one installation policy and one
-- project quota. Materialize the equivalent versioned hierarchy without
-- creating namespace policy that setup never expressed.
INSERT INTO policy_scopes(
  id, installation_id, scope_key, scope_type, project_id, namespace,
  current_version, created_at, updated_at
)
SELECT
  'setup_installation_policy_' || iap.installation_id,
  iap.installation_id, 'I', 'INSTALLATION', NULL, NULL, 1,
  i.created_at, i.created_at
FROM installation_admission_policy iap
JOIN installations i ON i.id = iap.installation_id
WHERE TRUE
ON CONFLICT DO NOTHING;

INSERT INTO policy_versions(policy_id, version, rules, created_at)
SELECT
  ps.id, 1,
  '{"Quotas":{"Global":{"MaxConcurrent":' ||
  CAST(iap.global_concurrency AS TEXT) || ',"MaxQueued":' ||
  CAST(iap.queue_capacity AS TEXT) || ',"MaxRetained":' ||
  CAST(iap.queue_capacity AS TEXT) || '},"Project":{"MaxConcurrent":' ||
  CAST(CASE
    WHEN COALESCE(q.maximum_running_jobs, iap.global_concurrency) > iap.global_concurrency
      THEN COALESCE(q.maximum_running_jobs, iap.global_concurrency)
    ELSE iap.global_concurrency
  END AS TEXT) || ',"MaxQueued":' ||
  CAST(CASE
    WHEN COALESCE(q.maximum_queued_jobs, iap.queue_capacity) > iap.queue_capacity
      THEN COALESCE(q.maximum_queued_jobs, iap.queue_capacity)
    ELSE iap.queue_capacity
  END AS TEXT) || ',"MaxRetained":' ||
  CAST(CASE
    WHEN COALESCE(q.maximum_queued_jobs, iap.queue_capacity) > iap.queue_capacity
      THEN COALESCE(q.maximum_queued_jobs, iap.queue_capacity)
    ELSE iap.queue_capacity
  END AS TEXT) || '},"Namespace":{"MaxConcurrent":' ||
  CAST(iap.namespace_concurrency AS TEXT) || ',"MaxQueued":' ||
  CAST(iap.queue_capacity AS TEXT) || ',"MaxRetained":' ||
  CAST(iap.queue_capacity AS TEXT) || '}},"Priority":{"Min":' ||
  CAST(iap.minimum_priority AS TEXT) || ',"Max":' ||
  CAST(iap.maximum_priority AS TEXT) || ',"Default":' ||
  CAST(CASE
    WHEN iap.minimum_priority > 0 THEN iap.minimum_priority
    WHEN iap.maximum_priority < 0 THEN iap.maximum_priority
    ELSE 0
  END AS TEXT) || '},"MaxDelayedStart":' ||
  CAST(CASE
    WHEN iap.maximum_delay_seconds = 0 THEN 1
    ELSE CAST(iap.maximum_delay_seconds AS BIGINT) * 1000000000
  END AS TEXT) ||
  ',"RoleLifecycleActions":null,"MaxExecutionDuration":86400000000000,' ||
  '"AllowedImageRegistries":null,"HasImageRegistryAllowlist":false}',
  ps.created_at
FROM policy_scopes ps
JOIN installation_admission_policy iap
  ON iap.installation_id = ps.installation_id
LEFT JOIN (
  SELECT
    p.installation_id,
    MAX(pq.maximum_running_jobs) AS maximum_running_jobs,
    MAX(pq.maximum_queued_jobs) AS maximum_queued_jobs
  FROM project_quotas pq
  JOIN projects p ON p.id = pq.project_id
  GROUP BY p.installation_id
) q ON q.installation_id = iap.installation_id
WHERE ps.id = 'setup_installation_policy_' || ps.installation_id
  AND ps.scope_key = 'I'
  AND ps.current_version = 1
ON CONFLICT(policy_id, version) DO NOTHING;

INSERT INTO policy_scopes(
  id, installation_id, scope_key, scope_type, project_id, namespace,
  current_version, created_at, updated_at
)
SELECT
  'setup_project_policy_' || p.id,
  p.installation_id,
  'P:' || CAST(LENGTH(p.id) AS TEXT) || ':' || p.id,
  'PROJECT', p.id, NULL, 1, p.created_at, p.created_at
FROM project_quotas pq
JOIN projects p ON p.id = pq.project_id
WHERE TRUE
ON CONFLICT DO NOTHING;

INSERT INTO policy_versions(policy_id, version, rules, created_at)
SELECT
  ps.id, 1,
  '{"Quotas":{"Project":{"MaxConcurrent":' ||
  CAST(pq.maximum_running_jobs AS TEXT) || ',"MaxQueued":' ||
  CAST(pq.maximum_queued_jobs AS TEXT) || ',"MaxRetained":' ||
  CAST(pq.maximum_queued_jobs AS TEXT) || '}}}',
  ps.created_at
FROM project_quotas pq
JOIN projects p ON p.id = pq.project_id
JOIN policy_scopes ps
  ON ps.id = 'setup_project_policy_' || p.id
 AND ps.installation_id = p.installation_id
 AND ps.scope_key = 'P:' || CAST(LENGTH(p.id) AS TEXT) || ':' || p.id
 AND ps.current_version = 1
ON CONFLICT(policy_id, version) DO NOTHING;

UPDATE projects
SET scheduling_weight = 1
WHERE id IN (SELECT project_id FROM project_quotas)
  AND scheduling_weight < 1;

INSERT INTO quota_usage(
  installation_id, scope_key, scope_type, project_id, namespace,
  concurrent_jobs, queued_jobs, retained_jobs, version, updated_at
)
SELECT
  iap.installation_id, 'I', 'INSTALLATION', NULL, NULL,
  0, 0, 0, 1, i.created_at
FROM installation_admission_policy iap
JOIN installations i ON i.id = iap.installation_id
WHERE TRUE
ON CONFLICT(installation_id, scope_key) DO NOTHING;

INSERT INTO quota_usage(
  installation_id, scope_key, scope_type, project_id, namespace,
  concurrent_jobs, queued_jobs, retained_jobs, version, updated_at
)
SELECT
  p.installation_id,
  'P:' || CAST(LENGTH(p.id) AS TEXT) || ':' || p.id,
  'PROJECT', p.id, NULL, 0, 0, 0, 1, p.created_at
FROM project_quotas pq
JOIN projects p ON p.id = pq.project_id
WHERE TRUE
ON CONFLICT(installation_id, scope_key) DO NOTHING;
