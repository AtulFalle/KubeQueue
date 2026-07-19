package migration

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/adapters/persistence"
)

func TestRunMigratesConfiguredDatabase(t *testing.T) {
	databaseURL := "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "platform.db"))
	t.Setenv("KUBEQUEUE_DATABASE_URL", databaseURL)
	t.Setenv("KUBEQUEUE_RELEASE_NAMESPACE", "kubequeue")
	t.Setenv("KUBEQUEUE_WATCH_MODE", "selected")
	t.Setenv("KUBEQUEUE_WATCH_NAMESPACES", "jobs-a,jobs-b")
	t.Setenv("KUBEQUEUE_EXCLUDED_NAMESPACES", "")

	if err := Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	store, err := persistence.OpenCompatible(context.Background(), databaseURL)
	if err != nil {
		t.Fatalf("OpenCompatible() error = %v", err)
	}
	_ = store.Close()
}
