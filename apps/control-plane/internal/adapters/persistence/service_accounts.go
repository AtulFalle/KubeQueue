package persistence

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain/serviceaccountcredential"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/ports"
)

func (s *Store) CreateServiceAccount(
	ctx context.Context,
	account serviceaccountcredential.ServiceAccount,
) (serviceaccountcredential.ServiceAccount, error) {
	err := s.auditTransaction(ctx, func(tx *sql.Tx) error {
		var creatorInstallation domain.InstallationID
		err := tx.QueryRowContext(ctx, s.bind(
			`SELECT installation_id FROM principals WHERE id=? AND disabled_at IS NULL`,
		), account.CreatedBy).Scan(&creatorInstallation)
		if errors.Is(err, sql.ErrNoRows) || creatorInstallation != account.InstallationID {
			return domain.ErrAccessDenied
		}
		if err != nil {
			return fmt.Errorf("read service-account creator: %w", err)
		}
		if account.ProjectID != "" {
			var projectInstallation domain.InstallationID
			err := tx.QueryRowContext(ctx, s.bind(
				`SELECT installation_id FROM projects WHERE id=?`,
			), account.ProjectID).Scan(&projectInstallation)
			if errors.Is(err, sql.ErrNoRows) {
				return ports.ErrServiceAccountNotFound
			}
			if err != nil {
				return fmt.Errorf("read service-account project: %w", err)
			}
			if projectInstallation != account.InstallationID {
				return domain.ErrAccessDenied
			}
		}
		createdAt := account.CreatedAt.UTC().Format(time.RFC3339Nano)
		result, err := tx.ExecContext(ctx, s.bind(
			`INSERT INTO principals(id,installation_id,kind,display_name,created_at)
			 VALUES(?,?,'SERVICE_ACCOUNT',?,?) ON CONFLICT(id) DO NOTHING`,
		), account.PrincipalID, account.InstallationID, account.DisplayName, createdAt)
		if err != nil {
			return fmt.Errorf("insert service-account principal: %w", err)
		}
		inserted, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("inspect service-account principal insert: %w", err)
		}
		if inserted == 0 {
			var installation domain.InstallationID
			var kind, displayName string
			err := tx.QueryRowContext(ctx, s.bind(
				`SELECT installation_id,kind,display_name FROM principals WHERE id=?`,
			), account.PrincipalID).Scan(&installation, &kind, &displayName)
			if err != nil || installation != account.InstallationID ||
				kind != "SERVICE_ACCOUNT" || displayName != account.DisplayName {
				return ports.ErrConflict
			}
		}
		_, err = tx.ExecContext(ctx, s.bind(
			`INSERT INTO service_accounts(principal_id,project_id,created_by_principal_id,created_at)
			 VALUES(?,?,?,?) ON CONFLICT(principal_id) DO NOTHING`,
		), account.PrincipalID, nullableID(account.ProjectID), account.CreatedBy, createdAt)
		if err != nil {
			return fmt.Errorf("insert service account: %w", err)
		}
		var project sql.NullString
		var createdBy domain.PrincipalID
		var storedCreatedAt string
		err = tx.QueryRowContext(ctx, s.bind(
			`SELECT project_id,created_by_principal_id,created_at
			 FROM service_accounts WHERE principal_id=?`,
		), account.PrincipalID).Scan(&project, &createdBy, &storedCreatedAt)
		if err != nil || domain.ProjectID(project.String) != account.ProjectID ||
			createdBy != account.CreatedBy {
			return ports.ErrConflict
		}
		return nil
	})
	if err != nil {
		return serviceaccountcredential.ServiceAccount{}, err
	}
	return s.ServiceAccount(ctx, account.PrincipalID)
}

