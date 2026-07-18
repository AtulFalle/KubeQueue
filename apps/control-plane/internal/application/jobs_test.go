package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/adapters/persistence"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/application"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/ports"
)

func TestRetryCreatesLinkedAttempt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:test-application-retry?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	jobs := application.NewJobs(store)
	original, err := jobs.Create(ctx, domain.CreateJob{
		Name: "report", Namespace: "default", Template: json.RawMessage(`{
			"spec":{"template":{"spec":{"restartPolicy":"Never","containers":[{"name":"job","image":"busybox"}]}}}
		}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetObserved(ctx, original.ID, domain.Observation{
		State: domain.StateFailed, KubernetesUID: "uid",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := jobs.Command(ctx, original.ID, "pause"); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("pause failed job error = %v, want invalid transition", err)
	}

	retry, err := jobs.Command(ctx, original.ID, "retry")
	if err != nil {
		t.Fatal(err)
	}
	if retry.ParentID != original.ID || retry.Attempt != 2 {
		t.Fatalf("retry lineage = parent %q attempt %d", retry.ParentID, retry.Attempt)
	}
	repeated, err := jobs.Command(ctx, original.ID, "retry")
	if err != nil {
		t.Fatal(err)
	}
	if repeated.ID != retry.ID {
		t.Fatalf("repeated retry created %q, want existing attempt %q", repeated.ID, retry.ID)
	}
}

func TestCommandRejectsObservedJob(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:test-observed-command?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	observed, err := store.Adopt(ctx, domain.Job{
		Name: "external", Namespace: "default",
		DesiredState: domain.StateRunning, ObservedState: domain.StateRunning,
		ManagementMode: domain.ManagementObserved, SyncStatus: domain.SyncStatusSynced,
		KubernetesUID: "external-uid", Template: json.RawMessage(`{
			"spec":{"template":{"spec":{"restartPolicy":"Never","containers":[{"name":"job","image":"busybox"}]}}}
		}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = application.NewJobs(store).Command(ctx, observed.ID, "pause")
	if !errors.Is(err, domain.ErrUnmanagedJob) {
		t.Fatalf("Command() error = %v, want unmanaged Job", err)
	}
}

func TestArchiveIsLimitedToStaleRecordsAndIdempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:test-archive?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	jobs := application.NewJobs(store)
	job, err := jobs.Create(ctx, domain.CreateJob{
		Name: "job", Namespace: "default", Template: json.RawMessage(`{
			"spec":{"template":{"spec":{"restartPolicy":"Never","containers":[{"name":"job","image":"busybox"}]}}}
		}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := jobs.Archive(ctx, job.ID); !errors.Is(err, domain.ErrNotArchivable) {
		t.Fatalf("Archive() active Job error = %v", err)
	}
	job, err = store.SetObserved(ctx, job.ID, domain.Observation{
		State: domain.StateRunning, KubernetesUID: "uid", ObservedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkMissing(
		ctx, job.ID, job.KubernetesUID, job.ResourceVersion, time.Now().UTC(),
	); err != nil {
		t.Fatal(err)
	}
	if err := jobs.Archive(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if err := jobs.Archive(ctx, job.ID); err != nil {
		t.Fatalf("repeated Archive() error = %v", err)
	}
	if _, err := jobs.Get(ctx, job.ID); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("Get() archived Job error = %v", err)
	}
}
