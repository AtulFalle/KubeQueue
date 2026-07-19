package persistence

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifySchemaDoesNotCreateMigrationHistory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := open(ctx, sqliteTestURL(t, "verify-empty.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	err = store.VerifySchema(ctx)
	if !errors.Is(err, ErrSchemaIncompatible) {
		t.Fatalf("VerifySchema() error = %v, want ErrSchemaIncompatible", err)
	}
	var tables int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master
		WHERE type='table' AND name='schema_migrations'`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 0 {
		t.Fatal("VerifySchema() created migration history")
	}
}

func TestMigrateRecordsChecksumsAndCompatibleSchema(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	databaseURL := sqliteTestURL(t, "compatible.db")
	if err := Migrate(ctx, databaseURL); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	store, err := OpenCompatible(ctx, databaseURL)
	if err != nil {
		t.Fatalf("OpenCompatible() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var missingChecksums, dirtyMigrations int
	if err := store.db.QueryRowContext(ctx, `SELECT
		SUM(CASE WHEN checksum='' THEN 1 ELSE 0 END),
		SUM(CASE WHEN dirty THEN 1 ELSE 0 END)
		FROM schema_migrations`).Scan(&missingChecksums, &dirtyMigrations); err != nil {
		t.Fatal(err)
	}
	if missingChecksums != 0 || dirtyMigrations != 0 {
		t.Fatalf("migration state = missing checksums %d, dirty %d",
			missingChecksums, dirtyMigrations)
	}
}

func TestVerifySchemaAcceptsLaterCleanMigration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	databaseURL := sqliteTestURL(t, "later.db")
	if err := Migrate(ctx, databaseURL); err != nil {
		t.Fatal(err)
	}
	store, err := open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.db.ExecContext(ctx, `INSERT INTO schema_migrations
		(version,checksum,dirty,applied_at) VALUES
		('migrations/999_future.sql','future-checksum',FALSE,'2026-07-19T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}

	if err := store.VerifySchema(ctx); err != nil {
		t.Fatalf("VerifySchema() error = %v", err)
	}
}

func TestVerifySchemaRejectsChangedDirtyAndIncompleteHistory(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate string
		want   string
	}{
		{
			name:   "checksum",
			mutate: `UPDATE schema_migrations SET checksum='changed' WHERE version='migrations/001_initial.sql'`,
			want:   "checksum changed",
		},
		{
			name:   "dirty",
			mutate: `UPDATE schema_migrations SET dirty=TRUE WHERE version='migrations/001_initial.sql'`,
			want:   "is dirty",
		},
		{
			name:   "incomplete",
			mutate: `DELETE FROM schema_migrations WHERE version='migrations/007_reconciliation_error_details.sql'`,
			want:   "is not applied",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			databaseURL := sqliteTestURL(t, test.name+".db")
			if err := Migrate(ctx, databaseURL); err != nil {
				t.Fatal(err)
			}
			store, err := open(ctx, databaseURL)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			if _, err := store.db.ExecContext(ctx, test.mutate); err != nil {
				t.Fatal(err)
			}

			err = store.VerifySchema(ctx)
			if !errors.Is(err, ErrSchemaIncompatible) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("VerifySchema() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestMigrateBaselinesLegacyHistory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	databaseURL := sqliteTestURL(t, "legacy.db")
	if err := Migrate(ctx, databaseURL); err != nil {
		t.Fatal(err)
	}
	store, err := open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(
		ctx, `ALTER TABLE schema_migrations DROP COLUMN dirty`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(
		ctx, `ALTER TABLE schema_migrations DROP COLUMN checksum`,
	); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(ctx, databaseURL); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	compatible, err := OpenCompatible(ctx, databaseURL)
	if err != nil {
		t.Fatalf("OpenCompatible() error = %v", err)
	}
	_ = compatible.Close()
}

func sqliteTestURL(t *testing.T, name string) string {
	t.Helper()
	return "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), name)) +
		"?_pragma=busy_timeout(5000)"
}
