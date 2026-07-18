CREATE TABLE IF NOT EXISTS jobs (
  id TEXT PRIMARY KEY,
  parent_id TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL,
  namespace TEXT NOT NULL,
  team TEXT NOT NULL DEFAULT '',
  priority INTEGER NOT NULL DEFAULT 0,
  position BIGINT NOT NULL,
  desired_state TEXT NOT NULL,
  observed_state TEXT NOT NULL,
  scheduled_for TEXT NULL,
  kubernetes_uid TEXT NOT NULL DEFAULT '',
  template TEXT NOT NULL,
  attempt INTEGER NOT NULL DEFAULT 1,
  version BIGINT NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS jobs_queue_order
  ON jobs (desired_state, priority DESC, position ASC, created_at ASC);
CREATE UNIQUE INDEX IF NOT EXISTS jobs_kubernetes_uid
  ON jobs (kubernetes_uid) WHERE kubernetes_uid <> '';
CREATE INDEX IF NOT EXISTS jobs_parent_id ON jobs (parent_id);

CREATE TABLE IF NOT EXISTS job_events (
  id {{EVENTS_ID}},
  job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  type TEXT NOT NULL,
  message TEXT NOT NULL,
  data TEXT,
  created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS job_events_job_time ON job_events (job_id, created_at DESC);

CREATE TABLE IF NOT EXISTS control_plane_metadata (
  key TEXT PRIMARY KEY,
  value BIGINT NOT NULL
);

INSERT INTO control_plane_metadata (key, value)
VALUES ('queue_version', 0)
ON CONFLICT (key) DO NOTHING;

CREATE TABLE IF NOT EXISTS scheduler_lease (
  id INTEGER PRIMARY KEY,
  holder TEXT NOT NULL,
  expires_at TEXT NOT NULL
);

