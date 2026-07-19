ALTER TABLE identity_providers ADD COLUMN display_name TEXT NOT NULL DEFAULT '';
ALTER TABLE identity_providers ADD COLUMN client_id TEXT NOT NULL DEFAULT '';
ALTER TABLE identity_providers ADD COLUMN redirect_uri TEXT NOT NULL DEFAULT '';
ALTER TABLE identity_providers ADD COLUMN client_secret_ciphertext TEXT NOT NULL DEFAULT '';
ALTER TABLE identity_providers ADD COLUMN client_secret_reference TEXT NOT NULL DEFAULT '';
ALTER TABLE identity_providers ADD COLUMN mapping_type TEXT NOT NULL DEFAULT '';
ALTER TABLE identity_providers ADD COLUMN mapping_value TEXT NOT NULL DEFAULT '';
ALTER TABLE identity_providers ADD COLUMN test_status TEXT NOT NULL DEFAULT 'NOT_TESTED'
  CHECK (test_status IN ('NOT_TESTED', 'PASSED', 'FAILED'));
ALTER TABLE identity_providers ADD COLUMN tested_at TEXT NULL;
ALTER TABLE identity_providers ADD COLUMN test_message TEXT NOT NULL DEFAULT '';
ALTER TABLE identity_providers ADD COLUMN tested_version INTEGER NULL;
ALTER TABLE identity_providers ADD COLUMN version INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1);

UPDATE identity_providers
SET display_name=id,
    client_id=audience,
    redirect_uri=issuer,
    test_status=CASE WHEN enabled THEN 'PASSED' ELSE 'NOT_TESTED' END,
    tested_version=CASE WHEN enabled THEN version ELSE NULL END;

CREATE UNIQUE INDEX IF NOT EXISTS identity_providers_installation_issuer
  ON identity_providers (installation_id, issuer);
