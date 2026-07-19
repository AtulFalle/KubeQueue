package persistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
)

const managedNamespaceBindingColumns = `
	id,installation_id,project_id,namespace,active,created_at,updated_at`

func (s *Store) ListNamespaceBindings(
	ctx context.Context,
	installationID domain.InstallationID,
	projectID domain.ProjectID,
	page domain.AccessPage,
) ([]domain.NamespaceBinding, error) {
	page, err := page.Normalize()
	if err != nil {
		return nil, err
	}
	if _, err := s.Project(ctx, installationID, projectID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, s.bind(
		`SELECT `+managedNamespaceBindingColumns+` FROM namespace_bindings
		 WHERE installation_id=? AND project_id=? AND active=? AND namespace>?
		 ORDER BY namespace LIMIT ?`,
	), installationID, projectID, true, page.After, page.Limit)
	if err != nil {
		return nil, fmt.Errorf("list namespace bindings: %w", err)
	}
	defer func() { _ = rows.Close() }()
	bindings := make([]domain.NamespaceBinding, 0, page.Limit)
	for rows.Next() {
		binding, err := scanManagedNamespaceBinding(rows)
		if err != nil {
			return nil, err
		}
		bindings = append(bindings, binding)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read namespace bindings: %w", err)
	}
	return bindings, nil
}

func (s *Store) ManagedNamespaceBinding(
	ctx context.Context,
	installationID domain.InstallationID,
	namespace string,
) (domain.NamespaceBinding, error) {
	binding, err := scanManagedNamespaceBinding(s.db.QueryRowContext(ctx, s.bind(
		`SELECT `+managedNamespaceBindingColumns+` FROM namespace_bindings
		 WHERE installation_id=? AND namespace=? AND active=?`,
	), installationID, namespace, true))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.NamespaceBinding{}, domain.ErrAccessResourceNotFound
	}
	return binding, err
}

func (s *Store) CreateNamespaceBinding(
	ctx context.Context,
	installationID domain.InstallationID,
	binding domain.NamespaceBinding,
	now time.Time,
) (domain.NamespaceBinding, error) {
	now = now.UTC()
	err := s.auditTransaction(ctx, func(tx *sql.Tx) error {
		if err := projectExistsTx(ctx, s, tx, installationID, binding.ProjectID); err != nil {
			return err
		}
		var id domain.NamespaceBindingID
		var active bool
		err := tx.QueryRowContext(ctx, s.bind(
			`SELECT id,active FROM namespace_bindings
			 WHERE installation_id=? AND namespace=?`,
		), installationID, binding.Namespace).Scan(&id, &active)
		switch {
		case err == nil && active:
			return domain.ErrAccessConflict
		case err == nil:
			_, err = tx.ExecContext(ctx, s.bind(
				`UPDATE namespace_bindings SET project_id=?,active=?,updated_at=?
				 WHERE installation_id=? AND namespace=? AND active=?`,
			), binding.ProjectID, true, now.Format(accessTimestamp),
				installationID, binding.Namespace, false)
			return err
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		_, err = tx.ExecContext(ctx, s.bind(
			`INSERT INTO namespace_bindings(
			 id,installation_id,project_id,namespace,active,created_at,updated_at
			 ) VALUES(?,?,?,?,?,?,?)`,
		), binding.ID, installationID, binding.ProjectID, binding.Namespace, true,
			now.Format(accessTimestamp), now.Format(accessTimestamp))
		return accessWriteError(err)
	})
	if err != nil {
		return domain.NamespaceBinding{}, fmt.Errorf("create namespace binding: %w", err)
	}
	return s.ManagedNamespaceBinding(ctx, installationID, binding.Namespace)
}

func (s *Store) ReassignNamespaceBinding(
	ctx context.Context,
	installationID domain.InstallationID,
	namespace string,
	projectID domain.ProjectID,
	now time.Time,
) (domain.NamespaceBinding, error) {
	err := s.auditTransaction(ctx, func(tx *sql.Tx) error {
		if err := projectExistsTx(ctx, s, tx, installationID, projectID); err != nil {
			return err
		}
		var current domain.ProjectID
		err := tx.QueryRowContext(ctx, s.bind(
			`SELECT project_id FROM namespace_bindings
			 WHERE installation_id=? AND namespace=? AND active=?`,
		), installationID, namespace, true).Scan(&current)
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ErrAccessResourceNotFound
		}
		if err != nil || current == projectID {
			return err
		}
		if err := noActiveManagedWorkloadsTx(ctx, s, tx, installationID, namespace); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, s.bind(
			`UPDATE namespace_bindings SET project_id=?,updated_at=?
			 WHERE installation_id=? AND namespace=? AND active=?`,
		), projectID, now.UTC().Format(accessTimestamp), installationID, namespace, true)
		return err
	})
	if err != nil {
		return domain.NamespaceBinding{}, fmt.Errorf("reassign namespace binding: %w", err)
	}
	return s.ManagedNamespaceBinding(ctx, installationID, namespace)
}

