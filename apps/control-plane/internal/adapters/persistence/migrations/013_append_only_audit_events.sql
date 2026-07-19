CREATE TABLE IF NOT EXISTS audit_events (
  id TEXT PRIMARY KEY,
  occurred_at TEXT NOT NULL,
  request_id TEXT NOT NULL,
  trace_id TEXT NOT NULL,
  actor_principal_id TEXT NOT NULL,
  authentication_method TEXT NOT NULL CHECK (
    authentication_method IN (
      'OIDC_CLIENT_CREDENTIALS',
      'OIDC_SESSION',
      'SERVICE_ACCOUNT_TOKEN',
      'BREAK_GLASS',
      'LEGACY_TOKEN'
    )
  ),
  actor_credential_id TEXT NOT NULL,
  effective_groups TEXT NOT NULL,
  action TEXT NOT NULL,
  target_type TEXT NOT NULL,
  target_id TEXT NOT NULL,
  installation_id TEXT NOT NULL REFERENCES installations(id),
  project_id TEXT NULL,
  team_id TEXT NULL,
  namespace TEXT NULL,
  authorization_decision TEXT NOT NULL CHECK (
    authorization_decision IN ('ALLOW', 'DENY')
  ),
  result TEXT NOT NULL CHECK (result IN ('SUCCESS', 'FAILURE')),
  reason TEXT NOT NULL,
  source_address TEXT NOT NULL,
  source_provenance TEXT NOT NULL CHECK (
    source_provenance IN ('DIRECT_PEER', 'TRUSTED_PROXY')
  ),
  source_user_agent TEXT NOT NULL,
  before_present BOOLEAN NOT NULL DEFAULT FALSE,
  before_state TEXT NULL,
  before_changed_fields TEXT NULL,
  before_redaction_count INTEGER NULL,
  before_truncated BOOLEAN NULL,
  after_present BOOLEAN NOT NULL DEFAULT FALSE,
  after_state TEXT NULL,
  after_changed_fields TEXT NULL,
  after_redaction_count INTEGER NULL,
  after_truncated BOOLEAN NULL,
  retention_expires_at TEXT NOT NULL,
  legal_hold_indefinite BOOLEAN NOT NULL DEFAULT FALSE,
  legal_hold_until TEXT NULL,
  persisted_at TEXT NOT NULL,
  CHECK (
    (before_present = FALSE AND before_state IS NULL
      AND before_changed_fields IS NULL AND before_redaction_count IS NULL
      AND before_truncated IS NULL)
    OR
    (before_present = TRUE AND before_changed_fields IS NOT NULL
      AND before_redaction_count IS NOT NULL AND before_truncated IS NOT NULL)
  ),
  CHECK (
    (after_present = FALSE AND after_state IS NULL
      AND after_changed_fields IS NULL AND after_redaction_count IS NULL
      AND after_truncated IS NULL)
    OR
    (after_present = TRUE AND after_changed_fields IS NOT NULL
      AND after_redaction_count IS NOT NULL AND after_truncated IS NOT NULL)
  ),
  CHECK (
    legal_hold_indefinite = FALSE OR legal_hold_until IS NULL
  ),
  CHECK (
    before_redaction_count IS NULL
    OR before_redaction_count BETWEEN 0 AND 65535
  ),
  CHECK (
    after_redaction_count IS NULL
    OR after_redaction_count BETWEEN 0 AND 65535
  ),
  CHECK (retention_expires_at >= occurred_at),
  CHECK (
    authorization_decision <> 'DENY' OR result = 'FAILURE'
  )
);

CREATE INDEX IF NOT EXISTS audit_events_installation_cursor
  ON audit_events (installation_id, occurred_at, id);

CREATE INDEX IF NOT EXISTS audit_events_retention_selection
  ON audit_events (
    installation_id,
    legal_hold_indefinite,
    retention_expires_at,
    legal_hold_until,
    occurred_at,
    id
  );

CREATE INDEX IF NOT EXISTS audit_events_target_cursor
  ON audit_events (installation_id, target_type, target_id, occurred_at, id);