func (s *Store) ServiceAccount(
	ctx context.Context,
	principalID domain.PrincipalID,
) (serviceaccountcredential.ServiceAccount, error) {
	var account serviceaccountcredential.ServiceAccount
	var project sql.NullString
	var oidcIssuer, oidcSubject sql.NullString
	var createdAt string
	err := s.db.QueryRowContext(ctx, s.bind(
		`SELECT p.id,p.installation_id,sa.project_id,p.display_name,
		 sa.created_by_principal_id,sa.created_at,oi.issuer,oi.subject
		 FROM service_accounts sa
		 JOIN principals p ON p.id=sa.principal_id AND p.kind='SERVICE_ACCOUNT'
		   AND p.disabled_at IS NULL
		 LEFT JOIN service_account_oidc_identities oi
		   ON oi.service_account_principal_id=sa.principal_id
		 WHERE sa.principal_id=?`,
	), principalID).Scan(
		&account.PrincipalID, &account.InstallationID, &project, &account.DisplayName,
		&account.CreatedBy, &createdAt, &oidcIssuer, &oidcSubject,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return serviceaccountcredential.ServiceAccount{}, ports.ErrServiceAccountNotFound
	}
	if err != nil {
		return serviceaccountcredential.ServiceAccount{}, fmt.Errorf("read service account: %w", err)
	}
	account.ProjectID = domain.ProjectID(project.String)
	setServiceAccountOIDCIdentity(&account, oidcIssuer, oidcSubject)
	account.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return serviceaccountcredential.ServiceAccount{}, fmt.Errorf("parse service-account creation time: %w", err)
	}
	return account, nil
}

func (s *Store) ListServiceAccounts(
	ctx context.Context,
	installationID domain.InstallationID,
	page domain.AccessPage,
) ([]serviceaccountcredential.ServiceAccount, error) {
	page, err := page.Normalize()
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, s.bind(
		`SELECT p.id,p.installation_id,sa.project_id,p.display_name,
		        sa.created_by_principal_id,sa.created_at,oi.issuer,oi.subject
		 FROM service_accounts sa
		 JOIN principals p ON p.id=sa.principal_id AND p.kind='SERVICE_ACCOUNT'
		   AND p.disabled_at IS NULL
		 LEFT JOIN service_account_oidc_identities oi
		   ON oi.service_account_principal_id=sa.principal_id
		 WHERE p.installation_id=? AND p.id>?
		 ORDER BY p.id LIMIT ?`,
	), installationID, page.After, page.Limit)
	if err != nil {
		return nil, fmt.Errorf("list service accounts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	accounts := make([]serviceaccountcredential.ServiceAccount, 0, page.Limit)
	for rows.Next() {
		var account serviceaccountcredential.ServiceAccount
		var project sql.NullString
		var oidcIssuer, oidcSubject sql.NullString
		var createdAt string
		if err := rows.Scan(
			&account.PrincipalID, &account.InstallationID, &project, &account.DisplayName,
			&account.CreatedBy, &createdAt, &oidcIssuer, &oidcSubject,
		); err != nil {
			return nil, fmt.Errorf("scan service account: %w", err)
		}
		account.ProjectID = domain.ProjectID(project.String)
		setServiceAccountOIDCIdentity(&account, oidcIssuer, oidcSubject)
		account.CreatedAt, err = parseCredentialTime(createdAt)
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, account)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read service accounts: %w", err)
	}
	return accounts, nil
}

func (s *Store) BindServiceAccountOIDCIdentity(
	ctx context.Context,
	installationID domain.InstallationID,
	principalID domain.PrincipalID,
	identity serviceaccountcredential.OIDCIdentity,
	createdBy domain.PrincipalID,
	createdAt time.Time,
) (serviceaccountcredential.ServiceAccount, error) {
	err := s.auditTransaction(ctx, func(tx *sql.Tx) error {
		var accountInstallation domain.InstallationID
		err := tx.QueryRowContext(ctx, s.bind(
			`SELECT p.installation_id FROM service_accounts sa
			 JOIN principals p ON p.id=sa.principal_id
			 WHERE sa.principal_id=? AND p.kind='SERVICE_ACCOUNT' AND p.disabled_at IS NULL`,
		), principalID).Scan(&accountInstallation)
		if errors.Is(err, sql.ErrNoRows) || accountInstallation != installationID {
			return ports.ErrServiceAccountNotFound
		}
		if err != nil {
			return fmt.Errorf("read OIDC service-account binding target: %w", err)
		}
		var providerID string
		err = tx.QueryRowContext(ctx, s.bind(
			`SELECT id FROM identity_providers
			 WHERE installation_id=? AND issuer=? AND enabled=TRUE`,
		), installationID, identity.Issuer).Scan(&providerID)
		if errors.Is(err, sql.ErrNoRows) {
			return ports.ErrServiceAccountNotFound
		}
		if err != nil {
			return fmt.Errorf("read OIDC service-account identity provider: %w", err)
		}
		var existingExternalIdentity int
		err = tx.QueryRowContext(ctx, s.bind(
			`SELECT 1 FROM external_identities WHERE issuer=? AND subject=?`,
		), identity.Issuer, identity.Subject).Scan(&existingExternalIdentity)
		if err == nil {
			return domain.ErrAccessConflict
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("check existing OIDC identity: %w", err)
		}
		_, err = tx.ExecContext(ctx, s.bind(
			`INSERT INTO service_account_oidc_identities(
			   service_account_principal_id,issuer,subject,created_by_principal_id,created_at
			 ) VALUES(?,?,?,?,?)
			 ON CONFLICT(service_account_principal_id) DO UPDATE
			 SET issuer=excluded.issuer,subject=excluded.subject,
			     created_by_principal_id=excluded.created_by_principal_id,
			     created_at=excluded.created_at`,
		), principalID, identity.Issuer, identity.Subject, createdBy,
			createdAt.UTC().Format(time.RFC3339Nano))
		if err := accessWriteError(err); err != nil {
			return fmt.Errorf("store OIDC service-account identity: %w", err)
		}
		return nil
	})
	if err != nil {
		return serviceaccountcredential.ServiceAccount{}, err
	}
	return s.ServiceAccount(ctx, principalID)
}

