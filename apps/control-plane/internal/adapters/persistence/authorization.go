package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
)

type resolvedGrant struct {
	roleID      string
	builtIn     bool
	scopeType   string
	projectID   domain.ProjectID
	permissions []domain.Permission
}

func (s *Store) Authorize(
	ctx context.Context,
	actor domain.Actor,
	permission domain.Permission,
	scope domain.AuthorizationScope,
) error {
	access, err := s.resolveAccess(ctx, actor, permission)
	if err != nil {
		return err
	}
	if scope.InstallationID != "" && scope.InstallationID != actor.InstallationID {
		return domain.ErrAccessDenied
	}
	if access.InstallationWide {
		return nil
	}
	for _, projectID := range access.ProjectIDs {
		if projectID == scope.ProjectID && scope.ProjectID != "" {
			return nil
		}
	}
	return domain.ErrAccessDenied
}

func (s *Store) AccessibleScope(
	ctx context.Context,
	actor domain.Actor,
	permission domain.Permission,
) (domain.AccessScope, error) {
	return s.resolveAccess(ctx, actor, permission)
}

func (s *Store) resolveAccess(
	ctx context.Context,
	actor domain.Actor,
	permission domain.Permission,
) (domain.AccessScope, error) {
	result := domain.AccessScope{InstallationID: actor.InstallationID}
	if actor.PrincipalID == "" || actor.InstallationID == "" || !permission.Valid() {
		return result, domain.ErrAccessDenied
	}
	if actor.AuthenticationMethod == domain.AuthenticationMethodBreakGlass {
		if actor.PrincipalID != "break_glass_owner" ||
			actor.CredentialID == "" ||
			actor.CredentialScope.InstallationID != actor.InstallationID ||
			!actor.CredentialScope.InstallationWide ||
			len(actor.CredentialPermissions) != 1 ||
			actor.CredentialPermissions[0] != domain.PermissionInternalAll {
			return result, domain.ErrAccessDenied
		}
		result.InstallationWide = true
		return result, nil
	}
	var disabled sql.NullString
	var installationID domain.InstallationID
	err := s.db.QueryRowContext(ctx, s.bind(
		`SELECT installation_id,disabled_at FROM principals WHERE id=?`,
	), actor.PrincipalID).Scan(&installationID, &disabled)
	if errors.Is(err, sql.ErrNoRows) || disabled.Valid || installationID != actor.InstallationID {
		return result, domain.ErrAccessDenied
	}
	if err != nil {
		return result, fmt.Errorf("resolve principal: %w", err)
	}
	if actor.AuthenticationMethod == domain.AuthenticationMethodNativeServiceAccount {
		return nativeCredentialAccess(actor, permission)
	}

	rows, err := s.db.QueryContext(ctx, s.bind(
		`SELECT rd.id,rd.built_in,rb.scope_type,COALESCE(rb.project_id,''),rd.permissions
		 FROM role_bindings rb
		 JOIN role_definitions rd ON rd.id=rb.role_definition_id
		   AND rd.installation_id=rb.installation_id
		 WHERE rb.installation_id=? AND (
		   rb.principal_id=? OR rb.team_id IN (
		     SELECT team_id FROM team_memberships WHERE principal_id=?
		   )
		 ) AND (
		   rb.scope_type='INSTALLATION' OR EXISTS (
		     SELECT 1 FROM projects p
		     WHERE p.id=rb.project_id AND p.installation_id=?
		   )
		 )`,
	), actor.InstallationID, actor.PrincipalID, actor.PrincipalID, actor.InstallationID)
	if err != nil {
		return result, fmt.Errorf("resolve role bindings: %w", err)
	}
	defer func() { _ = rows.Close() }()

	projects := make(map[domain.ProjectID]struct{})
	for rows.Next() {
		var grant resolvedGrant
		var encoded string
		if err := rows.Scan(
			&grant.roleID, &grant.builtIn, &grant.scopeType, &grant.projectID, &encoded,
		); err != nil {
			return result, fmt.Errorf("scan role binding: %w", err)
		}
		if err := json.Unmarshal([]byte(encoded), &grant.permissions); err != nil {
			return result, fmt.Errorf("decode role permissions: %w", err)
		}
		matches := false
		for _, granted := range grant.permissions {
			if !granted.Valid() {
				continue
			}
			if granted == permission ||
				(granted == domain.PermissionInternalAll && grant.builtIn &&
					grant.roleID == "installation_owner") {
				matches = true
				break
			}
		}
		if !matches {
			continue
		}
		if grant.scopeType == "INSTALLATION" {
			result.InstallationWide = true
			result.ProjectIDs = nil
			if actor.AuthenticationMethod == domain.AuthenticationMethodOIDCClientCredentials {
				return s.boundOIDCServiceAccountAccess(ctx, actor, result)
			}
			return result, nil
		}
		if grant.scopeType == "PROJECT" && grant.projectID != "" {
			projects[grant.projectID] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return result, fmt.Errorf("read role bindings: %w", err)
	}
	for projectID := range projects {
		result.ProjectIDs = append(result.ProjectIDs, projectID)
	}
	if len(result.ProjectIDs) == 0 {
		return result, domain.ErrAccessDenied
	}
	if actor.AuthenticationMethod == domain.AuthenticationMethodOIDCClientCredentials {
		return s.boundOIDCServiceAccountAccess(ctx, actor, result)
	}
	return result, nil
}

func (s *Store) boundOIDCServiceAccountAccess(
	ctx context.Context,
	actor domain.Actor,
	granted domain.AccessScope,
) (domain.AccessScope, error) {
	result := domain.AccessScope{InstallationID: actor.InstallationID}
	var projectID sql.NullString
	var installationID domain.InstallationID
	err := s.db.QueryRowContext(ctx, s.bind(
		`SELECT p.installation_id,sa.project_id
		 FROM service_accounts sa JOIN principals p ON p.id=sa.principal_id
		 WHERE sa.principal_id=? AND p.kind='SERVICE_ACCOUNT' AND p.disabled_at IS NULL`,
	), actor.PrincipalID).Scan(&installationID, &projectID)
	if errors.Is(err, sql.ErrNoRows) || installationID != actor.InstallationID {
		return result, domain.ErrAccessDenied
	}
	if err != nil {
		return result, fmt.Errorf("resolve OIDC service-account scope: %w", err)
	}
	if !projectID.Valid || projectID.String == "" {
		return granted, nil
	}
	boundProject := domain.ProjectID(projectID.String)
	if granted.InstallationWide {
		result.ProjectIDs = []domain.ProjectID{boundProject}
		return result, nil
	}
	for _, candidate := range granted.ProjectIDs {
		if candidate == boundProject {
			result.ProjectIDs = []domain.ProjectID{boundProject}
			return result, nil
		}
	}
	return result, domain.ErrAccessDenied
}

func nativeCredentialAccess(
	actor domain.Actor,
	permission domain.Permission,
) (domain.AccessScope, error) {
	result := domain.AccessScope{InstallationID: actor.InstallationID}
	if actor.CredentialID == "" ||
		actor.CredentialScope.InstallationID != actor.InstallationID {
		return result, domain.ErrAccessDenied
	}
	granted := false
	for _, candidate := range actor.CredentialPermissions {
		if !candidate.Valid() || candidate == domain.PermissionInternalAll {
			return result, domain.ErrAccessDenied
		}
		if candidate == permission {
			granted = true
		}
	}
	if !granted {
		return result, domain.ErrAccessDenied
	}
	if actor.CredentialScope.InstallationWide {
		result.InstallationWide = true
		return result, nil
	}
	if len(actor.CredentialScope.ProjectIDs) != 1 ||
		actor.CredentialScope.ProjectIDs[0] == "" {
		return result, domain.ErrAccessDenied
	}
	result.ProjectIDs = []domain.ProjectID{actor.CredentialScope.ProjectIDs[0]}
	return result, nil
}
