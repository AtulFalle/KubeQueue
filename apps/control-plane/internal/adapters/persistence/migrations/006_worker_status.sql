CREATE TABLE IF NOT EXISTS worker_status (
  id INTEGER PRIMARY KEY,
  state TEXT NOT NULL,
  heartbeat_at TEXT NULL,
  last_successful_reconciliation_at TEXT NULL,
  watch_mode TEXT NOT NULL,
  effective_namespaces TEXT NOT NULL,
  excluded_namespaces TEXT NOT NULL,
  namespace_statuses TEXT NOT NULL,
  global_concurrency INTEGER NOT NULL,
  namespace_concurrency INTEGER NOT NULL,
  release_version TEXT NOT NULL,
  active_error TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  CHECK (id = 1)
);

INSERT INTO worker_status (
  id, state, watch_mode, effective_namespaces, excluded_namespaces, namespace_statuses,
  global_concurrency, namespace_concurrency, release_version, active_error, updated_at
)
VALUES (1, 'unavailable', 'selected', '[]', '[]', '[]', 1, 1, '', '', '')
ON CONFLICT(id) DO NOTHING;