func (s *Store) RemoveServiceAccountOIDCIdentity(
	ctx context.Context,
	installationID domain.InstallationID,
	principalID domain.PrincipalID,
) error {
	return s.auditTransaction(ctx, func(tx *sql.Tx) error {
		var found int
		err := tx.QueryRowContext(ctx, s.bind(
			`SELECT 1 FROM service_accounts sa JOIN principals p ON p.id=sa.principal_id
			 WHERE sa.principal_id=? AND p.installation_id=?
			   AND p.kind='SERVICE_ACCOUNT' AND p.disabled_at IS NULL`,
		), principalID, installationID).Scan(&found)
		if errors.Is(err, sql.ErrNoRows) {
			return ports.ErrServiceAccountNotFound
		}
		if err != nil {
			return fmt.Errorf("read OIDC service-account binding target: %w", err)
		}
		_, err = tx.ExecContext(ctx, s.bind(
			`DELETE FROM service_account_oidc_identities
			 WHERE service_account_principal_id=?`,
		), principalID)
		if err != nil {
			return fmt.Errorf("remove OIDC service-account identity: %w", err)
		}
		return nil
	})
}

func setServiceAccountOIDCIdentity(
	account *serviceaccountcredential.ServiceAccount,
	issuer sql.NullString,
	subject sql.NullString,
) {
	if issuer.Valid && subject.Valid {
		account.OIDCIdentity = &serviceaccountcredential.OIDCIdentity{
			Issuer: issuer.String, Subject: subject.String,
		}
	}
}

func (s *Store) ListNativeCredentialMetadata(
	ctx context.Context,
	installationID domain.InstallationID,
	principalID domain.PrincipalID,
	page domain.AccessPage,
) ([]serviceaccountcredential.CredentialMetadata, error) {
	page, err := page.Normalize()
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, s.bind(
		`SELECT n.id,n.service_account_principal_id,n.safe_prefix,n.permissions,
		        n.created_by_principal_id,n.created_at,n.expires_at,n.last_used_at,
		        n.rotated_at,n.overlap_expires_at,n.revoked_at
		 FROM native_credential_metadata n
		 JOIN service_accounts sa ON sa.principal_id=n.service_account_principal_id
		 JOIN principals p ON p.id=sa.principal_id AND p.kind='SERVICE_ACCOUNT'
		   AND p.disabled_at IS NULL
		 WHERE p.installation_id=? AND n.service_account_principal_id=? AND n.id>?
		 ORDER BY n.id LIMIT ?`,
	), installationID, principalID, page.After, page.Limit)
	if err != nil {
		return nil, fmt.Errorf("list service-account credentials: %w", err)
	}
	defer func() { _ = rows.Close() }()
	credentials := make([]serviceaccountcredential.CredentialMetadata, 0, page.Limit)
	for rows.Next() {
		metadata, err := scanCredentialMetadata(rows)
		if err != nil {
			return nil, err
		}
		credentials = append(credentials, metadata)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read service-account credentials: %w", err)
	}
	return credentials, nil
}

