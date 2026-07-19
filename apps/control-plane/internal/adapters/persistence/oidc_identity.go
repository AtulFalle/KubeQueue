package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/google/uuid"
)

var errOIDCIdentityCreatedConcurrently = errors.New("OIDC identity created concurrently")

const maxActiveOIDCProviders = 32

type oidcQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) ActiveOIDCProviders(ctx context.Context) ([]domain.OIDCProvider, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,installation_id,issuer,audience,
		COALESCE(authorized_party,''),allowed_algorithms,groups_claim,email_claim,name_claim,
		cache_ttl_seconds FROM identity_providers WHERE enabled=TRUE ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("read OIDC providers: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var providers []domain.OIDCProvider
	for rows.Next() {
		var provider domain.OIDCProvider
		var algorithms string
		var cacheTTLSeconds int64
		if err := rows.Scan(
			&provider.ID, &provider.InstallationID, &provider.Issuer, &provider.Audience,
			&provider.AuthorizedParty, &algorithms, &provider.GroupsClaim, &provider.EmailClaim,
			&provider.NameClaim, &cacheTTLSeconds,
		); err != nil {
			return nil, fmt.Errorf("scan OIDC provider: %w", err)
		}
		if err := json.Unmarshal([]byte(algorithms), &provider.AllowedAlgorithms); err != nil {
			return nil, fmt.Errorf("decode OIDC provider %q algorithms: %w", provider.ID, err)
		}
		provider.CacheTTL = time.Duration(cacheTTLSeconds) * time.Second
		if err := provider.Validate(); err != nil {
			return nil, fmt.Errorf("invalid OIDC provider %q: %w", provider.ID, err)
		}
		providers = append(providers, provider)
		if len(providers) > maxActiveOIDCProviders {
			return nil, fmt.Errorf("active OIDC providers exceed limit %d", maxActiveOIDCProviders)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read OIDC providers: %w", err)
	}
	return providers, nil
}

func (s *Store) ResolveOIDCPrincipal(
	ctx context.Context,
	provider domain.OIDCProvider,
	claims domain.OIDCIdentityClaims,
) (domain.Actor, error) {
	if err := provider.Validate(); err != nil {
		return domain.Actor{}, err
	}
	if claims.Issuer != provider.Issuer || strings.TrimSpace(claims.Subject) == "" {
		return domain.Actor{}, errors.New("OIDC identity does not match provider")
	}
	if actor, found, err := s.lookupOIDCServiceAccountActor(
		ctx, provider, claims,
	); err != nil {
		return domain.Actor{}, err
	} else if found {
		return actor, nil
	}
	if actor, found, err := s.lookupOIDCActor(ctx, s.db, provider, claims); err != nil {
		return domain.Actor{}, err
	} else if found {
		return s.synchronizeExistingOIDCPrincipal(ctx, provider, claims, actor)
	}

	principalID := domain.PrincipalID("oidc_" + strings.ReplaceAll(uuid.NewString(), "-", ""))
	externalID := "oidc_identity_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	displayName := strings.TrimSpace(claims.DisplayName)
	if displayName == "" {
		displayName = strings.TrimSpace(claims.Email)
	}
	if displayName == "" {
		displayName = claims.Subject
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		teamIDs, err := s.matchingJITTeams(ctx, tx, provider, claims)
		if err != nil {
			return err
		}
		if len(teamIDs) == 0 {
			return domain.ErrJITProvisioningDenied
		}
		if _, err := tx.ExecContext(ctx, s.bind(
			`INSERT INTO principals(id,installation_id,kind,display_name,created_at)
			 VALUES(?,?,?,?,?)`,
		), principalID, provider.InstallationID, "HUMAN", displayName, now); err != nil {
			return fmt.Errorf("create OIDC principal: %w", err)
		}
		result, err := tx.ExecContext(ctx, s.bind(
			`INSERT INTO external_identities(id,principal_id,issuer,subject,created_at)
			 VALUES(?,?,?,?,?) ON CONFLICT(issuer,subject) DO NOTHING`,
		), externalID, principalID, provider.Issuer, claims.Subject, now)
		if err != nil {
			return fmt.Errorf("create external identity: %w", err)
		}
		created, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("inspect external identity creation: %w", err)
		}
		if created == 0 {
			return errOIDCIdentityCreatedConcurrently
		}
		for _, teamID := range teamIDs {
			if _, err := tx.ExecContext(ctx, s.bind(
				`INSERT INTO team_memberships
				 (team_id,principal_id,created_at,source_identity_provider_id)
				 VALUES(?,?,?,?) ON CONFLICT(team_id,principal_id) DO NOTHING`,
			), teamID, principalID, now, provider.ID); err != nil {
				return fmt.Errorf("create OIDC team membership: %w", err)
			}
		}
		return nil
	})
	if errors.Is(err, errOIDCIdentityCreatedConcurrently) {
		actor, found, lookupErr := s.lookupOIDCActor(ctx, s.db, provider, claims)
		if lookupErr != nil {
			return domain.Actor{}, lookupErr
		}
		if found {
			return s.synchronizeExistingOIDCPrincipal(ctx, provider, claims, actor)
		}
	}
	if err != nil {
		return domain.Actor{}, err
	}
	return domain.Actor{PrincipalID: principalID, InstallationID: provider.InstallationID}, nil
}

