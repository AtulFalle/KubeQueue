package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
)

const managedIdentityProviderColumns = `id,installation_id,display_name,issuer,audience,
	client_id,client_secret_ciphertext,client_secret_reference,redirect_uri,
	COALESCE(authorized_party,''),allowed_algorithms,mapping_type,mapping_value,
	groups_claim,email_claim,name_claim,cache_ttl_seconds,enabled,test_status,
	COALESCE(tested_at,''),test_message,COALESCE(tested_version,0),version,created_at,updated_at`

type identityProviderScanner interface {
	Scan(...any) error
}

func (s *Store) ListIdentityProviders(
	ctx context.Context, installationID domain.InstallationID,
) ([]domain.ManagedIdentityProvider, error) {
	query := `SELECT ` + managedIdentityProviderColumns + ` FROM identity_providers`
	args := []any{}
	if installationID != "" {
		query += ` WHERE installation_id=?`
		args = append(args, installationID)
	}
	query += ` ORDER BY id`
	rows, err := s.db.QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return nil, fmt.Errorf("list identity providers: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var providers []domain.ManagedIdentityProvider
	for rows.Next() {
		provider, err := scanManagedIdentityProvider(rows)
		if err != nil {
			return nil, err
		}
		providers = append(providers, provider)
	}
	return providers, rows.Err()
}

func (s *Store) IdentityProvider(
	ctx context.Context, installationID domain.InstallationID, id string,
) (domain.ManagedIdentityProvider, error) {
	provider, err := scanManagedIdentityProvider(s.db.QueryRowContext(ctx, s.bind(
		`SELECT `+managedIdentityProviderColumns+` FROM identity_providers
		 WHERE installation_id=? AND id=?`,
	), installationID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ManagedIdentityProvider{}, domain.ErrIdentityProviderNotFound
	}
	return provider, err
}

func (s *Store) CreateIdentityProvider(
	ctx context.Context, provider domain.ManagedIdentityProvider,
) (domain.ManagedIdentityProvider, error) {
	algorithms, err := json.Marshal(provider.Configuration.AllowedAlgorithms)
	if err != nil {
		return domain.ManagedIdentityProvider{}, err
	}
	err = s.auditTransaction(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, s.bind(
			`INSERT INTO identity_providers(
			 id,installation_id,display_name,issuer,audience,client_id,
			 client_secret_ciphertext,client_secret_reference,redirect_uri,authorized_party,
			 allowed_algorithms,mapping_type,mapping_value,groups_claim,email_claim,name_claim,
			 cache_ttl_seconds,enabled,test_status,test_message,version,created_at,updated_at)
			 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		), provider.ID, provider.InstallationID, provider.Configuration.DisplayName,
			provider.Configuration.Issuer, provider.Configuration.Audience,
			provider.Configuration.ClientID, provider.ClientSecretCiphertext,
			provider.Configuration.ClientSecretRef, provider.Configuration.RedirectURI,
			nullableString(provider.Configuration.AuthorizedParty), string(algorithms),
			provider.Configuration.MappingType, provider.Configuration.MappingValue,
			provider.Configuration.GroupsClaim, provider.Configuration.EmailClaim,
			provider.Configuration.NameClaim, int64(provider.Configuration.CacheTTL/time.Second),
			false, provider.TestStatus, "", provider.Version, formatTime(&provider.CreatedAt),
			formatTime(&provider.UpdatedAt))
		return err
	})
	if err != nil {
		return domain.ManagedIdentityProvider{}, fmt.Errorf("create identity provider: %w", err)
	}
	return s.IdentityProvider(ctx, provider.InstallationID, provider.ID)
}

func (s *Store) UpdateIdentityProvider(
	ctx context.Context, provider domain.ManagedIdentityProvider, expected uint64,
) (domain.ManagedIdentityProvider, error) {
	algorithms, err := json.Marshal(provider.Configuration.AllowedAlgorithms)
	if err != nil {
		return domain.ManagedIdentityProvider{}, err
	}
	var changed int64
	err = s.auditTransaction(ctx, func(tx *sql.Tx) error {
		var enabled bool
		err := tx.QueryRowContext(ctx, s.bind(
			`SELECT enabled FROM identity_providers
			 WHERE installation_id=? AND id=? AND version=?`,
		), provider.InstallationID, provider.ID, expected).Scan(&enabled)
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ErrIdentityProviderConflict
		}
		if err != nil {
			return err
		}
		if enabled {
			safe, err := s.hasAlternativeOwnerLogin(
				ctx, tx, provider.InstallationID, provider.ID,
			)
			if err != nil {
				return err
			}
			if !safe {
				return domain.ErrIdentityProviderUnsafeChange
			}
		}
		result, err := tx.ExecContext(ctx, s.bind(
			`UPDATE identity_providers SET display_name=?,issuer=?,audience=?,client_id=?,
			 client_secret_ciphertext=?,client_secret_reference=?,redirect_uri=?,authorized_party=?,
			 allowed_algorithms=?,mapping_type=?,mapping_value=?,groups_claim=?,email_claim=?,
			 name_claim=?,cache_ttl_seconds=?,enabled=FALSE,test_status='NOT_TESTED',
			 tested_at=NULL,test_message='',tested_version=NULL,version=version+1,updated_at=?
			 WHERE installation_id=? AND id=? AND version=?`,
		), provider.Configuration.DisplayName, provider.Configuration.Issuer,
			provider.Configuration.Audience, provider.Configuration.ClientID,
			provider.ClientSecretCiphertext, provider.Configuration.ClientSecretRef,
			provider.Configuration.RedirectURI, nullableString(provider.Configuration.AuthorizedParty),
			string(algorithms), provider.Configuration.MappingType, provider.Configuration.MappingValue,
			provider.Configuration.GroupsClaim, provider.Configuration.EmailClaim,
			provider.Configuration.NameClaim, int64(provider.Configuration.CacheTTL/time.Second),
			formatTime(&provider.UpdatedAt), provider.InstallationID, provider.ID, expected)
		if err != nil {
			return err
		}
		changed, err = result.RowsAffected()
		return err
	})
	if err != nil {
		return domain.ManagedIdentityProvider{}, fmt.Errorf("update identity provider: %w", err)
	}
	if changed != 1 {
		return domain.ManagedIdentityProvider{}, domain.ErrIdentityProviderConflict
	}
	return s.IdentityProvider(ctx, provider.InstallationID, provider.ID)
}

func (s *Store) RecordIdentityProviderTest(
	ctx context.Context, installationID domain.InstallationID, id string, expected uint64,
	passed bool, message string, testedAt time.Time,
) (domain.ManagedIdentityProvider, error) {
	status := domain.IdentityProviderTestFailed
	if passed {
		status = domain.IdentityProviderTestPassed
	}
	var changed int64
	err := s.auditTransaction(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, s.bind(
			`UPDATE identity_providers SET test_status=?,tested_at=?,test_message=?,
			 tested_version=version+1,version=version+1,updated_at=?
			 WHERE installation_id=? AND id=? AND version=?`,
		), status, formatTime(&testedAt), message, formatTime(&testedAt),
			installationID, id, expected)
		if err != nil {
			return err
		}
		changed, err = result.RowsAffected()
		return err
	})
	if err != nil {
		return domain.ManagedIdentityProvider{}, err
	}
	if changed != 1 {
		return domain.ManagedIdentityProvider{}, domain.ErrIdentityProviderConflict
	}
	return s.IdentityProvider(ctx, installationID, id)
}

func (s *Store) SetIdentityProviderEnabled(
	ctx context.Context, installationID domain.InstallationID, id string, expected uint64,
	enabled bool, changedAt time.Time,
) (domain.ManagedIdentityProvider, error) {
	var changed int64
	err := s.auditTransaction(ctx, func(tx *sql.Tx) error {
		if !enabled {
			safe, err := s.hasAlternativeOwnerLogin(ctx, tx, installationID, id)
			if err != nil {
				return err
			}
			if !safe {
				return domain.ErrIdentityProviderUnsafeChange
			}
		}
		testClause := ""
		if enabled {
			testClause = ` AND test_status='PASSED' AND tested_version=version`
		}
		result, err := tx.ExecContext(ctx, s.bind(
			`UPDATE identity_providers SET enabled=?,
			 tested_version=CASE WHEN test_status='PASSED' THEN version+1 ELSE tested_version END,
			 version=version+1,updated_at=?
			 WHERE installation_id=? AND id=? AND version=?`+testClause,
		), enabled, formatTime(&changedAt), installationID, id, expected)
		if err != nil {
			return err
		}
		changed, err = result.RowsAffected()
		return err
	})
	if err != nil {
		return domain.ManagedIdentityProvider{}, err
	}
	if changed != 1 {
		return domain.ManagedIdentityProvider{}, domain.ErrIdentityProviderConflict
	}
	return s.IdentityProvider(ctx, installationID, id)
}

func (s *Store) hasAlternativeOwnerLogin(
	ctx context.Context, tx *sql.Tx, installationID domain.InstallationID, excludedID string,
) (bool, error) {
	var count int
	err := tx.QueryRowContext(ctx, s.bind(
		`SELECT COUNT(*) FROM principals p
		 JOIN role_bindings rb ON rb.principal_id=p.id AND rb.installation_id=p.installation_id
		 JOIN role_definitions rd ON rd.id=rb.role_definition_id
		 WHERE p.installation_id=? AND p.kind='HUMAN' AND p.disabled_at IS NULL
		   AND rb.scope_type='INSTALLATION' AND rd.id='installation_owner' AND rd.built_in=TRUE
		   AND (EXISTS(SELECT 1 FROM local_accounts la WHERE la.principal_id=p.id)
		     OR EXISTS(SELECT 1 FROM external_identities ei
		       JOIN identity_providers ip ON ip.issuer=ei.issuer
		       WHERE ei.principal_id=p.id AND ip.installation_id=p.installation_id
		         AND ip.enabled=TRUE AND ip.id<>?))`,
	), installationID, excludedID).Scan(&count)
	return count > 0, err
}

func scanManagedIdentityProvider(
	scanner identityProviderScanner,
) (domain.ManagedIdentityProvider, error) {
	var provider domain.ManagedIdentityProvider
	var algorithms, testedAt, createdAt, updatedAt string
	var enabled bool
	var cacheTTL int64
	err := scanner.Scan(
		&provider.ID, &provider.InstallationID, &provider.Configuration.DisplayName,
		&provider.Configuration.Issuer, &provider.Configuration.Audience,
		&provider.Configuration.ClientID, &provider.ClientSecretCiphertext,
		&provider.Configuration.ClientSecretRef, &provider.Configuration.RedirectURI,
		&provider.Configuration.AuthorizedParty, &algorithms, &provider.Configuration.MappingType,
		&provider.Configuration.MappingValue, &provider.Configuration.GroupsClaim,
		&provider.Configuration.EmailClaim, &provider.Configuration.NameClaim, &cacheTTL,
		&enabled, &provider.TestStatus, &testedAt, &provider.TestMessage,
		&provider.TestedVersion, &provider.Version, &createdAt, &updatedAt,
	)
	if err != nil {
		return provider, err
	}
	if err := json.Unmarshal([]byte(algorithms), &provider.Configuration.AllowedAlgorithms); err != nil {
		return provider, err
	}
	provider.Configuration.CacheTTL = time.Duration(cacheTTL) * time.Second
	provider.ClientSecretConfigured = provider.ClientSecretCiphertext != "" ||
		provider.Configuration.ClientSecretRef != ""
	provider.State = domain.IdentityProviderDisabled
	if enabled {
		provider.State = domain.IdentityProviderEnabled
	}
	if testedAt != "" {
		parsed := parseTime(sql.NullString{String: testedAt, Valid: true})
		provider.TestedAt = parsed
	}
	provider.CreatedAt = *parseTime(sql.NullString{String: createdAt, Valid: true})
	provider.UpdatedAt = *parseTime(sql.NullString{String: updatedAt, Valid: true})
	return provider, provider.Validate()
}
