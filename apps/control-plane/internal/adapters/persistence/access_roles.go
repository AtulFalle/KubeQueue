package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
)

const accessRoleColumns = `id,installation_id,name,scope_type,permissions,built_in,
	current_revision,created_at`
const accessBindingColumns = `id,installation_id,role_definition_id,scope_type,
	project_id,principal_id,team_id,created_at`

func (s *Store) ListRoleDefinitions(
	ctx context.Context, installationID domain.InstallationID, page domain.AccessPage,
) ([]domain.RoleDefinition, error) {
	page, err := page.Normalize()
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, s.bind(
		`SELECT `+accessRoleColumns+` FROM role_definitions
		 WHERE installation_id=? AND id>? ORDER BY id LIMIT ?`,
	), installationID, page.After, page.Limit)
	if err != nil {
		return nil, fmt.Errorf("list role definitions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	roles := make([]domain.RoleDefinition, 0, page.Limit)
	for rows.Next() {
		role, err := scanRoleDefinition(rows)
		if err != nil {
			return nil, fmt.Errorf("scan role definition: %w", err)
		}
		roles = append(roles, role)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read role definitions: %w", err)
	}
	return roles, nil
}

func (s *Store) RoleDefinition(
	ctx context.Context,
	installationID domain.InstallationID,
	id domain.RoleDefinitionID,
) (domain.RoleDefinition, error) {
	return scanRoleDefinition(s.db.QueryRowContext(ctx, s.bind(
		`SELECT `+accessRoleColumns+` FROM role_definitions
		 WHERE installation_id=? AND id=?`,
	), installationID, id))
}

func (s *Store) ListRoleDefinitionRevisions(
	ctx context.Context,
	installationID domain.InstallationID,
	id domain.RoleDefinitionID,
	page domain.AccessPage,
) ([]domain.RoleDefinition, error) {
	page, err := page.Normalize()
	if err != nil {
		return nil, err
	}
	beforeRevision := int64(math.MaxInt64)
	if page.After != "" {
		beforeRevision, err = strconv.ParseInt(page.After, 10, 64)
		if err != nil || beforeRevision < 1 {
			return nil, domain.ErrInvalidAccessChange
		}
	}
	var customRole int
	if err := s.db.QueryRowContext(ctx, s.bind(
		`SELECT COUNT(*) FROM role_definitions
		 WHERE installation_id=? AND id=? AND built_in=FALSE`,
	), installationID, id).Scan(&customRole); err != nil {
		return nil, fmt.Errorf("find custom role definition: %w", err)
	}
	if customRole != 1 {
		return nil, domain.ErrAccessResourceNotFound
	}
	rows, err := s.db.QueryContext(ctx, s.bind(
		`SELECT rd.id,rd.installation_id,rdr.name,rdr.scope_type,rdr.permissions,
		        FALSE,rdr.revision,rdr.created_at
		 FROM role_definition_revisions rdr
		 JOIN role_definitions rd ON rd.id=rdr.role_definition_id
		 WHERE rd.installation_id=? AND rd.id=? AND rd.built_in=FALSE
		   AND rdr.revision<?
		 ORDER BY rdr.revision DESC LIMIT ?`,
	), installationID, id, beforeRevision, page.Limit)
	if err != nil {
		return nil, fmt.Errorf("list role definition revisions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	revisions := make([]domain.RoleDefinition, 0, page.Limit)
	for rows.Next() {
		revision, err := scanRoleDefinition(rows)
		if err != nil {
			return nil, fmt.Errorf("scan role definition revision: %w", err)
		}
		revisions = append(revisions, revision)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read role definition revisions: %w", err)
	}
	return revisions, nil
}

func (s *Store) RoleDefinitionRevision(
	ctx context.Context,
	installationID domain.InstallationID,
	id domain.RoleDefinitionID,
	revision uint64,
) (domain.RoleDefinition, error) {
	if revision == 0 || revision > math.MaxInt64 {
		return domain.RoleDefinition{}, domain.ErrInvalidAccessChange
	}
	return scanRoleDefinition(s.db.QueryRowContext(ctx, s.bind(
		`SELECT rd.id,rd.installation_id,rdr.name,rdr.scope_type,rdr.permissions,
		        FALSE,rdr.revision,rdr.created_at
		 FROM role_definition_revisions rdr
		 JOIN role_definitions rd ON rd.id=rdr.role_definition_id
		 WHERE rd.installation_id=? AND rd.id=? AND rd.built_in=FALSE
		   AND rdr.revision=?`,
	), installationID, id, revision))
}

func (s *Store) CreateRoleDefinition(
	ctx context.Context, role domain.RoleDefinition,
) (domain.RoleDefinition, error) {
	encoded, err := json.Marshal(role.Permissions)
	if err != nil {
		return domain.RoleDefinition{}, fmt.Errorf("encode role permissions: %w", err)
	}
	err = s.auditTransaction(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, s.bind(
			`INSERT INTO role_definitions(
			   id,installation_id,name,scope_type,permissions,built_in,
			   current_revision,created_at
			 ) VALUES(?,?,?,?,?,FALSE,1,?)`,
		), role.ID, role.InstallationID, role.Name, role.Scope, string(encoded),
			role.CreatedAt.Format(accessTimestamp))
		if err := accessWriteError(err); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, s.bind(
			`INSERT INTO role_definition_revisions(
			   role_definition_id,revision,name,scope_type,permissions,created_at
			 ) VALUES(?,1,?,?,?,?)`,
		), role.ID, role.Name, role.Scope, string(encoded),
			role.CreatedAt.Format(accessTimestamp))
		return accessWriteError(err)
	})
	if err != nil {
		return domain.RoleDefinition{}, fmt.Errorf("create role definition: %w", err)
	}
	return s.RoleDefinition(ctx, role.InstallationID, role.ID)
}

