CREATE TABLE IF NOT EXISTS leadership_leases (
  name TEXT PRIMARY KEY,
  holder TEXT NOT NULL,
  generation BIGINT NOT NULL,
  expires_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  CHECK (generation > 0)
);
