package persistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
)

const developmentLocalAdminPrincipalID = domain.PrincipalID("local_development_admin")

func (s *Store) EnsureDevelopmentLocalAdmin(ctx context.Context, passwordHash string) error {
	if strings.TrimSpace(passwordHash) == "" {
		return errors.New("development local-admin password hash is required")
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		var existingPrincipalID domain.PrincipalID
		err := tx.QueryRowContext(ctx, s.bind(
			`SELECT principal_id FROM local_accounts WHERE normalized_username=?`,
		), domain.NormalizeLocalUsername("admin")).Scan(&existingPrincipalID)
		switch {
		case err == nil && existingPrincipalID != developmentLocalAdminPrincipalID:
			return errors.New("development local-admin seed cannot replace an existing admin account")
		case err != nil && !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("check development local-admin account: %w", err)
		}
		if _, err := tx.ExecContext(ctx, s.bind(
			`INSERT INTO principals(
			 id,installation_id,kind,display_name,disabled_at,created_at
			 ) VALUES(?,'default','HUMAN','admin',NULL,?)
			 ON CONFLICT(id) DO UPDATE SET
			 display_name='admin',disabled_at=NULL`,
		), developmentLocalAdminPrincipalID, now); err != nil {
			return fmt.Errorf("create development local-admin principal: %w", err)
		}
		if _, err := tx.ExecContext(ctx, s.bind(
			`INSERT INTO local_accounts(
			 principal_id,normalized_username,username,password_hash,created_at,updated_at
			 ) VALUES(?,'admin','admin',?,?,?)
			 ON CONFLICT(principal_id) DO UPDATE SET
			 username='admin',normalized_username='admin',updated_at=excluded.updated_at`,
		), developmentLocalAdminPrincipalID, passwordHash, now, now); err != nil {
			return fmt.Errorf("create development local-admin credential: %w", err)
		}
		if _, err := tx.ExecContext(ctx, s.bind(
			`INSERT INTO role_bindings(
			 id,installation_id,role_definition_id,scope_type,principal_id,created_at
			 ) VALUES(
			 'development_seed_owner','default','installation_owner','INSTALLATION',?,?
			 ) ON CONFLICT(id) DO UPDATE SET principal_id=excluded.principal_id`,
		), developmentLocalAdminPrincipalID, now); err != nil {
			return fmt.Errorf("grant development local-admin ownership: %w", err)
		}
		return nil
	})
}

func (s *Store) LocalAccountByUsername(
	ctx context.Context, normalizedUsername string,
) (domain.LocalAccount, error) {
	return scanLocalAccount(s.db.QueryRowContext(ctx, s.bind(
		`SELECT la.principal_id,p.installation_id,la.username,la.password_hash,
		        p.disabled_at,la.created_at,la.updated_at
		 FROM local_accounts la JOIN principals p ON p.id=la.principal_id
		 WHERE la.normalized_username=?`,
	), normalizedUsername))
}

func (s *Store) LocalAccountByPrincipal(
	ctx context.Context, principalID domain.PrincipalID,
) (domain.LocalAccount, error) {
	return scanLocalAccount(s.db.QueryRowContext(ctx, s.bind(
		`SELECT la.principal_id,p.installation_id,la.username,la.password_hash,
		        p.disabled_at,la.created_at,la.updated_at
		 FROM local_accounts la JOIN principals p ON p.id=la.principal_id
		 WHERE la.principal_id=?`,
	), principalID))
}

type localAccountScanner interface {
	Scan(...any) error
}

func scanLocalAccount(row localAccountScanner) (domain.LocalAccount, error) {
	var account domain.LocalAccount
	var disabled sql.NullString
	var createdAt, updatedAt string
	err := row.Scan(
		&account.PrincipalID, &account.InstallationID, &account.Username,
		&account.PasswordHash, &disabled, &createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.LocalAccount{}, domain.ErrLocalAccountNotFound
	}
	if err != nil {
		return domain.LocalAccount{}, fmt.Errorf("read local account: %w", err)
	}
	account.Disabled = disabled.Valid
	account.CreatedAt = mustParseSessionTime(createdAt)
	account.UpdatedAt = mustParseSessionTime(updatedAt)
	return account, nil
}

func (s *Store) LocalLoginAllowed(
	ctx context.Context, key string, now time.Time,
) (bool, error) {
	var lockedUntil sql.NullString
	err := s.db.QueryRowContext(ctx, s.bind(
		`SELECT locked_until FROM local_login_throttles WHERE throttle_key=?`,
	), key).Scan(&lockedUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("read local login throttle: %w", err)
	}
	if !lockedUntil.Valid {
		return true, nil
	}
	locked := mustParseSessionTime(lockedUntil.String)
	return !now.Before(locked), nil
}

