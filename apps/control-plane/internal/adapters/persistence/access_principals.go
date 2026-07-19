package persistence

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
)

const accessPrincipalColumns = `id,installation_id,kind,display_name,disabled_at,
	authz_generation,created_at`

func (s *Store) ListPrincipals(
	ctx context.Context, installationID domain.InstallationID, page domain.AccessPage,
) ([]domain.ManagedPrincipal, error) {
	page, err := page.Normalize()
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, s.bind(
		`SELECT `+accessPrincipalColumns+` FROM principals
		 WHERE installation_id=? AND id>? ORDER BY id LIMIT ?`,
	), installationID, page.After, page.Limit)
	if err != nil {
		return nil, fmt.Errorf("list principals: %w", err)
	}
	defer func() { _ = rows.Close() }()
	principals := make([]domain.ManagedPrincipal, 0, page.Limit)
	for rows.Next() {
		principal, err := scanManagedPrincipal(rows)
		if err != nil {
			return nil, fmt.Errorf("scan principal: %w", err)
		}
		principals = append(principals, principal)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read principals: %w", err)
	}
	return principals, nil
}

func (s *Store) Principal(
	ctx context.Context, installationID domain.InstallationID, id domain.PrincipalID,
) (domain.ManagedPrincipal, error) {
	return scanManagedPrincipal(s.db.QueryRowContext(ctx, s.bind(
		`SELECT `+accessPrincipalColumns+` FROM principals
		 WHERE installation_id=? AND id=?`,
	), installationID, id))
}

func (s *Store) CreatePrincipal(
	ctx context.Context, principal domain.ManagedPrincipal,
) (domain.ManagedPrincipal, error) {
	_, err := s.db.ExecContext(ctx, s.bind(
		`INSERT INTO principals(
		   id,installation_id,kind,display_name,disabled_at,authz_generation,created_at
		 ) VALUES(?,?,?,?,?,?,?)`,
	), principal.ID, principal.InstallationID, principal.Kind, principal.DisplayName,
		nil, 1, principal.CreatedAt.Format(accessTimestamp))
	if err := accessWriteError(err); err != nil {
		return domain.ManagedPrincipal{}, fmt.Errorf("create principal: %w", err)
	}
	return s.Principal(ctx, principal.InstallationID, principal.ID)
}

func (s *Store) UpdatePrincipal(
	ctx context.Context, principal domain.ManagedPrincipal,
) (domain.ManagedPrincipal, error) {
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		var disabled any
		if principal.DisabledAt != nil {
			disabled = principal.DisabledAt.UTC().Format(accessTimestamp)
		}
		result, err := tx.ExecContext(ctx, s.bind(
			`UPDATE principals
			 SET display_name=?,disabled_at=?,authz_generation=authz_generation+1
			 WHERE id=? AND installation_id=?`,
		), principal.DisplayName, disabled, principal.ID, principal.InstallationID)
		if err != nil {
			return accessWriteError(err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return domain.ErrAccessResourceNotFound
		}
		return s.ensureInstallationOwner(ctx, tx, principal.InstallationID)
	})
	if err != nil {
		return domain.ManagedPrincipal{}, fmt.Errorf("update principal: %w", err)
	}
	return s.Principal(ctx, principal.InstallationID, principal.ID)
}
