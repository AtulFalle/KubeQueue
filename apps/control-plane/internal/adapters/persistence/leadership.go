package persistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/leadership"
)

// AcquireLeadership atomically acquires or renews a named lease. Generation
// advances on every acquisition after expiry, including by the same holder.
func (s *Store) AcquireLeadership(
	ctx context.Context, name, holder string, ttl time.Duration,
) (leadership.Lease, error) {
	if name == "" {
		return leadership.Lease{}, errors.New("acquire leadership: lease name is required")
	}
	if holder == "" {
		return leadership.Lease{}, leadership.ErrInvalidHolder
	}
	if ttl <= 0 {
		return leadership.Lease{}, leadership.ErrInvalidTTL
	}
	now := time.Now().UTC()
	nowText := now.Format(time.RFC3339Nano)
	expiresText := now.Add(ttl).Format(time.RFC3339Nano)
	row := s.db.QueryRowContext(ctx, s.bind(`
		INSERT INTO leadership_leases(name,holder,generation,expires_at,updated_at)
		VALUES(?,?,1,?,?)
		ON CONFLICT(name) DO UPDATE SET
			holder=excluded.holder,
			generation=CASE
				WHEN leadership_leases.holder=excluded.holder
					AND leadership_leases.expires_at>? THEN leadership_leases.generation
				ELSE leadership_leases.generation+1
			END,
			expires_at=excluded.expires_at,
			updated_at=excluded.updated_at
		WHERE leadership_leases.holder=excluded.holder
			OR (leadership_leases.expires_at<=? AND leadership_leases.generation<?)
		RETURNING holder,generation,expires_at
	`), name, holder, expiresText, nowText, nowText, nowText, int64(math.MaxInt64))
	lease, err := scanLeadershipLease(row)
	if err == nil {
		return lease, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return leadership.Lease{}, fmt.Errorf("acquire leadership lease %q: %w", name, err)
	}
	current, readErr := s.LeadershipLease(ctx, name)
	if readErr != nil {
		return leadership.Lease{}, fmt.Errorf("read contended leadership lease %q: %w", name, readErr)
	}
	if current.Generation == uint64(math.MaxInt64) && !current.Active(now) {
		return current, leadership.ErrGenerationExhausted
	}
	return current, leadership.ErrLeaseHeld
}

func (s *Store) LeadershipLease(
	ctx context.Context, name string,
) (leadership.Lease, error) {
	lease, err := scanLeadershipLease(s.db.QueryRowContext(
		ctx,
		s.bind(`SELECT holder,generation,expires_at FROM leadership_leases WHERE name=?`),
		name,
	))
	if err != nil {
		return leadership.Lease{}, fmt.Errorf("read leadership lease %q: %w", name, err)
	}
	return lease, nil
}

type leadershipLeaseScanner interface {
	Scan(...any) error
}

func scanLeadershipLease(row leadershipLeaseScanner) (leadership.Lease, error) {
	var holder, expiresText string
	var generation int64
	if err := row.Scan(&holder, &generation, &expiresText); err != nil {
		return leadership.Lease{}, err
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, expiresText)
	if err != nil {
		return leadership.Lease{}, fmt.Errorf("parse leadership expiry: %w", err)
	}
	if generation <= 0 {
		return leadership.Lease{}, errors.New("leadership generation is invalid")
	}
	return leadership.Lease{
		Holder: holder, Generation: uint64(generation), ExpiresAt: expiresAt,
	}, nil
}
