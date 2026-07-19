package persistence

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// ErrSchemaIncompatible reports migration history that this binary cannot safely use.
var ErrSchemaIncompatible = errors.New("schema incompatible")

type migration struct {
	version  string
	checksum string
	source   string
}

type migrationState struct {
	checksum string
	dirty    bool
}

// Migrate applies all embedded migrations to the configured database.
func Migrate(ctx context.Context, databaseURL string) error {
	store, err := open(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	return store.migrate(ctx)
}

// VerifySchema checks migration history without changing the database.
func (s *Store) VerifySchema(ctx context.Context) error {
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	connection, err := s.db.Conn(ctx)
	if err != nil {
		return incompatible("migration history is unavailable", err)
	}
	defer func() { _ = connection.Close() }()
	states, err := s.readMigrationState(ctx, connection)
	if err != nil {
		return incompatible("migration history is unavailable", err)
	}
	return validateMigrationState(migrations, states, false)
}

func (s *Store) migrate(ctx context.Context) error {
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	connection, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("open migration connection: %w", err)
	}
	defer func() { _ = connection.Close() }()

	if s.postgres {
		if _, err := connection.ExecContext(
			ctx, `SELECT pg_advisory_lock(hashtext('kubequeue_migrations'))`,
		); err != nil {
			return fmt.Errorf("lock migrations: %w", err)
		}
		defer func() {
			_, _ = connection.ExecContext(
				context.Background(), `SELECT pg_advisory_unlock(hashtext('kubequeue_migrations'))`,
			)
		}()
	}
	if err := s.prepareMigrationHistory(ctx, connection, migrations); err != nil {
		return err
	}
	states, err := s.readMigrationState(ctx, connection)
	if err != nil {
		return fmt.Errorf("read migration history: %w", err)
	}
	if err := validateMigrationState(migrations, states, true); err != nil {
		return err
	}
	for _, item := range migrations {
		if _, applied := states[item.version]; applied {
			continue
		}
		if err := s.applyMigration(ctx, connection, item); err != nil {
			return err
		}
	}
	return nil
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		return nil, fmt.Errorf("list migrations: %w", err)
	}
	sort.Strings(entries)
	result := make([]migration, 0, len(entries))
	for _, name := range entries {
		source, err := migrationFiles.ReadFile(name)
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", name, err)
		}
		sum := sha256.Sum256(source)
		result = append(result, migration{
			version: name, checksum: fmt.Sprintf("%x", sum), source: string(source),
		})
	}
	return result, nil
}

func (s *Store) prepareMigrationHistory(
	ctx context.Context,
	connection *sql.Conn,
	migrations []migration,
) error {
	if _, err := connection.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		checksum TEXT NOT NULL,
		dirty BOOLEAN NOT NULL DEFAULT FALSE,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("create migration history: %w", err)
	}
	columns, err := s.migrationHistoryColumns(ctx, connection)
	if err != nil {
		return fmt.Errorf("inspect migration history: %w", err)
	}
	if _, exists := columns["checksum"]; !exists {
		if _, err := connection.ExecContext(
			ctx, `ALTER TABLE schema_migrations ADD COLUMN checksum TEXT NOT NULL DEFAULT ''`,
		); err != nil {
			return fmt.Errorf("add migration checksums: %w", err)
		}
	}
	if _, exists := columns["dirty"]; !exists {
		if _, err := connection.ExecContext(
			ctx, `ALTER TABLE schema_migrations ADD COLUMN dirty BOOLEAN NOT NULL DEFAULT FALSE`,
		); err != nil {
			return fmt.Errorf("add migration dirty state: %w", err)
		}
	}
	return s.baselineMigrationChecksums(ctx, connection, migrations)
}

func (s *Store) migrationHistoryColumns(
	ctx context.Context,
	connection *sql.Conn,
) (map[string]struct{}, error) {
	query := `PRAGMA table_info(schema_migrations)`
	if s.postgres {
		query = `SELECT column_name FROM information_schema.columns
			WHERE table_schema=current_schema() AND table_name='schema_migrations'`
	}
	rows, err := connection.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	columns := make(map[string]struct{})
	for rows.Next() {
		var name string
		if s.postgres {
			if err := rows.Scan(&name); err != nil {
				return nil, err
			}
		} else {
			var sequence, notNull, primaryKey int
			var dataType string
			var defaultValue sql.NullString
			if err := rows.Scan(
				&sequence, &name, &dataType, &notNull, &defaultValue, &primaryKey,
			); err != nil {
				return nil, err
			}
		}
		columns[name] = struct{}{}
	}
	return columns, rows.Err()
}