func (s *Store) lookupOIDCServiceAccountActor(
	ctx context.Context,
	provider domain.OIDCProvider,
	claims domain.OIDCIdentityClaims,
) (domain.Actor, bool, error) {
	var actor domain.Actor
	var projectID sql.NullString
	err := s.db.QueryRowContext(ctx, s.bind(
		`SELECT p.id,p.installation_id,sa.project_id
		 FROM service_account_oidc_identities oi
		 JOIN service_accounts sa ON sa.principal_id=oi.service_account_principal_id
		 JOIN principals p ON p.id=sa.principal_id
		   AND p.kind='SERVICE_ACCOUNT' AND p.disabled_at IS NULL
		 WHERE oi.issuer=? AND oi.subject=? AND p.installation_id=?`,
	), provider.Issuer, claims.Subject, provider.InstallationID).Scan(
		&actor.PrincipalID, &actor.InstallationID, &projectID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Actor{}, false, nil
	}
	if err != nil {
		return domain.Actor{}, false, fmt.Errorf("resolve OIDC service-account identity: %w", err)
	}
	actor.AuthenticationMethod = domain.AuthenticationMethodOIDCClientCredentials
	actor.CredentialScope = domain.AccessScope{InstallationID: actor.InstallationID}
	if projectID.Valid && projectID.String != "" {
		actor.CredentialScope.ProjectIDs = []domain.ProjectID{domain.ProjectID(projectID.String)}
	} else {
		actor.CredentialScope.InstallationWide = true
	}
	return actor, true, nil
}

func (s *Store) synchronizeExistingOIDCPrincipal(
	ctx context.Context,
	provider domain.OIDCProvider,
	claims domain.OIDCIdentityClaims,
	actor domain.Actor,
) (domain.Actor, error) {
	allowed := false
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		current, found, err := s.lookupOIDCActor(ctx, tx, provider, claims)
		if err != nil {
			return err
		}
		if !found ||
			current.PrincipalID != actor.PrincipalID ||
			current.InstallationID != actor.InstallationID {
			return domain.ErrJITProvisioningDenied
		}
		teamIDs, err := s.matchingJITTeams(ctx, tx, provider, claims)
		if err != nil {
			return err
		}
		if err := s.synchronizeOIDCMemberships(
			ctx, tx, provider.ID, actor.PrincipalID, teamIDs,
		); err != nil {
			return err
		}
		if len(teamIDs) > 0 {
			allowed = true
			return nil
		}
		allowed, err = s.hasDirectRoleGrant(ctx, tx, actor)
		return err
	})
	if err != nil {
		return domain.Actor{}, err
	}
	if !allowed {
		return domain.Actor{}, domain.ErrJITProvisioningDenied
	}
	return actor, nil
}

