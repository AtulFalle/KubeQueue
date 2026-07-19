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

func (s *Store) SchemaDiagnostics(ctx context.Context) (domain.SchemaDiagnostics, error) {
	if err := s.VerifySchema(ctx); err != nil {
		return domain.SchemaDiagnostics{}, err
	}
	migrations, err := loadMigrations()
	if err != nil {
		return domain.SchemaDiagnostics{}, fmt.Errorf("load schema versions: %w", err)
	}
	latest := ""
	if len(migrations) > 0 {
		latest = schemaVersion(migrations[len(migrations)-1].version)
	}
	var current string
	if err := s.db.QueryRowContext(
		ctx, `SELECT COALESCE(MAX(version), '') FROM schema_migrations WHERE dirty=FALSE`,
	).Scan(&current); err != nil {
		return domain.SchemaDiagnostics{}, fmt.Errorf("read current schema version: %w", err)
	}
	return domain.SchemaDiagnostics{
		Healthy: true, Current: schemaVersion(current), Latest: latest,
	}, nil
}

func (s *Store) SupportLeadershipDiagnostics(
	ctx context.Context,
) (domain.LeadershipDiagnostics, error) {
	lease, err := s.LeadershipLease(ctx, "reconciler")
	if errors.Is(err, sql.ErrNoRows) {
		return domain.LeadershipDiagnostics{}, nil
	}
	if err != nil {
		return domain.LeadershipDiagnostics{}, err
	}
	return domain.LeadershipDiagnostics{
		Held: lease.Active(time.Now().UTC()), Generation: lease.Generation,
	}, nil
}

func (s *Store) RecentSupportErrorClasses(
	ctx context.Context, limit int,
) ([]domain.SupportErrorClass, error) {
	if limit <= 0 || limit > domain.MaxSupportErrorClasses {
		limit = domain.MaxSupportErrorClasses
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`
		SELECT last_error_code, COUNT(*), MAX(updated_at)
		FROM jobs
		WHERE last_error_code<>''
		GROUP BY last_error_code
		ORDER BY MAX(updated_at) DESC, last_error_code ASC
		LIMIT ?`), limit)
	if err != nil {
		return nil, fmt.Errorf("read support error classes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]domain.SupportErrorClass, 0, limit)
	for rows.Next() {
		var item domain.SupportErrorClass
		var count int64
		var lastSeen string
		if err := rows.Scan(&item.Class, &count, &lastSeen); err != nil {
			return nil, fmt.Errorf("scan support error class: %w", err)
		}
		item.Class = boundedDiagnosticClass(item.Class)
		if item.Class == "" || count <= 0 {
			continue
		}
		item.Count = uint64(count)
		if parsed, err := time.Parse(time.RFC3339Nano, lastSeen); err == nil {
			parsed = parsed.UTC()
			item.LastSeenAt = &parsed
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate support error classes: %w", err)
	}
	return result, nil
}

func schemaVersion(value string) string {
	value = strings.TrimPrefix(value, "migrations/")
	value = strings.TrimSuffix(value, ".sql")
	if len(value) > 128 {
		return value[:128]
	}
	return value
}

func boundedDiagnosticClass(value string) string {
	value = strings.Join(strings.Fields(value), "_")
	if len(value) > 64 {
		return value[:64]
	}
	return value
}
