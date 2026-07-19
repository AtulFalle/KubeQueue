CREATE TABLE IF NOT EXISTS installations (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS projects (
  id TEXT PRIMARY KEY,
  installation_id TEXT NOT NULL REFERENCES installations(id),
  name TEXT NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE (installation_id, name)
);

CREATE TABLE IF NOT EXISTS principals (
  id TEXT PRIMARY KEY,
  installation_id TEXT NOT NULL REFERENCES installations(id),
  kind TEXT NOT NULL CHECK (kind IN ('HUMAN', 'SERVICE_ACCOUNT', 'LEGACY_ADMIN')),
  display_name TEXT NOT NULL,
  disabled_at TEXT NULL,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS principals_installation ON principals (installation_id);

CREATE TABLE IF NOT EXISTS external_identities (
  id TEXT PRIMARY KEY,
  principal_id TEXT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  issuer TEXT NOT NULL,
  subject TEXT NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE (issuer, subject)
);
CREATE INDEX IF NOT EXISTS external_identities_principal
  ON external_identities (principal_id);

CREATE TABLE IF NOT EXISTS teams (
  id TEXT PRIMARY KEY,
  installation_id TEXT NOT NULL REFERENCES installations(id),
  name TEXT NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE (installation_id, name)
);

CREATE TABLE IF NOT EXISTS team_memberships (
  team_id TEXT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
  principal_id TEXT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  created_at TEXT NOT NULL,
  PRIMARY KEY (team_id, principal_id)
);
CREATE INDEX IF NOT EXISTS team_memberships_principal
  ON team_memberships (principal_id);

CREATE TABLE IF NOT EXISTS role_definitions (
  id TEXT PRIMARY KEY,
  installation_id TEXT NOT NULL REFERENCES installations(id),
  name TEXT NOT NULL,
  scope_type TEXT NOT NULL CHECK (scope_type IN ('INSTALLATION', 'PROJECT')),
  permissions TEXT NOT NULL,
  built_in BOOLEAN NOT NULL DEFAULT FALSE,
  created_at TEXT NOT NULL,
  UNIQUE (installation_id, name)
);

CREATE TABLE IF NOT EXISTS role_bindings (
  id TEXT PRIMARY KEY,
  installation_id TEXT NOT NULL REFERENCES installations(id),
  role_definition_id TEXT NOT NULL REFERENCES role_definitions(id),
  scope_type TEXT NOT NULL CHECK (scope_type IN ('INSTALLATION', 'PROJECT')),
  project_id TEXT NULL REFERENCES projects(id),
  principal_id TEXT NULL REFERENCES principals(id),
  team_id TEXT NULL REFERENCES teams(id),
  created_at TEXT NOT NULL,
  CHECK (
    (scope_type = 'INSTALLATION' AND project_id IS NULL) OR
    (scope_type = 'PROJECT' AND project_id IS NOT NULL)
  ),
  CHECK (
    (principal_id IS NOT NULL AND team_id IS NULL) OR
    (principal_id IS NULL AND team_id IS NOT NULL)
  )
);
CREATE INDEX IF NOT EXISTS role_bindings_principal
  ON role_bindings (principal_id, scope_type, project_id);
CREATE INDEX IF NOT EXISTS role_bindings_team
  ON role_bindings (team_id, scope_type, project_id);

CREATE TABLE IF NOT EXISTS service_accounts (
  principal_id TEXT PRIMARY KEY REFERENCES principals(id) ON DELETE CASCADE,
  project_id TEXT NULL REFERENCES projects(id),
  created_by_principal_id TEXT NOT NULL REFERENCES principals(id),
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS service_accounts_project ON service_accounts (project_id);

CREATE TABLE IF NOT EXISTS native_credential_metadata (
  id TEXT PRIMARY KEY,
  service_account_principal_id TEXT NOT NULL
    REFERENCES service_accounts(principal_id) ON DELETE CASCADE,
  safe_prefix TEXT NOT NULL,
  keyed_digest TEXT NOT NULL,
  -- Additive Milestone 2 credential lifecycle state. Migration 008 is still
  -- uncommitted, so these columns intentionally remain part of its initial table.
  permissions TEXT NOT NULL DEFAULT '[]',
  expires_at TEXT NOT NULL,
  created_by_principal_id TEXT NOT NULL REFERENCES principals(id),
  last_used_at TEXT NULL,
  rotated_at TEXT NULL,
  overlap_expires_at TEXT NULL,
  revoked_at TEXT NULL,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS native_credentials_service_account
  ON native_credential_metadata (service_account_principal_id);
CREATE UNIQUE INDEX IF NOT EXISTS native_credentials_safe_prefix
  ON native_credential_metadata (safe_prefix);

CREATE TABLE IF NOT EXISTS namespace_bindings (
  id TEXT PRIMARY KEY,
  installation_id TEXT NOT NULL REFERENCES installations(id),
  project_id TEXT NOT NULL REFERENCES projects(id),
  namespace TEXT NOT NULL UNIQUE,
  active BOOLEAN NOT NULL DEFAULT TRUE,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS namespace_bindings_project
  ON namespace_bindings (project_id, namespace);

ALTER TABLE jobs ADD COLUMN project_id TEXT NULL REFERENCES projects(id);
ALTER TABLE jobs ADD COLUMN namespace_binding_id TEXT NULL REFERENCES namespace_bindings(id);
ALTER TABLE jobs ADD COLUMN creator_principal_id TEXT NULL REFERENCES principals(id);
ALTER TABLE jobs ADD COLUMN submission_source TEXT NULL CHECK (
  submission_source IN ('API', 'KUBERNETES_DISCOVERY', 'LEGACY_COMPATIBILITY')
);

CREATE INDEX IF NOT EXISTS jobs_project ON jobs (project_id);
CREATE INDEX IF NOT EXISTS jobs_namespace_binding ON jobs (namespace_binding_id);
CREATE INDEX IF NOT EXISTS jobs_creator_principal ON jobs (creator_principal_id);
