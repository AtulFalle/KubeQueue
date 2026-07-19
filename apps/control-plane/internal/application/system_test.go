package application_test

import (
	"context"
	"testing"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/adapters/persistence"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/application"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
)

func TestSystemStatusReportsHealthyWorker(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:test-system-status?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	setReadyWorkerStatus(t, store, "default")

	status, err := application.NewSystem(store).Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !status.API.Ready || !status.Database.Ready ||
		status.Worker.State != domain.WorkerStateReady ||
		len(status.Watch.Namespaces) != 1 || len(status.ActiveErrors) != 0 {
		t.Fatalf("Status() = %#v", status)
	}
}

func TestSystemStatusMarksStaleWorkerUnavailable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:test-system-status-stale?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	stale := time.Now().UTC().Add(-time.Minute)
	if err := store.UpdateWorkerStatus(ctx, domain.WorkerStatus{
		State: domain.WorkerStateReady, HeartbeatAt: &stale,
		WatchMode: domain.WatchModeSelected, EffectiveNamespaces: []string{"default"},
		GlobalConcurrency: 10, NamespaceConcurrency: 5,
	}); err != nil {
		t.Fatal(err)
	}

	status, err := application.NewSystem(store).Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Worker.State != domain.WorkerStateUnavailable ||
		len(status.ActiveErrors) == 0 ||
		status.ActiveErrors[0].Code != "WORKER_HEARTBEAT_STALE" {
		t.Fatalf("Status() = %#v", status)
	}
}
