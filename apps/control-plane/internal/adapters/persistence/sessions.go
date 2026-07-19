package persistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
)

func (s *Store) CreateBrowserSession(
	ctx context.Context,
	session domain.BrowserSession,
	rotateDigest string,
) error {
	return s.auditTransaction(ctx, func(tx *sql.Tx) error {
		retentionCutoff := session.CreatedAt.Add(-30 * 24 * time.Hour).Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx, s.bind(
			`DELETE FROM browser_sessions
			 WHERE absolute_expires_at<? AND (revoked_at IS NULL OR revoked_at<?)`,
		), retentionCutoff, retentionCutoff); err != nil {
			return fmt.Errorf("prune expired browser sessions: %w", err)
		}
		var generation int64
		var installationID domain.InstallationID
		err := tx.QueryRowContext(ctx, s.bind(
			`SELECT installation_id,authz_generation FROM principals
			 WHERE id=? AND disabled_at IS NULL`,
		), session.Actor.PrincipalID).Scan(&installationID, &generation)
		if errors.Is(err, sql.ErrNoRows) || installationID != session.Actor.InstallationID {
			return domain.ErrIdentityDisabled
		}
		if err != nil {
			return fmt.Errorf("read session principal: %w", err)
		}
		if session.AuthenticationMethod == "OIDC" {
			var providerInstallation domain.InstallationID
			err = tx.QueryRowContext(ctx, s.bind(
				`SELECT installation_id FROM identity_providers WHERE id=? AND enabled=TRUE`,
			), session.IdentityProviderID).Scan(&providerInstallation)
			if errors.Is(err, sql.ErrNoRows) || providerInstallation != installationID {
				return domain.ErrSessionInvalid
			}
			if err != nil {
				return fmt.Errorf("read session identity provider: %w", err)
			}
		} else if session.AuthenticationMethod != domain.AuthenticationMethodLocal ||
			session.IdentityProviderID != "" ||
			session.RefreshTokenCiphertext != "" ||
			session.AccessTokenCiphertext != "" {
			return domain.ErrSessionInvalid
		}
		session.AuthorizationGeneration = generation
		if rotateDigest != "" {
			if _, err := tx.ExecContext(ctx, s.bind(
				`UPDATE browser_sessions SET revoked_at=?
				 WHERE credential_digest=? AND principal_id=? AND revoked_at IS NULL`,
			), session.CreatedAt.Format(time.RFC3339Nano), rotateDigest, session.Actor.PrincipalID); err != nil {
				return fmt.Errorf("revoke rotated session: %w", err)
			}
		}
		_, err = tx.ExecContext(ctx, s.bind(
			`INSERT INTO browser_sessions(
			 id,credential_digest,csrf_digest,principal_id,identity_provider_id,
			 authentication_method,refresh_token_ciphertext,access_token_ciphertext,
			 authz_generation,idle_expires_at,absolute_expires_at,last_used_at,created_at
			 ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		), session.ID, session.CredentialDigest, session.CSRFDigest, session.Actor.PrincipalID,
			nullableString(session.IdentityProviderID), session.AuthenticationMethod,
			nullableString(session.RefreshTokenCiphertext), nullableString(session.AccessTokenCiphertext),
			generation, session.IdleExpiresAt.Format(time.RFC3339Nano),
			session.AbsoluteExpiresAt.Format(time.RFC3339Nano),
			session.LastUsedAt.Format(time.RFC3339Nano), session.CreatedAt.Format(time.RFC3339Nano))
		if err != nil {
			return fmt.Errorf("insert browser session: %w", err)
		}
		return nil
	})
}

func (s *Store) BrowserSessionByDigest(
	ctx context.Context, digest string,
) (domain.BrowserSession, error) {
	var session domain.BrowserSession
	var idleExpiry, absoluteExpiry, lastUsed, createdAt string
	var revokedAt sql.NullString
	err := s.db.QueryRowContext(ctx, s.bind(
		`SELECT bs.id,bs.credential_digest,bs.csrf_digest,bs.principal_id,p.installation_id,
		 COALESCE(bs.identity_provider_id,''),bs.authentication_method,
		 COALESCE(bs.refresh_token_ciphertext,''),COALESCE(bs.access_token_ciphertext,''),
		 bs.authz_generation,bs.idle_expires_at,bs.absolute_expires_at,bs.last_used_at,
		 bs.revoked_at,bs.created_at
		 FROM browser_sessions bs
		 JOIN principals p ON p.id=bs.principal_id
		   AND p.disabled_at IS NULL AND p.authz_generation=bs.authz_generation
		 WHERE bs.credential_digest=?`,
	), digest).Scan(
		&session.ID, &session.CredentialDigest, &session.CSRFDigest,
		&session.Actor.PrincipalID, &session.Actor.InstallationID,
		&session.IdentityProviderID, &session.AuthenticationMethod,
		&session.RefreshTokenCiphertext, &session.AccessTokenCiphertext,
		&session.AuthorizationGeneration, &idleExpiry, &absoluteExpiry, &lastUsed,
		&revokedAt, &createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.BrowserSession{}, domain.ErrSessionInvalid
	}
	if err != nil {
		return domain.BrowserSession{}, fmt.Errorf("read browser session: %w", err)
	}
	session.IdleExpiresAt = mustParseSessionTime(idleExpiry)
	session.AbsoluteExpiresAt = mustParseSessionTime(absoluteExpiry)
	session.LastUsedAt = mustParseSessionTime(lastUsed)
	session.CreatedAt = mustParseSessionTime(createdAt)
	session.RevokedAt = parseTime(revokedAt)
	session.Actor.AuthenticationMethod = session.AuthenticationMethod
	session.Actor.IdentityProviderID = session.IdentityProviderID
	return session, nil
}

func (s *Store) TouchBrowserSession(
	ctx context.Context, digest string, lastUsedAt, idleExpiresAt time.Time,
) error {
	result, err := s.db.ExecContext(ctx, s.bind(
		`UPDATE browser_sessions SET last_used_at=?,idle_expires_at=?
		 WHERE credential_digest=? AND revoked_at IS NULL
		   AND last_used_at<? AND absolute_expires_at>?`,
	), lastUsedAt.Format(time.RFC3339Nano), idleExpiresAt.Format(time.RFC3339Nano), digest,
		lastUsedAt.Format(time.RFC3339Nano), lastUsedAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("touch browser session: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect browser session touch: %w", err)
	}
	if updated == 0 {
		return domain.ErrSessionInvalid
	}
	return nil
}

func (s *Store) RevokeBrowserSession(
	ctx context.Context, digest string, revokedAt time.Time,
) error {
	return s.auditTransaction(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, s.bind(
			`UPDATE browser_sessions SET revoked_at=COALESCE(revoked_at,?)
			 WHERE credential_digest=?`,
		), revokedAt.Format(time.RFC3339Nano), digest)
		if err != nil {
			return fmt.Errorf("revoke browser session: %w", err)
		}
		return nil
	})
}

func (s *Store) UpdateBrowserSessionTokens(
	ctx context.Context,
	credentialDigest string,
	expectedRefreshCiphertext string,
	refreshCiphertext string,
	accessCiphertext string,
) (bool, error) {
	updated := false
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, s.bind(
			`UPDATE browser_sessions
			 SET refresh_token_ciphertext=?,access_token_ciphertext=?
			 WHERE credential_digest=? AND refresh_token_ciphertext=?
			   AND revoked_at IS NULL`,
		), refreshCiphertext, accessCiphertext, credentialDigest, expectedRefreshCiphertext)
		if err != nil {
			return fmt.Errorf("update browser session tokens: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("inspect browser session token update: %w", err)
		}
		updated = affected == 1
		if !updated {
			return nil
		}
		return s.appendTransactionalAudit(ctx, tx)
	})
	return updated, err
}

func (s *Store) RevokeBrowserSessionIfRefreshToken(
	ctx context.Context,
	credentialDigest string,
	expectedRefreshCiphertext string,
	revokedAt time.Time,
) (bool, error) {
	updated := false
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, s.bind(
			`UPDATE browser_sessions SET revoked_at=?
			 WHERE credential_digest=? AND refresh_token_ciphertext=?
			   AND revoked_at IS NULL`,
		), revokedAt.Format(time.RFC3339Nano), credentialDigest, expectedRefreshCiphertext)
		if err != nil {
			return fmt.Errorf("conditionally revoke browser session: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("inspect conditional session revocation: %w", err)
		}
		updated = affected == 1
		if !updated {
			return nil
		}
		return s.appendTransactionalAudit(ctx, tx)
	})
	return updated, err
}

func (s *Store) CreateOAuthLoginAttempt(
	ctx context.Context, attempt domain.OAuthLoginAttempt,
) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, s.bind(
			`DELETE FROM oauth_login_attempts WHERE expires_at<=?`,
		), attempt.CreatedAt.Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("prune OAuth login attempts: %w", err)
		}
		_, err := tx.ExecContext(ctx, s.bind(
			`INSERT INTO oauth_login_attempts(
			 state_digest,nonce_digest,nonce_ciphertext,pkce_verifier_ciphertext,
			 return_to,expires_at,created_at
			 ) VALUES(?,?,?,?,?,?,?)`,
		), attempt.StateDigest, attempt.NonceDigest, attempt.NonceCiphertext,
			attempt.PKCEVerifierCiphertext, attempt.ReturnTo,
			attempt.ExpiresAt.Format(time.RFC3339Nano), attempt.CreatedAt.Format(time.RFC3339Nano))
		if err != nil {
			return fmt.Errorf("insert OAuth login attempt: %w", err)
		}
		return nil
	})
}

func (s *Store) ConsumeOAuthLoginAttempt(
	ctx context.Context, stateDigest string, consumedAt time.Time,
) (domain.OAuthLoginAttempt, error) {
	var attempt domain.OAuthLoginAttempt
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		var expiresAt, createdAt string
		var consumed sql.NullString
		err := tx.QueryRowContext(ctx, s.bind(
			`SELECT state_digest,nonce_digest,nonce_ciphertext,pkce_verifier_ciphertext,
			 return_to,expires_at,consumed_at,created_at
			 FROM oauth_login_attempts WHERE state_digest=?`,
		), stateDigest).Scan(
			&attempt.StateDigest, &attempt.NonceDigest, &attempt.NonceCiphertext,
			&attempt.PKCEVerifierCiphertext, &attempt.ReturnTo,
			&expiresAt, &consumed, &createdAt,
		)
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ErrSessionInvalid
		}
		if err != nil {
			return fmt.Errorf("read OAuth login attempt: %w", err)
		}
		attempt.ExpiresAt = mustParseSessionTime(expiresAt)
		attempt.CreatedAt = mustParseSessionTime(createdAt)
		attempt.ConsumedAt = parseTime(consumed)
		if attempt.ConsumedAt != nil || !consumedAt.Before(attempt.ExpiresAt) {
			return domain.ErrSessionInvalid
		}
		result, err := tx.ExecContext(ctx, s.bind(
			`UPDATE oauth_login_attempts SET consumed_at=?
			 WHERE state_digest=? AND consumed_at IS NULL`,
		), consumedAt.Format(time.RFC3339Nano), stateDigest)
		if err != nil {
			return fmt.Errorf("consume OAuth login attempt: %w", err)
		}
		updated, err := result.RowsAffected()
		if err != nil || updated != 1 {
			return domain.ErrSessionInvalid
		}
		attempt.ConsumedAt = &consumedAt
		return nil
	})
	return attempt, err
}

func mustParseSessionTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
