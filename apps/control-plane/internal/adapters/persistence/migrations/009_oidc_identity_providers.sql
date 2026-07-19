CREATE TABLE IF NOT EXISTS identity_providers (
  id TEXT PRIMARY KEY,
  installation_id TEXT NOT NULL REFERENCES installations(id),
  issuer TEXT NOT NULL UNIQUE,
  audience TEXT NOT NULL,
  authorized_party TEXT NULL,
  allowed_algorithms TEXT NOT NULL,
  groups_claim TEXT NOT NULL DEFAULT 'groups',
  email_claim TEXT NOT NULL DEFAULT 'email',
  name_claim TEXT NOT NULL DEFAULT 'name',
  cache_ttl_seconds INTEGER NOT NULL DEFAULT 300
    CHECK (cache_ttl_seconds BETWEEN 60 AND 86400),
  enabled BOOLEAN NOT NULL DEFAULT TRUE,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS identity_providers_installation
  ON identity_providers (installation_id, enabled);

ALTER TABLE team_memberships
  ADD COLUMN source_identity_provider_id TEXT NULL
    REFERENCES identity_providers(id) ON DELETE CASCADE;
CREATE INDEX IF NOT EXISTS team_memberships_identity_provider
  ON team_memberships (principal_id, source_identity_provider_id);

CREATE TABLE IF NOT EXISTS oidc_jit_provisioning_mappings (
  id TEXT PRIMARY KEY,
  identity_provider_id TEXT NOT NULL
    REFERENCES identity_providers(id) ON DELETE CASCADE,
  mapping_type TEXT NOT NULL CHECK (mapping_type IN ('GROUP', 'DOMAIN')),
  claim_value TEXT NOT NULL,
  team_id TEXT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
  created_at TEXT NOT NULL,
  UNIQUE (identity_provider_id, mapping_type, claim_value, team_id)
);
CREATE INDEX IF NOT EXISTS oidc_jit_mappings_provider
  ON oidc_jit_provisioning_mappings (identity_provider_id, mapping_type, claim_value);
