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
)

type accessScanner interface {
	Scan(...any) error
}

func accessNotFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrAccessResourceNotFound
	}
	return err
}

func accessWriteError(err error) error {
	if err == nil {
		return nil
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "unique") || strings.Contains(message, "duplicate") {
		return domain.ErrAccessConflict
	}
	if strings.Contains(message, "foreign key") || strings.Contains(message, "check constraint") {
		return domain.ErrAccessResourceNotFound
	}
	return err
}

func accessTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}

func scanManagedProject(row accessScanner) (domain.ManagedProject, error) {
	var project domain.ManagedProject
	var createdAt string
	err := row.Scan(&project.ID, &project.InstallationID, &project.Name, &createdAt)
	project.CreatedAt = accessTime(createdAt)
	return project, accessNotFound(err)
}

func scanTeam(row accessScanner) (domain.Team, error) {
	var team domain.Team
	var createdAt string
	err := row.Scan(&team.ID, &team.InstallationID, &team.Name, &createdAt)
	team.CreatedAt = accessTime(createdAt)
	return team, accessNotFound(err)
}

func scanManagedPrincipal(row accessScanner) (domain.ManagedPrincipal, error) {
	var principal domain.ManagedPrincipal
	var kind, createdAt string
	var disabledAt sql.NullString
	var generation int64
	err := row.Scan(
		&principal.ID, &principal.InstallationID, &kind, &principal.DisplayName,
		&disabledAt, &generation, &createdAt,
	)
	if err != nil {
		return domain.ManagedPrincipal{}, accessNotFound(err)
	}
	principal.Kind = domain.PrincipalKind(kind)
	principal.DisabledAt = parseTime(disabledAt)
	if generation > 0 {
		principal.AuthorizationGeneration = uint64(generation)
	}
	principal.CreatedAt = accessTime(createdAt)
	return principal, nil
}

func scanRoleDefinition(row accessScanner) (domain.RoleDefinition, error) {
	var role domain.RoleDefinition
	var scope, permissions, createdAt string
	var revision int64
	err := row.Scan(
		&role.ID, &role.InstallationID, &role.Name, &scope,
		&permissions, &role.BuiltIn, &revision, &createdAt,
	)
	if err != nil {
		return domain.RoleDefinition{}, accessNotFound(err)
	}
	role.Scope = domain.RoleScope(scope)
	if err := json.Unmarshal([]byte(permissions), &role.Permissions); err != nil {
		return domain.RoleDefinition{}, fmt.Errorf("decode role definition: %w", err)
	}
	if revision < 1 {
		return domain.RoleDefinition{}, fmt.Errorf("decode role definition: invalid revision")
	}
	role.Revision = uint64(revision)
	role.CreatedAt = accessTime(createdAt)
	return role, nil
}

func scanRoleBinding(row accessScanner) (domain.RoleBinding, error) {
	var binding domain.RoleBinding
	var scope, createdAt string
	var projectID, principalID, teamID sql.NullString
	err := row.Scan(
		&binding.ID, &binding.InstallationID, &binding.RoleDefinitionID, &scope,
		&projectID, &principalID, &teamID, &createdAt,
	)
	if err != nil {
		return domain.RoleBinding{}, accessNotFound(err)
	}
	binding.Scope = domain.RoleScope(scope)
	binding.ProjectID = domain.ProjectID(projectID.String)
	binding.PrincipalID = domain.PrincipalID(principalID.String)
	binding.TeamID = domain.TeamID(teamID.String)
	if principalID.Valid {
		binding.SubjectKind = domain.BindingSubjectPrincipal
	} else {
		binding.SubjectKind = domain.BindingSubjectTeam
	}
	binding.CreatedAt = accessTime(createdAt)
	return binding, nil
}

func (s *Store) invalidatePrincipal(
	ctx context.Context, tx *sql.Tx, installationID domain.InstallationID, principalID domain.PrincipalID,
) error {
	result, err := tx.ExecContext(ctx, s.bind(
		`UPDATE principals SET authz_generation=authz_generation+1
		 WHERE id=? AND installation_id=?`,
	), principalID, installationID)
	if err != nil {
		return fmt.Errorf("increment authorization generation: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect authorization generation increment: %w", err)
	}
	if affected != 1 {
		return domain.ErrAccessResourceNotFound
	}
	return nil
}

func (s *Store) invalidateBindingSubject(
	ctx context.Context, tx *sql.Tx, binding domain.RoleBinding,
) error {
	var (
		result sql.Result
		err    error
	)
	if binding.SubjectKind == domain.BindingSubjectPrincipal {
		result, err = tx.ExecContext(ctx, s.bind(
			`UPDATE principals SET authz_generation=authz_generation+1
			 WHERE id=? AND installation_id=?`,
		), binding.PrincipalID, binding.InstallationID)
	} else {
		result, err = tx.ExecContext(ctx, s.bind(
			`UPDATE principals SET authz_generation=authz_generation+1
			 WHERE installation_id=? AND id IN (
			   SELECT principal_id FROM team_memberships WHERE team_id=?
			 )`,
		), binding.InstallationID, binding.TeamID)
	}
	if err != nil {
		return fmt.Errorf("invalidate role-binding sessions: %w", err)
	}
	if binding.SubjectKind == domain.BindingSubjectPrincipal {
		affected, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return rowsErr
		}
		if affected != 1 {
			return domain.ErrAccessResourceNotFound
		}
	}
	return nil
}

func (s *Store) ensureInstallationOwner(
	ctx context.Context, tx *sql.Tx, installationID domain.InstallationID,
) error {
	if s.postgres {
		var locked domain.InstallationID
		if err := tx.QueryRowContext(ctx,
			`SELECT id FROM installations WHERE id=$1 FOR UPDATE`,
			installationID,
		).Scan(&locked); err != nil {
			return accessNotFound(err)
		}
	}
	var owners int
	err := tx.QueryRowContext(ctx, s.bind(
		`SELECT COUNT(DISTINCT p.id)
		 FROM principals p
		 WHERE p.installation_id=? AND p.disabled_at IS NULL AND (
		   EXISTS (
		     SELECT 1 FROM role_bindings rb
		     JOIN role_definitions rd ON rd.id=rb.role_definition_id
		       AND rd.installation_id=rb.installation_id
		     WHERE rb.installation_id=p.installation_id
		       AND rd.id='installation_owner' AND rd.built_in=TRUE
		       AND rb.scope_type='INSTALLATION' AND rb.principal_id=p.id
		   ) OR EXISTS (
		     SELECT 1 FROM team_memberships tm
		     JOIN role_bindings rb ON rb.team_id=tm.team_id
		     JOIN role_definitions rd ON rd.id=rb.role_definition_id
		       AND rd.installation_id=rb.installation_id
		     WHERE tm.principal_id=p.id AND rb.installation_id=p.installation_id
		       AND rd.id='installation_owner' AND rd.built_in=TRUE
		       AND rb.scope_type='INSTALLATION'
		   )
		 )`,
	), installationID).Scan(&owners)
	if err != nil {
		return fmt.Errorf("count installation owners: %w", err)
	}
	if owners == 0 {
		return domain.ErrFinalInstallationOwner
	}
	return nil
}
