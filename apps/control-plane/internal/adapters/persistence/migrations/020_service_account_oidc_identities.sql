CREATE TABLE service_account_oidc_identities (
  service_account_principal_id TEXT PRIMARY KEY
    REFERENCES service_accounts(principal_id) ON DELETE CASCADE,
  issuer TEXT NOT NULL,
  subject TEXT NOT NULL,
  created_by_principal_id TEXT NOT NULL REFERENCES principals(id),
  created_at TEXT NOT NULL,
  UNIQUE (issuer, subject)
);