func (s *Store) UpdateRoleDefinition(
	ctx context.Context, role domain.RoleDefinition, expectedRevision uint64,
) (domain.RoleDefinition, error) {
	if expectedRevision == 0 || expectedRevision >= math.MaxInt64 {
		return domain.RoleDefinition{}, domain.ErrAccessConflict
	}
	nextRevision := expectedRevision + 1
	encoded, err := json.Marshal(role.Permissions)
	if err != nil {
		return domain.RoleDefinition{}, fmt.Errorf("encode role permissions: %w", err)
	}
	err = s.auditTransaction(ctx, func(tx *sql.Tx) error {
		current, err := scanRoleDefinition(tx.QueryRowContext(ctx, s.bind(
			`SELECT `+accessRoleColumns+` FROM role_definitions
			 WHERE installation_id=? AND id=?`,
		), role.InstallationID, role.ID))
		if err != nil {
			return err
		}
		if current.BuiltIn {
			return domain.ErrInvalidAccessChange
		}
		if current.Revision != expectedRevision {
			return domain.ErrAccessConflict
		}
		if current.Scope != role.Scope {
			var bindings int
			if err := tx.QueryRowContext(ctx, s.bind(
				`SELECT COUNT(*) FROM role_bindings
				 WHERE installation_id=? AND role_definition_id=?`,
			), role.InstallationID, role.ID).Scan(&bindings); err != nil {
				return err
			}
			if bindings != 0 {
				return domain.ErrAccessConflict
			}
		}
		result, err := tx.ExecContext(ctx, s.bind(
			`UPDATE role_definitions
			 SET name=?,scope_type=?,permissions=?,current_revision=?
			 WHERE id=? AND installation_id=? AND built_in=FALSE
			   AND current_revision=?`,
		), role.Name, role.Scope, string(encoded), nextRevision,
			role.ID, role.InstallationID, expectedRevision)
		if err != nil {
			return accessWriteError(err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return domain.ErrAccessConflict
		}
		_, err = tx.ExecContext(ctx, s.bind(
			`INSERT INTO role_definition_revisions(
			   role_definition_id,revision,name,scope_type,permissions,created_at
			 ) VALUES(?,?,?,?,?,?)`,
		), role.ID, nextRevision, role.Name, role.Scope, string(encoded),
			time.Now().UTC().Format(accessTimestamp))
		if err != nil {
			return fmt.Errorf("append role definition revision: %w", accessWriteError(err))
		}
		_, err = tx.ExecContext(ctx, s.bind(
			`UPDATE principals SET authz_generation=authz_generation+1
			 WHERE installation_id=? AND id IN (
			   SELECT principal_id FROM role_bindings
			   WHERE installation_id=? AND role_definition_id=? AND principal_id IS NOT NULL
			   UNION
			   SELECT tm.principal_id FROM team_memberships tm
			   JOIN role_bindings rb ON rb.team_id=tm.team_id
			   WHERE rb.installation_id=? AND rb.role_definition_id=?
			 )`,
		), role.InstallationID, role.InstallationID, role.ID,
			role.InstallationID, role.ID)
		if err != nil {
			return fmt.Errorf("invalidate custom-role sessions: %w", err)
		}
		return nil
	})
	if err != nil {
		return domain.RoleDefinition{}, fmt.Errorf("update role definition: %w", err)
	}
	return s.RoleDefinition(ctx, role.InstallationID, role.ID)
}

func (s *Store) ListRoleBindings(
	ctx context.Context, installationID domain.InstallationID, page domain.AccessPage,
) ([]domain.RoleBinding, error) {
	page, err := page.Normalize()
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, s.bind(
		`SELECT `+accessBindingColumns+` FROM role_bindings
		 WHERE installation_id=? AND id>? ORDER BY id LIMIT ?`,
	), installationID, page.After, page.Limit)
	if err != nil {
		return nil, fmt.Errorf("list role bindings: %w", err)
	}
	defer func() { _ = rows.Close() }()
	bindings := make([]domain.RoleBinding, 0, page.Limit)
	for rows.Next() {
		binding, err := scanRoleBinding(rows)
		if err != nil {
			return nil, fmt.Errorf("scan role binding: %w", err)
		}
		bindings = append(bindings, binding)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read role bindings: %w", err)
	}
	return bindings, nil
}

