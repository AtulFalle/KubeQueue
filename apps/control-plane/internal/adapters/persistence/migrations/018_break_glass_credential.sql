CREATE TABLE IF NOT EXISTS break_glass_credentials (
  slot TEXT PRIMARY KEY CHECK (slot IN ('current', 'previous')),
  safe_prefix TEXT NOT NULL UNIQUE,
  keyed_digest TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  overlap_expires_at TEXT,
  revoked_at TEXT,
  last_used_at TEXT,
  configured_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS break_glass_rate_limit (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  window_started_at TEXT,
  failures INTEGER NOT NULL DEFAULT 0 CHECK (failures >= 0),
  blocked_until TEXT
);

INSERT INTO break_glass_rate_limit(id, failures)
VALUES(1, 0)
ON CONFLICT(id) DO NOTHING;
