ALTER TABLE role_definitions
  ADD COLUMN current_revision BIGINT NOT NULL DEFAULT 1
    CHECK (current_revision > 0);

CREATE TABLE IF NOT EXISTS role_definition_revisions (
  role_definition_id TEXT NOT NULL
    REFERENCES role_definitions(id) ON DELETE CASCADE,
  revision BIGINT NOT NULL CHECK (revision > 0),
  name TEXT NOT NULL,
  scope_type TEXT NOT NULL CHECK (scope_type IN ('INSTALLATION', 'PROJECT')),
  permissions TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY (role_definition_id, revision)
);

INSERT INTO role_definition_revisions(
  role_definition_id,
  revision,
  name,
  scope_type,
  permissions,
  created_at
)
SELECT id, 1, name, scope_type, permissions, created_at
FROM role_definitions
WHERE built_in = FALSE;

DELETE FROM role_bindings
WHERE id NOT IN (
  SELECT MIN(id)
  FROM role_bindings
  GROUP BY
    installation_id,
    role_definition_id,
    scope_type,
    COALESCE(project_id, ''),
    COALESCE(principal_id, ''),
    COALESCE(team_id, '')
);

CREATE UNIQUE INDEX IF NOT EXISTS role_bindings_unique_principal_grant
  ON role_bindings (
    installation_id,
    role_definition_id,
    scope_type,
    COALESCE(project_id, ''),
    principal_id
  )
  WHERE principal_id IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS role_bindings_unique_team_grant
  ON role_bindings (
    installation_id,
    role_definition_id,
    scope_type,
    COALESCE(project_id, ''),
    team_id
  )
  WHERE team_id IS NOT NULL;