func (s *Store) RoleBinding(
	ctx context.Context,
	installationID domain.InstallationID,
	id domain.RoleBindingID,
) (domain.RoleBinding, error) {
	return scanRoleBinding(s.db.QueryRowContext(ctx, s.bind(
		`SELECT `+accessBindingColumns+` FROM role_bindings
		 WHERE installation_id=? AND id=?`,
	), installationID, id))
}

func (s *Store) CreateRoleBinding(
	ctx context.Context, binding domain.RoleBinding,
) (domain.RoleBinding, error) {
	err := s.auditTransaction(ctx, func(tx *sql.Tx) error {
		if err := s.validateRoleBinding(ctx, tx, binding); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, s.bind(
			`INSERT INTO role_bindings(
			   id,installation_id,role_definition_id,scope_type,project_id,
			   principal_id,team_id,created_at
			 ) VALUES(?,?,?,?,?,?,?,?)`,
		), binding.ID, binding.InstallationID, binding.RoleDefinitionID, binding.Scope,
			nullableAccessID(string(binding.ProjectID)),
			nullableAccessID(string(binding.PrincipalID)),
			nullableAccessID(string(binding.TeamID)),
			binding.CreatedAt.Format(accessTimestamp))
		if err := accessWriteError(err); err != nil {
			return err
		}
		return s.invalidateBindingSubject(ctx, tx, binding)
	})
	if err != nil {
		return domain.RoleBinding{}, fmt.Errorf("create role binding: %w", err)
	}
	return s.RoleBinding(ctx, binding.InstallationID, binding.ID)
}