func (s *Store) RemoveNamespaceBinding(
	ctx context.Context,
	installationID domain.InstallationID,
	projectID domain.ProjectID,
	namespace string,
	now time.Time,
) error {
	err := s.auditTransaction(ctx, func(tx *sql.Tx) error {
		if err := noActiveManagedWorkloadsTx(ctx, s, tx, installationID, namespace); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, s.bind(
			`UPDATE namespace_bindings SET active=?,updated_at=?
			 WHERE installation_id=? AND project_id=? AND namespace=? AND active=?`,
		), false, now.UTC().Format(accessTimestamp),
			installationID, projectID, namespace, true)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return domain.ErrAccessResourceNotFound
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("remove namespace binding: %w", err)
	}
	return nil
}

func noActiveManagedWorkloadsTx(
	ctx context.Context,
	store *Store,
	tx *sql.Tx,
	installationID domain.InstallationID,
	namespace string,
) error {
	var count int
	err := tx.QueryRowContext(ctx, store.bind(
		`SELECT COUNT(*) FROM jobs j
		 JOIN projects p ON p.id=j.project_id
		 WHERE p.installation_id=? AND j.namespace=?
		   AND j.management_mode='MANAGED' AND j.archived_at IS NULL
		   AND j.desired_state<>'CANCELLED'
		   AND j.observed_state NOT IN ('COMPLETED','FAILED')`,
	), installationID, namespace).Scan(&count)
	if err != nil {
		return err
	}
	if count != 0 {
		return domain.ErrAccessConflict
	}
	return nil
}

func projectExistsTx(
	ctx context.Context,
	store *Store,
	tx *sql.Tx,
	installationID domain.InstallationID,
	projectID domain.ProjectID,
) error {
	var count int
	if err := tx.QueryRowContext(ctx, store.bind(
		`SELECT COUNT(*) FROM projects WHERE installation_id=? AND id=?`,
	), installationID, projectID).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return domain.ErrAccessResourceNotFound
	}
	return nil
}

type namespaceBindingScanner interface {
	Scan(...any) error
}

func scanManagedNamespaceBinding(scanner namespaceBindingScanner) (domain.NamespaceBinding, error) {
	var binding domain.NamespaceBinding
	var active bool
	var createdAt, updatedAt string
	err := scanner.Scan(
		&binding.ID, &binding.InstallationID, &binding.ProjectID, &binding.Namespace,
		&active, &createdAt, &updatedAt,
	)
	if err != nil {
		return domain.NamespaceBinding{}, err
	}
	binding.Desired = active
	binding.CreatedAt = accessTime(createdAt)
	binding.UpdatedAt = accessTime(updatedAt)
	if binding.UpdatedAt.IsZero() {
		binding.UpdatedAt = binding.CreatedAt
	}
	return binding, nil
}
