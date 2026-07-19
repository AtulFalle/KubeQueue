package persistence

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
)

func TestMigrateAndBackfillSelectedScopeIsDeterministicAndIdempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	databaseURL := sqliteTestURL(t, "selected-backfill.db")
	if err := Migrate(ctx, databaseURL); err != nil {
		t.Fatal(err)
	}
	createHistoricalJob(t, ctx, databaseURL, "historical")

	scope, err := domain.NewNamespaceScope(
		domain.WatchModeSelected,
		[]string{"configured-b", "configured-a", "configured-a"},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateAndBackfill(ctx, databaseURL, scope); err != nil {
		t.Fatalf("MigrateAndBackfill() error = %v", err)
	}
	if err := MigrateAndBackfill(ctx, databaseURL, scope); err != nil {
		t.Fatalf("second MigrateAndBackfill() error = %v", err)
	}

	store, err := open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	assertCount(t, store, `SELECT COUNT(*) FROM installations WHERE id='default'`, 1)
	assertCount(t, store, `SELECT COUNT(*) FROM projects WHERE id='default'`, 1)
	assertCount(t, store, `SELECT COUNT(*) FROM principals
		WHERE id='legacy_admin' AND kind='LEGACY_ADMIN'`, 1)
	assertCount(t, store, `SELECT COUNT(*) FROM role_definitions WHERE built_in=TRUE`, 7)
	assertCount(t, store, `SELECT COUNT(*) FROM role_bindings
		WHERE id='legacy_admin_owner' AND principal_id='legacy_admin'
		AND role_definition_id='installation_owner'`, 1)
	assertCount(t, store, `SELECT COUNT(*) FROM namespace_bindings
		WHERE namespace IN ('configured-a','configured-b','historical')`, 3)
	assertCount(t, store, `SELECT COUNT(*) FROM namespace_bindings`, 3)
	assertCount(t, store, `SELECT COUNT(*) FROM jobs
		WHERE namespace='historical' AND project_id='default'
		AND namespace_binding_id='default__historical'
		AND creator_principal_id='legacy_admin'
		AND submission_source='LEGACY_COMPATIBILITY'`, 1)
	rows, err := store.db.QueryContext(
		t.Context(), `SELECT permissions FROM role_definitions WHERE built_in=TRUE`,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var encoded string
		if err := rows.Scan(&encoded); err != nil {
			t.Fatal(err)
		}
		var permissions []domain.Permission
		if err := json.Unmarshal([]byte(encoded), &permissions); err != nil {
			t.Fatal(err)
		}
		for _, permission := range permissions {
			if !permission.Valid() {
				t.Errorf("built-in role contains permission outside catalog: %q", permission)
			}
		}
	}
}

func TestMigrateAndBackfillAllScopeBindsHistoricalNamespacesOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	databaseURL := sqliteTestURL(t, "all-backfill.db")
	if err := Migrate(ctx, databaseURL); err != nil {
		t.Fatal(err)
	}
	createHistoricalJob(t, ctx, databaseURL, "historical")
	scope, err := domain.NewNamespaceScope(
		domain.WatchModeAll,
		[]string{"not-enumerable"},
		[]string{"kube-system"},
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := MigrateAndBackfill(ctx, databaseURL, scope); err != nil {
		t.Fatalf("MigrateAndBackfill() error = %v", err)
	}
	store, err := open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	assertCount(t, store, `SELECT COUNT(*) FROM namespace_bindings
		WHERE namespace='historical'`, 1)
	assertCount(t, store, `SELECT COUNT(*) FROM namespace_bindings
		WHERE namespace='not-enumerable'`, 0)
}

func createHistoricalJob(
	t *testing.T, ctx context.Context, databaseURL, namespace string,
) {
	t.Helper()
	store, err := OpenCompatible(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Create(ctx, domain.CreateJob{
		Name:      "historical-job",
		Namespace: namespace,
		Team:      "legacy-display-only",
		Template: json.RawMessage(
			`{"apiVersion":"batch/v1","kind":"Job","spec":{"template":{"spec":{"restartPolicy":"Never","containers":[{"name":"job","image":"busybox"}]}}}}`,
		),
	})
	if closeErr := store.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func assertCount(t *testing.T, store *Store, query string, want int) {
	t.Helper()
	var got int
	if err := store.db.QueryRowContext(t.Context(), query).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s count = %d, want %d", query, got, want)
	}
}