func (s *Store) UpdateRoleBinding(
	ctx context.Context, binding domain.RoleBinding,
) (domain.RoleBinding, error) {
	err := s.auditTransaction(ctx, func(tx *sql.Tx) error {
		current, err := scanRoleBinding(tx.QueryRowContext(ctx, s.bind(
			`SELECT `+accessBindingColumns+` FROM role_bindings
			 WHERE installation_id=? AND id=?`,
		), binding.InstallationID, binding.ID))
		if err != nil {
			return err
		}
		if err := s.validateRoleBinding(ctx, tx, binding); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, s.bind(
			`UPDATE role_bindings SET role_definition_id=?,scope_type=?,project_id=?,
			   principal_id=?,team_id=?
			 WHERE id=? AND installation_id=?`,
		), binding.RoleDefinitionID, binding.Scope,
			nullableAccessID(string(binding.ProjectID)),
			nullableAccessID(string(binding.PrincipalID)),
			nullableAccessID(string(binding.TeamID)),
			binding.ID, binding.InstallationID)
		if err := accessWriteError(err); err != nil {
			return err
		}
		if err := s.ensureInstallationOwner(ctx, tx, binding.InstallationID); err != nil {
			return err
		}
		if err := s.invalidateBindingSubject(ctx, tx, current); err != nil {
			return err
		}
		if current.SubjectKind == binding.SubjectKind &&
			current.PrincipalID == binding.PrincipalID && current.TeamID == binding.TeamID {
			return nil
		}
		return s.invalidateBindingSubject(ctx, tx, binding)
	})
	if err != nil {
		return domain.RoleBinding{}, fmt.Errorf("update role binding: %w", err)
	}
	return s.RoleBinding(ctx, binding.InstallationID, binding.ID)
}

func (s *Store) DeleteRoleBinding(
	ctx context.Context,
	installationID domain.InstallationID,
	id domain.RoleBindingID,
) error {
	err := s.auditTransaction(ctx, func(tx *sql.Tx) error {
		binding, err := scanRoleBinding(tx.QueryRowContext(ctx, s.bind(
			`SELECT `+accessBindingColumns+` FROM role_bindings
			 WHERE installation_id=? AND id=?`,
		), installationID, id))
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, s.bind(
			`DELETE FROM role_bindings WHERE installation_id=? AND id=?`,
		), installationID, id)
		if err != nil {
			return err
		}
		deleted, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if deleted != 1 {
			return domain.ErrAccessResourceNotFound
		}
		if err := s.ensureInstallationOwner(ctx, tx, installationID); err != nil {
			return err
		}
		return s.invalidateBindingSubject(ctx, tx, binding)
	})
	if err != nil {
		return fmt.Errorf("delete role binding: %w", err)
	}
	return nil
}

