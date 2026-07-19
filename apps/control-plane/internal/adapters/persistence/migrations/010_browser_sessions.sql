ALTER TABLE principals
  ADD COLUMN authz_generation INTEGER NOT NULL DEFAULT 1
    CHECK (authz_generation > 0);

CREATE TABLE IF NOT EXISTS browser_sessions (
  id TEXT PRIMARY KEY,
  credential_digest TEXT NOT NULL UNIQUE,
  csrf_digest TEXT NOT NULL,
  principal_id TEXT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  identity_provider_id TEXT NULL REFERENCES identity_providers(id),
  authentication_method TEXT NOT NULL,
  refresh_token_ciphertext TEXT NULL,
  access_token_ciphertext TEXT NULL,
  authz_generation INTEGER NOT NULL,
  idle_expires_at TEXT NOT NULL,
  absolute_expires_at TEXT NOT NULL,
  last_used_at TEXT NOT NULL,
  revoked_at TEXT NULL,
  created_at TEXT NOT NULL,
  CHECK (idle_expires_at <= absolute_expires_at)
);

CREATE INDEX IF NOT EXISTS browser_sessions_principal
  ON browser_sessions (principal_id, revoked_at, absolute_expires_at);

CREATE TABLE IF NOT EXISTS oauth_login_attempts (
  state_digest TEXT PRIMARY KEY,
  nonce_digest TEXT NOT NULL,
  nonce_ciphertext TEXT NOT NULL,
  pkce_verifier_ciphertext TEXT NOT NULL,
  return_to TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  consumed_at TEXT NULL,
  created_at TEXT NOT NULL
);