func (s *Store) synchronizeOIDCMemberships(
	ctx context.Context,
	tx *sql.Tx,
	providerID string,
	principalID domain.PrincipalID,
	desired []string,
) error {
	desiredTeams := make(map[string]struct{}, len(desired))
	for _, teamID := range desired {
		desiredTeams[teamID] = struct{}{}
	}
	rows, err := tx.QueryContext(ctx, s.bind(
		`SELECT team_id FROM team_memberships
		 WHERE principal_id=? AND source_identity_provider_id=?`,
	), principalID, providerID)
	if err != nil {
		return fmt.Errorf("read provider-managed team memberships: %w", err)
	}
	var stale []string
	for rows.Next() {
		var teamID string
		if err := rows.Scan(&teamID); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan provider-managed team membership: %w", err)
		}
		if _, keep := desiredTeams[teamID]; !keep {
			stale = append(stale, teamID)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("read provider-managed team memberships: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close provider-managed team memberships: %w", err)
	}
	for _, teamID := range stale {
		if _, err := tx.ExecContext(ctx, s.bind(
			`DELETE FROM team_memberships
			 WHERE team_id=? AND principal_id=? AND source_identity_provider_id=?`,
		), teamID, principalID, providerID); err != nil {
			return fmt.Errorf("remove stale provider-managed team membership: %w", err)
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	changed := len(stale) > 0
	for _, teamID := range desired {
		result, err := tx.ExecContext(ctx, s.bind(
			`INSERT INTO team_memberships
			 (team_id,principal_id,created_at,source_identity_provider_id)
			 VALUES(?,?,?,?) ON CONFLICT(team_id,principal_id) DO NOTHING`,
		), teamID, principalID, now, providerID)
		if err != nil {
			return fmt.Errorf("add provider-managed team membership: %w", err)
		}
		added, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("inspect provider-managed team membership: %w", err)
		}
		changed = changed || added > 0
	}
	if changed {
		if _, err := tx.ExecContext(ctx, s.bind(
			`UPDATE principals SET authz_generation=authz_generation+1 WHERE id=?`,
		), principalID); err != nil {
			return fmt.Errorf("invalidate sessions after membership change: %w", err)
		}
	}
	return nil
}

func (s *Store) hasDirectRoleGrant(
	ctx context.Context,
	tx *sql.Tx,
	actor domain.Actor,
) (bool, error) {
	rows, err := tx.QueryContext(ctx, s.bind(
		`SELECT rd.id,rd.built_in,rd.permissions
		 FROM role_bindings rb
		 JOIN role_definitions rd ON rd.id=rb.role_definition_id
		   AND rd.installation_id=rb.installation_id
		 WHERE rb.principal_id=? AND rb.installation_id=? AND (
		   rb.scope_type='INSTALLATION' OR EXISTS (
		     SELECT 1 FROM projects p
		     WHERE p.id=rb.project_id AND p.installation_id=rb.installation_id
		   )
		 )`,
	), actor.PrincipalID, actor.InstallationID)
	if err != nil {
		return false, fmt.Errorf("read direct OIDC principal grants: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var roleID, encoded string
		var builtIn bool
		if err := rows.Scan(&roleID, &builtIn, &encoded); err != nil {
			return false, fmt.Errorf("scan direct OIDC principal grant: %w", err)
		}
		valid, err := hasEffectivePermissions(roleID, builtIn, encoded)
		if err != nil {
			return false, fmt.Errorf("decode direct OIDC principal grant: %w", err)
		}
		if valid {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("read direct OIDC principal grants: %w", err)
	}
	return false, nil
}

func (s *Store) lookupOIDCActor(
	ctx context.Context,
	queryer oidcQueryer,
	provider domain.OIDCProvider,
	claims domain.OIDCIdentityClaims,
) (domain.Actor, bool, error) {
	var actor domain.Actor
	var disabledAt sql.NullString
	err := queryer.QueryRowContext(ctx, s.bind(
		`SELECT p.id,p.installation_id,p.disabled_at
		 FROM external_identities ei JOIN principals p ON p.id=ei.principal_id
		 WHERE ei.issuer=? AND ei.subject=?`,
	), provider.Issuer, claims.Subject).Scan(
		&actor.PrincipalID, &actor.InstallationID, &disabledAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Actor{}, false, nil
	}
	if err != nil {
		return domain.Actor{}, false, fmt.Errorf("resolve external identity: %w", err)
	}
	if actor.InstallationID != provider.InstallationID {
		return domain.Actor{}, true, domain.ErrJITProvisioningDenied
	}
	if disabledAt.Valid {
		return domain.Actor{}, true, domain.ErrIdentityDisabled
	}
	return actor, true, nil
}

func (s *Store) matchingJITTeams(
	ctx context.Context,
	queryer oidcQueryer,
	provider domain.OIDCProvider,
	claims domain.OIDCIdentityClaims,
) ([]string, error) {
	rows, err := queryer.QueryContext(ctx, s.bind(
		`SELECT m.mapping_type,m.claim_value,m.team_id,rd.id,rd.built_in,rd.permissions
		 FROM oidc_jit_provisioning_mappings m
		 JOIN teams t ON t.id=m.team_id
		 JOIN role_bindings rb ON rb.team_id=m.team_id AND rb.installation_id=?
		 JOIN role_definitions rd ON rd.id=rb.role_definition_id
		   AND rd.installation_id=rb.installation_id
		 WHERE m.identity_provider_id=? AND t.installation_id=? AND (
		   rb.scope_type='INSTALLATION' OR EXISTS (
		     SELECT 1 FROM projects p
		     WHERE p.id=rb.project_id AND p.installation_id=rb.installation_id
		   )
		 )`,
	), provider.InstallationID, provider.ID, provider.InstallationID)
	if err != nil {
		return nil, fmt.Errorf("read OIDC JIT mappings: %w", err)
	}
	defer func() { _ = rows.Close() }()

	emailDomain := ""
	if _, domainPart, found := strings.Cut(strings.TrimSpace(claims.Email), "@"); found && claims.EmailVerified {
		emailDomain = strings.ToLower(domainPart)
	}
	groups := make(map[string]struct{}, len(claims.Groups))
	for _, group := range claims.Groups {
		groups[group] = struct{}{}
	}
	var teamIDs []string
	seen := make(map[string]struct{})
	for rows.Next() {
		var mappingType, value, teamID, roleID, permissions string
		var builtIn bool
		if err := rows.Scan(
			&mappingType, &value, &teamID, &roleID, &builtIn, &permissions,
		); err != nil {
			return nil, fmt.Errorf("scan OIDC JIT mapping: %w", err)
		}
		validGrant, err := hasEffectivePermissions(roleID, builtIn, permissions)
		if err != nil {
			return nil, fmt.Errorf("decode OIDC JIT mapping grant: %w", err)
		}
		if !validGrant {
			continue
		}
		matches := false
		switch mappingType {
		case domain.OIDCMappingGroup:
			_, matches = groups[value]
		case domain.OIDCMappingDomain:
			matches = emailDomain != "" && strings.EqualFold(emailDomain, value)
		}
		if matches {
			if _, duplicate := seen[teamID]; !duplicate {
				teamIDs = append(teamIDs, teamID)
				seen[teamID] = struct{}{}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read OIDC JIT mappings: %w", err)
	}
	return teamIDs, nil
}

func hasEffectivePermissions(roleID string, builtIn bool, encoded string) (bool, error) {
	var permissions []domain.Permission
	if err := json.Unmarshal([]byte(encoded), &permissions); err != nil {
		return false, err
	}
	for _, permission := range permissions {
		if permission.Valid() && (permission != domain.PermissionInternalAll ||
			(builtIn && roleID == "installation_owner")) {
			return true, nil
		}
	}
	return false, nil
}