func (s *Store) EffectiveAccess(
	ctx context.Context,
	installationID domain.InstallationID,
	principalID domain.PrincipalID,
	page domain.AccessPage,
) (domain.EffectiveAccess, error) {
	page, err := page.Normalize()
	if err != nil {
		return domain.EffectiveAccess{}, err
	}
	var present int
	if err := s.db.QueryRowContext(ctx, s.bind(
		`SELECT COUNT(*) FROM principals WHERE installation_id=? AND id=?`,
	), installationID, principalID).Scan(&present); err != nil {
		return domain.EffectiveAccess{}, fmt.Errorf("find effective-access principal: %w", err)
	}
	if present != 1 {
		return domain.EffectiveAccess{}, domain.ErrAccessResourceNotFound
	}
	rows, err := s.db.QueryContext(ctx, s.bind(
		`SELECT rb.id,rd.id,rd.name,rb.scope_type,COALESCE(rb.project_id,''),
		        rd.permissions,TRUE,''
		 FROM role_bindings rb
		 JOIN role_definitions rd ON rd.id=rb.role_definition_id
		   AND rd.installation_id=rb.installation_id
		 WHERE rb.installation_id=? AND rb.principal_id=? AND rb.id>?
		   AND (rb.scope_type='INSTALLATION' OR EXISTS (
		     SELECT 1 FROM projects p
		     WHERE p.id=rb.project_id AND p.installation_id=rb.installation_id
		   ))
		 UNION ALL
		 SELECT rb.id,rd.id,rd.name,rb.scope_type,COALESCE(rb.project_id,''),
		        rd.permissions,FALSE,tm.team_id
		 FROM team_memberships tm
		 JOIN teams t ON t.id=tm.team_id
		 JOIN role_bindings rb ON rb.team_id=tm.team_id
		 JOIN role_definitions rd ON rd.id=rb.role_definition_id
		   AND rd.installation_id=rb.installation_id
		 WHERE tm.principal_id=? AND t.installation_id=? AND rb.id>?
		   AND rb.installation_id=t.installation_id
		   AND (rb.scope_type='INSTALLATION' OR EXISTS (
		     SELECT 1 FROM projects p
		     WHERE p.id=rb.project_id AND p.installation_id=rb.installation_id
		   ))
		 ORDER BY 1 LIMIT ?`,
	), installationID, principalID, page.After,
		principalID, installationID, page.After, page.Limit)
	if err != nil {
		return domain.EffectiveAccess{}, fmt.Errorf("calculate effective access: %w", err)
	}
	defer func() { _ = rows.Close() }()
	access := domain.EffectiveAccess{PrincipalID: principalID}
	for rows.Next() {
		var grant domain.EffectiveGrant
		var scope, encoded string
		if err := rows.Scan(
			&grant.RoleBindingID, &grant.RoleDefinitionID, &grant.RoleName, &scope, &grant.ProjectID,
			&encoded, &grant.Direct, &grant.ViaTeamID,
		); err != nil {
			return domain.EffectiveAccess{}, fmt.Errorf("scan effective grant: %w", err)
		}
		grant.Scope = domain.RoleScope(scope)
		if err := json.Unmarshal([]byte(encoded), &grant.Permissions); err != nil {
			return domain.EffectiveAccess{}, fmt.Errorf("decode effective grant: %w", err)
		}
		access.Grants = append(access.Grants, grant)
	}
	if err := rows.Err(); err != nil {
		return domain.EffectiveAccess{}, fmt.Errorf("read effective access: %w", err)
	}
	return access, nil
}

func (s *Store) validateRoleBinding(
	ctx context.Context, tx *sql.Tx, binding domain.RoleBinding,
) error {
	var roleScope string
	err := tx.QueryRowContext(ctx, s.bind(
		`SELECT scope_type FROM role_definitions
		 WHERE id=? AND installation_id=?`,
	), binding.RoleDefinitionID, binding.InstallationID).Scan(&roleScope)
	if err != nil {
		return accessNotFound(err)
	}
	if domain.RoleScope(roleScope) != binding.Scope {
		return domain.ErrInvalidAccessChange
	}
	if binding.Scope == domain.RoleScopeProject {
		var project int
		if err := tx.QueryRowContext(ctx, s.bind(
			`SELECT COUNT(*) FROM projects WHERE id=? AND installation_id=?`,
		), binding.ProjectID, binding.InstallationID).Scan(&project); err != nil {
			return err
		}
		if project != 1 {
			return domain.ErrAccessResourceNotFound
		}
	}
	var subject int
	if binding.SubjectKind == domain.BindingSubjectPrincipal {
		err = tx.QueryRowContext(ctx, s.bind(
			`SELECT COUNT(*) FROM principals WHERE id=? AND installation_id=?`,
		), binding.PrincipalID, binding.InstallationID).Scan(&subject)
	} else {
		err = tx.QueryRowContext(ctx, s.bind(
			`SELECT COUNT(*) FROM teams WHERE id=? AND installation_id=?`,
		), binding.TeamID, binding.InstallationID).Scan(&subject)
	}
	if err != nil {
		return err
	}
	if subject != 1 {
		return domain.ErrAccessResourceNotFound
	}
	return nil
}

func nullableAccessID(value string) any {
	if value == "" {
		return nil
	}
	return value
}
