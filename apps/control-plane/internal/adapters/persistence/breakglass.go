package persistence

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain/breakglass"
)

func (s *Store) ConfigureBreakGlass(
	ctx context.Context,
	credentials []breakglass.Credential,
	now time.Time,
) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, s.bind(
			`UPDATE break_glass_credentials SET revoked_at=COALESCE(revoked_at,?)`,
		), formatCredentialTime(now)); err != nil {
			return fmt.Errorf("disable prior break-glass credentials: %w", err)
		}
		for _, credential := range credentials {
			_, err := tx.ExecContext(ctx, s.bind(
				`INSERT INTO break_glass_credentials(
				 slot,safe_prefix,keyed_digest,expires_at,overlap_expires_at,
				 revoked_at,last_used_at,configured_at
				 ) VALUES(?,?,?,?,?,NULL,NULL,?)
				 ON CONFLICT(slot) DO UPDATE SET
				 safe_prefix=excluded.safe_prefix,keyed_digest=excluded.keyed_digest,
				 expires_at=excluded.expires_at,
				 overlap_expires_at=excluded.overlap_expires_at,
				 revoked_at=NULL,configured_at=excluded.configured_at`,
			), credential.Slot, credential.Prefix,
				base64.RawURLEncoding.EncodeToString(credential.Digest.Bytes()),
				formatCredentialTime(credential.ExpiresAt),
				nullableCredentialTimePointer(credential.OverlapExpires),
				formatCredentialTime(now))
			if err != nil {
				return fmt.Errorf("configure break-glass credential: %w", err)
			}
		}
		return nil
	})
}

func (s *Store) BreakGlassCredential(
	ctx context.Context,
	prefix string,
) (breakglass.Credential, error) {
	var credential breakglass.Credential
	var digest, expiresAt string
	var overlap, revoked, lastUsed sql.NullString
	err := s.db.QueryRowContext(ctx, s.bind(
		`SELECT slot,safe_prefix,keyed_digest,expires_at,overlap_expires_at,
		        revoked_at,last_used_at
		 FROM break_glass_credentials WHERE safe_prefix=?`,
	), prefix).Scan(
		&credential.Slot, &credential.Prefix, &digest, &expiresAt,
		&overlap, &revoked, &lastUsed,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return breakglass.Credential{}, breakglass.ErrInvalidCredential
	}
	if err != nil {
		return breakglass.Credential{}, fmt.Errorf("read break-glass credential: %w", err)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(digest)
	if err != nil || len(decoded) != len(credential.Digest) {
		return breakglass.Credential{}, errors.New("stored break-glass digest is invalid")
	}
	copy(credential.Digest[:], decoded)
	credential.ExpiresAt, err = parseCredentialTime(expiresAt)
	if err != nil {
		return breakglass.Credential{}, err
	}
	credential.OverlapExpires, err = parseOptionalCredentialTimePointer(overlap)
	if err != nil {
		return breakglass.Credential{}, err
	}
	credential.RevokedAt, err = parseOptionalCredentialTimePointer(revoked)
	if err != nil {
		return breakglass.Credential{}, err
	}
	credential.LastUsedAt, err = parseOptionalCredentialTime(lastUsed)
	return credential, err
}

func (s *Store) RecordBreakGlassAttempt(
	ctx context.Context,
	success bool,
	now time.Time,
) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		var window, blocked sql.NullString
		var failures int
		err := tx.QueryRowContext(ctx, s.bind(
			`SELECT window_started_at,failures,blocked_until
			 FROM break_glass_rate_limit WHERE id=1`,
		)).Scan(&window, &failures, &blocked)
		if err != nil {
			return fmt.Errorf("read break-glass rate limit: %w", err)
		}
		blockedUntil, err := parseOptionalCredentialTime(blocked)
		if err != nil {
			return err
		}
		if !blockedUntil.IsZero() && now.Before(blockedUntil) {
			return breakglass.ErrRateLimited
		}
		if success {
			if _, err := tx.ExecContext(ctx, s.bind(
				`UPDATE break_glass_rate_limit
				 SET window_started_at=NULL,failures=0,blocked_until=NULL WHERE id=1`,
			)); err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, s.bind(
				`UPDATE break_glass_credentials SET last_used_at=?
				 WHERE revoked_at IS NULL AND expires_at>?`,
			), formatCredentialTime(now), formatCredentialTime(now))
			return err
		}
		windowStarted, err := parseOptionalCredentialTime(window)
		if err != nil {
			return err
		}
		if windowStarted.IsZero() || !now.Before(windowStarted.Add(breakglass.FailureWindow)) {
			windowStarted, failures = now, 0
		}
		failures++
		var nextBlocked any
		if failures >= breakglass.DurableFailureLimit {
			nextBlocked = formatCredentialTime(now.Add(breakglass.BlockDuration))
		}
		_, err = tx.ExecContext(ctx, s.bind(
			`UPDATE break_glass_rate_limit
			 SET window_started_at=?,failures=?,blocked_until=? WHERE id=1`,
		), formatCredentialTime(windowStarted), failures, nextBlocked)
		return err
	})
}