func (s *Store) RecordLocalLoginFailure(
	ctx context.Context,
	key string,
	now time.Time,
	window time.Duration,
	maxFailures int,
	lockout time.Duration,
) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		var count int
		var windowStarted string
		err := tx.QueryRowContext(ctx, s.bind(
			`SELECT failure_count,window_started_at
			 FROM local_login_throttles WHERE throttle_key=?`,
		), key).Scan(&count, &windowStarted)
		if errors.Is(err, sql.ErrNoRows) ||
			(err == nil && !now.Before(mustParseSessionTime(windowStarted).Add(window))) {
			count = 0
			windowStarted = now.Format(time.RFC3339Nano)
		} else if err != nil {
			return fmt.Errorf("read local login failures: %w", err)
		}
		count++
		var lockedUntil any
		if count >= maxFailures {
			lockedUntil = now.Add(lockout).Format(time.RFC3339Nano)
		}
		_, err = tx.ExecContext(ctx, s.bind(
			`INSERT INTO local_login_throttles(
			 throttle_key,failure_count,window_started_at,locked_until,updated_at
			 ) VALUES(?,?,?,?,?)
			 ON CONFLICT(throttle_key) DO UPDATE SET
			 failure_count=excluded.failure_count,
			 window_started_at=excluded.window_started_at,
			 locked_until=excluded.locked_until,
			 updated_at=excluded.updated_at`,
		), key, count, windowStarted, lockedUntil, now.Format(time.RFC3339Nano))
		if err != nil {
			return fmt.Errorf("persist local login failure: %w", err)
		}
		return nil
	})
}

func (s *Store) ClearLocalLoginFailures(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, s.bind(
		`DELETE FROM local_login_throttles WHERE throttle_key=?`,
	), key)
	if err != nil {
		return fmt.Errorf("clear local login failures: %w", err)
	}
	return nil
}

func (s *Store) ChangeLocalPassword(
	ctx context.Context,
	principalID domain.PrincipalID,
	expectedHash string,
	newHash string,
	changedAt time.Time,
) error {
	return s.auditTransaction(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, s.bind(
			`UPDATE local_accounts SET password_hash=?,updated_at=?
			 WHERE principal_id=? AND password_hash=?`,
		), newHash, changedAt.Format(time.RFC3339Nano), principalID, expectedHash)
		if err != nil {
			return fmt.Errorf("change local password: %w", err)
		}
		updated, err := result.RowsAffected()
		if err != nil || updated != 1 {
			return domain.ErrLocalPasswordConflict
		}
		if _, err := tx.ExecContext(ctx, s.bind(
			`UPDATE browser_sessions SET revoked_at=COALESCE(revoked_at,?)
			 WHERE principal_id=?`,
		), changedAt.Format(time.RFC3339Nano), principalID); err != nil {
			return fmt.Errorf("revoke sessions after password change: %w", err)
		}
		return nil
	})
}

func (s *Store) ResetLocalPassword(
	ctx context.Context,
	principalID domain.PrincipalID,
	newHash string,
	changedAt time.Time,
) error {
	return s.auditTransaction(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, s.bind(
			`UPDATE local_accounts SET password_hash=?,updated_at=?
			 WHERE principal_id=? AND EXISTS (
			   SELECT 1 FROM principals p
			   WHERE p.id=local_accounts.principal_id
			     AND p.kind='HUMAN' AND p.disabled_at IS NULL
			 )`,
		), newHash, changedAt.Format(time.RFC3339Nano), principalID)
		if err != nil {
			return fmt.Errorf("reset local password: %w", err)
		}
		updated, err := result.RowsAffected()
		if err != nil || updated != 1 {
			return domain.ErrLocalAccountNotFound
		}
		if _, err := tx.ExecContext(ctx, s.bind(
			`UPDATE browser_sessions SET revoked_at=COALESCE(revoked_at,?)
			 WHERE principal_id=?`,
		), changedAt.Format(time.RFC3339Nano), principalID); err != nil {
			return fmt.Errorf("revoke sessions after password reset: %w", err)
		}
		return nil
	})
}

func (s *Store) IsInstallationOwner(ctx context.Context, actor domain.Actor) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, s.bind(
		`SELECT COUNT(*) FROM role_bindings rb
		 JOIN role_definitions rd ON rd.id=rb.role_definition_id
		 JOIN principals p ON p.id=rb.principal_id
		 WHERE rb.principal_id=? AND rb.installation_id=?
		   AND rb.scope_type='INSTALLATION'
		   AND rd.id='installation_owner' AND rd.built_in=TRUE
		   AND p.kind='HUMAN' AND p.disabled_at IS NULL`,
	), actor.PrincipalID, actor.InstallationID).Scan(&count)
	return count == 1, err
}

func (s *Store) LocalLoginEnabled(ctx context.Context) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM local_accounts la
		JOIN principals p ON p.id=la.principal_id
		WHERE p.kind='HUMAN' AND p.disabled_at IS NULL`).Scan(&count)
	return count > 0, err
}