func (s *Store) baselineMigrationChecksums(
	ctx context.Context,
	connection *sql.Conn,
	migrations []migration,
) error {
	known := make(map[string]string, len(migrations))
	for _, item := range migrations {
		known[item.version] = item.checksum
	}
	rows, err := connection.QueryContext(
		ctx, `SELECT version, checksum, dirty FROM schema_migrations`,
	)
	if err != nil {
		return fmt.Errorf("read migration baseline: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var baselines []migration
	for rows.Next() {
		var version, checksum string
		var dirty bool
		if err := rows.Scan(&version, &checksum, &dirty); err != nil {
			return fmt.Errorf("scan migration baseline: %w", err)
		}
		if dirty {
			return incompatible(fmt.Sprintf("migration %s is dirty", version), nil)
		}
		if checksum != "" {
			continue
		}
		expected, exists := known[version]
		if !exists {
			return incompatible(
				fmt.Sprintf("legacy migration %s has no known checksum", version), nil,
			)
		}
		baselines = append(baselines, migration{version: version, checksum: expected})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read migration baseline: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close migration baseline: %w", err)
	}
	for _, item := range baselines {
		if _, err := connection.ExecContext(ctx, s.bind(
			`UPDATE schema_migrations SET checksum=? WHERE version=? AND checksum=''`,
		), item.checksum, item.version); err != nil {
			return fmt.Errorf("baseline migration %s: %w", item.version, err)
		}
	}
	return nil
}

func (s *Store) readMigrationState(
	ctx context.Context,
	connection *sql.Conn,
) (map[string]migrationState, error) {
	rows, err := connection.QueryContext(
		ctx, `SELECT version, checksum, dirty FROM schema_migrations ORDER BY version`,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	states := make(map[string]migrationState)
	for rows.Next() {
		var version, checksum string
		var dirty bool
		if err := rows.Scan(&version, &checksum, &dirty); err != nil {
			return nil, err
		}
		states[version] = migrationState{checksum: checksum, dirty: dirty}
	}
	return states, rows.Err()
}

func validateMigrationState(
	migrations []migration,
	states map[string]migrationState,
	allowIncomplete bool,
) error {
	known := make(map[string]string, len(migrations))
	latest := ""
	for _, item := range migrations {
		known[item.version] = item.checksum
		latest = item.version
	}
	hasUnknown := false
	for version, state := range states {
		if state.dirty {
			return incompatible(fmt.Sprintf("migration %s is dirty", version), nil)
		}
		expected, exists := known[version]
		if !exists {
			if version <= latest {
				return incompatible(
					fmt.Sprintf("unknown migration %s precedes the supported schema", version), nil,
				)
			}
			hasUnknown = true
			continue
		}
		if state.checksum != expected {
			return incompatible(fmt.Sprintf("migration %s checksum changed", version), nil)
		}
	}
	missing := ""
	for _, item := range migrations {
		if _, exists := states[item.version]; !exists {
			if missing == "" {
				missing = item.version
			}
			continue
		}
		if missing != "" {
			return incompatible(
				fmt.Sprintf("migration %s is not applied before %s", missing, item.version), nil,
			)
		}
	}
	if missing != "" && (!allowIncomplete || hasUnknown) {
		return incompatible(fmt.Sprintf("migration %s is not applied", missing), nil)
	}
	return nil
}

func (s *Store) applyMigration(
	ctx context.Context,
	connection *sql.Conn,
	item migration,
) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	marker, err := connection.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration marker %s: %w", item.version, err)
	}
	if _, err := marker.ExecContext(ctx, s.bind(
		`INSERT INTO schema_migrations(version,checksum,dirty,applied_at)
			VALUES(?,?,?,?)`,
	), item.version, item.checksum, true, now); err != nil {
		_ = marker.Rollback()
		return fmt.Errorf("mark migration %s dirty: %w", item.version, err)
	}
	if err := marker.Commit(); err != nil {
		return fmt.Errorf("commit migration marker %s: %w", item.version, err)
	}

	tx, err := connection.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", item.version, err)
	}
	statement := s.renderMigration(item.source)
	if _, err := tx.ExecContext(ctx, statement); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("apply migration %s: %w", item.version, err)
	}
	if _, err := tx.ExecContext(ctx, s.bind(
		`UPDATE schema_migrations SET dirty=?, applied_at=? WHERE version=?`,
	), false, time.Now().UTC().Format(time.RFC3339Nano), item.version); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("complete migration %s: %w", item.version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", item.version, err)
	}
	return nil
}

func (s *Store) renderMigration(source string) string {
	eventsID := "INTEGER PRIMARY KEY AUTOINCREMENT"
	if s.postgres {
		eventsID = "BIGSERIAL PRIMARY KEY"
	}
	statement := strings.ReplaceAll(source, "{{EVENTS_ID}}", eventsID)
	return strings.ReplaceAll(
		statement, "{{ARCHIVE_IGNORED_JOBS}}", s.archiveIgnoredJobsStatement(),
	)
}

func (s *Store) archiveIgnoredJobsStatement() string {
	ignoredAnnotation := `json_extract(template, '$.metadata.annotations."kubequeue.io/ignore"')`
	helmHookAnnotation := `json_extract(template, '$.metadata.annotations."helm.sh/hook"')`
	internalLabel := `json_extract(template, '$.metadata.labels."kubequeue.io/internal"')`
	if s.postgres {
		ignoredAnnotation = `template::jsonb -> 'metadata' -> 'annotations' ->> 'kubequeue.io/ignore'`
		helmHookAnnotation = `template::jsonb -> 'metadata' -> 'annotations' ->> 'helm.sh/hook'`
		internalLabel = `template::jsonb -> 'metadata' -> 'labels' ->> 'kubequeue.io/internal'`
	}
	return fmt.Sprintf(`UPDATE jobs
		SET management_mode='IGNORED', sync_status='STALE',
			archived_at=COALESCE(archived_at, updated_at)
		WHERE LOWER(COALESCE(%s, ''))='true'
			OR COALESCE(%s, '')<>''
			OR LOWER(COALESCE(%s, ''))='true'`,
		ignoredAnnotation, helmHookAnnotation, internalLabel)
}

func incompatible(message string, cause error) error {
	if cause == nil {
		return fmt.Errorf("%w: %s", ErrSchemaIncompatible, message)
	}
	return fmt.Errorf("%w: %s: %w", ErrSchemaIncompatible, message, cause)
}
