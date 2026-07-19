CREATE TABLE local_accounts (
  principal_id TEXT PRIMARY KEY REFERENCES principals(id) ON DELETE CASCADE,
  normalized_username TEXT NOT NULL UNIQUE,
  username TEXT NOT NULL,
  password_hash TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE local_login_throttles (
  throttle_key TEXT PRIMARY KEY,
  failure_count INTEGER NOT NULL CHECK (failure_count >= 0),
  window_started_at TEXT NOT NULL,
  locked_until TEXT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE local_setup_completions (
  installation_id TEXT PRIMARY KEY REFERENCES installations(id),
  owner_principal_id TEXT NOT NULL UNIQUE REFERENCES principals(id),
  username TEXT NOT NULL,
  claim_fingerprint TEXT NOT NULL UNIQUE,
  created_at TEXT NOT NULL
);