func (s *Store) CreateNativeCredential(
	ctx context.Context,
	credential serviceaccountcredential.Credential,
) error {
	permissions, err := json.Marshal(credential.Stored.Permissions)
	if err != nil {
		return fmt.Errorf("encode service-account credential permissions: %w", err)
	}
	err = s.auditTransaction(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, s.bind(
			`INSERT INTO native_credential_metadata(
			 id,service_account_principal_id,safe_prefix,keyed_digest,permissions,
			 expires_at,created_by_principal_id,last_used_at,rotated_at,
			 overlap_expires_at,revoked_at,created_at
			 ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		), credential.ID, credential.ServiceAccountPrincipalID, credential.Stored.Prefix,
			base64.RawURLEncoding.EncodeToString(credential.Stored.Digest.Bytes()), string(permissions),
			formatCredentialTime(credential.Stored.ExpiresAt), credential.Stored.CreatedBy,
			nullableCredentialTime(credential.Stored.LastUsedAt),
			nullableCredentialTimePointer(credential.Stored.RotatedAt),
			nullableCredentialTimePointer(credential.Stored.OverlapExpiresAt),
			nullableCredentialTimePointer(credential.Stored.RevokedAt),
			formatCredentialTime(credential.Stored.CreatedAt))
		return err
	})
	if err != nil {
		return fmt.Errorf("insert service-account credential: %w", err)
	}
	return nil
}

func (s *Store) NativeCredentialByID(
	ctx context.Context,
	id string,
) (serviceaccountcredential.Credential, serviceaccountcredential.ServiceAccount, error) {
	return scanNativeCredential(s.db.QueryRowContext(
		ctx, s.bind(nativeCredentialQuery+` WHERE n.id=?`), id,
	))
}

func (s *Store) NativeCredentialByPrefix(
	ctx context.Context,
	prefix string,
) (serviceaccountcredential.Credential, serviceaccountcredential.ServiceAccount, error) {
	return scanNativeCredential(s.db.QueryRowContext(
		ctx, s.bind(nativeCredentialQuery+` WHERE n.safe_prefix=?`), prefix,
	))
}

func (s *Store) RotateNativeCredential(
	ctx context.Context,
	previous serviceaccountcredential.Credential,
	replacement serviceaccountcredential.Credential,
) error {
	return s.auditTransaction(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, s.bind(
			`UPDATE native_credential_metadata SET rotated_at=?,overlap_expires_at=?
			 WHERE id=? AND service_account_principal_id=?
			   AND revoked_at IS NULL AND rotated_at IS NULL`,
		), nullableCredentialTimePointer(previous.Stored.RotatedAt),
			nullableCredentialTimePointer(previous.Stored.OverlapExpiresAt),
			previous.ID, previous.ServiceAccountPrincipalID)
		if err != nil {
			return fmt.Errorf("mark rotated service-account credential: %w", err)
		}
		updated, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("inspect rotated service-account credential: %w", err)
		}
		if updated != 1 {
			return ports.ErrCredentialConflict
		}
		permissions, err := json.Marshal(replacement.Stored.Permissions)
		if err != nil {
			return fmt.Errorf("encode replacement credential permissions: %w", err)
		}
		_, err = tx.ExecContext(ctx, s.bind(
			`INSERT INTO native_credential_metadata(
			 id,service_account_principal_id,safe_prefix,keyed_digest,permissions,
			 expires_at,created_by_principal_id,created_at
			 ) VALUES(?,?,?,?,?,?,?,?)`,
		), replacement.ID, replacement.ServiceAccountPrincipalID, replacement.Stored.Prefix,
			base64.RawURLEncoding.EncodeToString(replacement.Stored.Digest.Bytes()),
			string(permissions), formatCredentialTime(replacement.Stored.ExpiresAt),
			replacement.Stored.CreatedBy, formatCredentialTime(replacement.Stored.CreatedAt))
		if err != nil {
			return fmt.Errorf("insert replacement service-account credential: %w", err)
		}
		return nil
	})
}

func (s *Store) RevokeNativeCredential(
	ctx context.Context,
	id string,
	revokedAt time.Time,
) error {
	return s.auditTransaction(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, s.bind(
			`UPDATE native_credential_metadata SET revoked_at=COALESCE(revoked_at,?) WHERE id=?`,
		), formatCredentialTime(revokedAt), id)
		if err != nil {
			return fmt.Errorf("revoke service-account credential: %w", err)
		}
		updated, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("inspect service-account credential revocation: %w", err)
		}
		if updated == 0 {
			return ports.ErrCredentialNotFound
		}
		return nil
	})
}

func (s *Store) TouchNativeCredential(
	ctx context.Context,
	id string,
	lastUsedAt time.Time,
) error {
	at := formatCredentialTime(lastUsedAt)
	result, err := s.db.ExecContext(ctx, s.bind(
		`UPDATE native_credential_metadata SET last_used_at=?
		 WHERE id=? AND revoked_at IS NULL AND expires_at>?
		   AND (rotated_at IS NULL OR overlap_expires_at>?)`,
	), at, id, at, at)
	if err != nil {
		return fmt.Errorf("touch service-account credential: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect service-account credential touch: %w", err)
	}
	if updated != 1 {
		return serviceaccountcredential.ErrInvalidCredential
	}
	return nil
}

const nativeCredentialQuery = `SELECT
	n.id,n.service_account_principal_id,n.safe_prefix,n.keyed_digest,n.permissions,
	n.created_by_principal_id,n.created_at,n.expires_at,n.last_used_at,
	n.rotated_at,n.overlap_expires_at,n.revoked_at,
	p.installation_id,sa.project_id,p.display_name,sa.created_by_principal_id,sa.created_at
	FROM native_credential_metadata n
	JOIN service_accounts sa ON sa.principal_id=n.service_account_principal_id
	JOIN principals p ON p.id=sa.principal_id AND p.kind='SERVICE_ACCOUNT'
	  AND p.disabled_at IS NULL`

func scanNativeCredential(
	row scanner,
) (serviceaccountcredential.Credential, serviceaccountcredential.ServiceAccount, error) {
	var credential serviceaccountcredential.Credential
	var account serviceaccountcredential.ServiceAccount
	var digest, permissions, createdAt, expiresAt string
	var lastUsedAt, rotatedAt, overlapExpiresAt, revokedAt sql.NullString
	var projectID sql.NullString
	var accountCreatedAt string
	err := row.Scan(
		&credential.ID, &credential.ServiceAccountPrincipalID,
		&credential.Stored.Prefix, &digest, &permissions,
		&credential.Stored.CreatedBy, &createdAt, &expiresAt, &lastUsedAt,
		&rotatedAt, &overlapExpiresAt, &revokedAt,
		&account.InstallationID, &projectID, &account.DisplayName,
		&account.CreatedBy, &accountCreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return serviceaccountcredential.Credential{},
			serviceaccountcredential.ServiceAccount{}, ports.ErrCredentialNotFound
	}
	if err != nil {
		return serviceaccountcredential.Credential{},
			serviceaccountcredential.ServiceAccount{}, fmt.Errorf("scan service-account credential: %w", err)
	}
	decodedDigest, err := base64.RawURLEncoding.DecodeString(digest)
	if err != nil || len(decodedDigest) != len(credential.Stored.Digest) {
		return serviceaccountcredential.Credential{},
			serviceaccountcredential.ServiceAccount{}, errors.New("stored service-account credential digest is invalid")
	}
	copy(credential.Stored.Digest[:], decodedDigest)
	if err := json.Unmarshal([]byte(permissions), &credential.Stored.Permissions); err != nil {
		return serviceaccountcredential.Credential{},
			serviceaccountcredential.ServiceAccount{}, fmt.Errorf("decode service-account credential permissions: %w", err)
	}
	for _, permission := range credential.Stored.Permissions {
		if !permission.Valid() || permission == domain.PermissionInternalAll {
			return serviceaccountcredential.Credential{},
				serviceaccountcredential.ServiceAccount{}, errors.New("stored service-account credential permission is invalid")
		}
	}
	credential.Stored.CreatedAt, err = parseCredentialTime(createdAt)
	if err != nil {
		return serviceaccountcredential.Credential{}, serviceaccountcredential.ServiceAccount{}, err
	}
	credential.Stored.ExpiresAt, err = parseCredentialTime(expiresAt)
	if err != nil {
		return serviceaccountcredential.Credential{}, serviceaccountcredential.ServiceAccount{}, err
	}
	credential.Stored.LastUsedAt, err = parseOptionalCredentialTime(lastUsedAt)
	if err != nil {
		return serviceaccountcredential.Credential{}, serviceaccountcredential.ServiceAccount{}, err
	}
	credential.Stored.RotatedAt, err = parseOptionalCredentialTimePointer(rotatedAt)
	if err != nil {
		return serviceaccountcredential.Credential{}, serviceaccountcredential.ServiceAccount{}, err
	}
	credential.Stored.OverlapExpiresAt, err = parseOptionalCredentialTimePointer(overlapExpiresAt)
	if err != nil {
		return serviceaccountcredential.Credential{}, serviceaccountcredential.ServiceAccount{}, err
	}
	credential.Stored.RevokedAt, err = parseOptionalCredentialTimePointer(revokedAt)
	if err != nil {
		return serviceaccountcredential.Credential{}, serviceaccountcredential.ServiceAccount{}, err
	}
	account.PrincipalID = credential.ServiceAccountPrincipalID
	account.ProjectID = domain.ProjectID(projectID.String)
	account.CreatedAt, err = parseCredentialTime(accountCreatedAt)
	if err != nil {
		return serviceaccountcredential.Credential{}, serviceaccountcredential.ServiceAccount{}, err
	}
	return credential, account, nil
}

func scanCredentialMetadata(row scanner) (serviceaccountcredential.CredentialMetadata, error) {
	var metadata serviceaccountcredential.CredentialMetadata
	var permissions, createdAt, expiresAt string
	var lastUsedAt, rotatedAt, overlapExpiresAt, revokedAt sql.NullString
	err := row.Scan(
		&metadata.ID, &metadata.ServiceAccountPrincipalID, &metadata.Prefix, &permissions,
		&metadata.CreatedBy, &createdAt, &expiresAt, &lastUsedAt,
		&rotatedAt, &overlapExpiresAt, &revokedAt,
	)
	if err != nil {
		return serviceaccountcredential.CredentialMetadata{}, fmt.Errorf(
			"scan service-account credential metadata: %w", err,
		)
	}
	if err := json.Unmarshal([]byte(permissions), &metadata.Permissions); err != nil {
		return serviceaccountcredential.CredentialMetadata{}, fmt.Errorf(
			"decode service-account credential metadata permissions: %w", err,
		)
	}
	for _, permission := range metadata.Permissions {
		if !permission.Valid() || permission == domain.PermissionInternalAll {
			return serviceaccountcredential.CredentialMetadata{},
				errors.New("stored service-account credential permission is invalid")
		}
	}
	metadata.CreatedAt, err = parseCredentialTime(createdAt)
	if err != nil {
		return serviceaccountcredential.CredentialMetadata{}, err
	}
	metadata.ExpiresAt, err = parseCredentialTime(expiresAt)
	if err != nil {
		return serviceaccountcredential.CredentialMetadata{}, err
	}
	metadata.LastUsedAt, err = parseOptionalCredentialTime(lastUsedAt)
	if err != nil {
		return serviceaccountcredential.CredentialMetadata{}, err
	}
	metadata.RotatedAt, err = parseOptionalCredentialTimePointer(rotatedAt)
	if err != nil {
		return serviceaccountcredential.CredentialMetadata{}, err
	}
	metadata.OverlapExpiresAt, err = parseOptionalCredentialTimePointer(overlapExpiresAt)
	if err != nil {
		return serviceaccountcredential.CredentialMetadata{}, err
	}
	metadata.RevokedAt, err = parseOptionalCredentialTimePointer(revokedAt)
	if err != nil {
		return serviceaccountcredential.CredentialMetadata{}, err
	}
	return metadata, nil
}

func formatCredentialTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func nullableCredentialTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return formatCredentialTime(value)
}

func nullableCredentialTimePointer(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatCredentialTime(*value)
}

func parseCredentialTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse service-account credential time: %w", err)
	}
	return parsed, nil
}

func parseOptionalCredentialTime(value sql.NullString) (time.Time, error) {
	if !value.Valid || value.String == "" {
		return time.Time{}, nil
	}
	return parseCredentialTime(value.String)
}

func parseOptionalCredentialTimePointer(value sql.NullString) (*time.Time, error) {
	if !value.Valid || value.String == "" {
		return nil, nil
	}
	parsed, err := parseCredentialTime(value.String)
	return &parsed, err
}
