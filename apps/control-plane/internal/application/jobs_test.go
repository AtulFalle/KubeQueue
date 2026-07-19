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
	setReadyWorkerStatus(t, store, "default")
	jobs := application.NewJobs(store, selectedScope(t, "default"))
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

func TestJobsExposeGlobalFacetsAndCompleteQueue(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:test-application-inventory?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	first, err := store.Create(ctx, domain.CreateJob{
		Name: "first", Namespace: "batch", Team: "data", Priority: 1,
		Template: json.RawMessage(`{
			"spec":{"template":{"spec":{"restartPolicy":"Never","containers":[{"name":"job","image":"busybox"}]}}}
		}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, domain.CreateJob{
		Name: "second", Namespace: "default", Team: "platform", Priority: 10,
		Template: first.Template,
	}); err != nil {
		t.Fatal(err)
	}

	jobs := application.NewJobs(store, selectedScope(t, "batch", "default"))
	facets, err := jobs.Facets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if facets.Total != 2 || len(facets.Namespaces) != 2 {
		t.Fatalf("Facets() = %#v", facets)
	}
	queue, version, err := jobs.Queue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(queue) != 2 || queue[0].Name != "second" || version < 1 {
		t.Fatalf("Queue() = %#v, version %d", queue, version)
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

	_, err = application.NewJobs(store, selectedScope(t, "default")).Command(ctx, observed.ID, "pause")
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
	setReadyWorkerStatus(t, store, "default")
	jobs := application.NewJobs(store, selectedScope(t, "default"))
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

func TestCreateRejectsNamespaceOutsideEffectiveScope(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:test-create-scope?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	jobs := application.NewJobs(store, selectedScope(t, "batch"))

	_, err = jobs.Create(ctx, domain.CreateJob{
		Name: "job", Namespace: "other", Template: json.RawMessage(`{
			"spec":{"template":{"spec":{"restartPolicy":"Never","containers":[{"name":"job","image":"busybox"}]}}}
		}`),
	})
	if !errors.Is(err, domain.ErrNamespaceOutOfScope) {
		t.Fatalf("Create() error = %v, want namespace out of scope", err)
	}
	stored, listErr := store.List(ctx, ports.JobFilter{})
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(stored) != 0 {
		t.Fatalf("out-of-scope submission persisted: %#v", stored)
	}
}

func TestCreateRejectsNamespaceWithoutWorkerReadiness(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:test-create-readiness?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	if err := store.UpdateWorkerStatus(ctx, domain.WorkerStatus{
		State: domain.WorkerStateDegraded, HeartbeatAt: &now,
		WatchMode: domain.WatchModeSelected, EffectiveNamespaces: []string{"batch"},
		Namespaces: []domain.NamespaceAuthorityStatus{{
			Namespace: "batch", InformerSynced: true, Authorized: false,
			Message: "create access is denied", ObservedAt: &now,
		}},
		GlobalConcurrency: 10, NamespaceConcurrency: 5,
	}); err != nil {
		t.Fatal(err)
	}
	jobs := application.NewJobs(store, selectedScope(t, "batch"))
	_, err = jobs.Create(ctx, domain.CreateJob{
		Name: "job", Namespace: "batch", Template: json.RawMessage(`{
			"spec":{"template":{"spec":{"restartPolicy":"Never","containers":[{"name":"job","image":"busybox"}]}}}
		}`),
	})
	if !errors.Is(err, domain.ErrNamespaceUnavailable) {
		t.Fatalf("Create() error = %v, want namespace unavailable", err)
	}
}

func selectedScope(t *testing.T, namespaces ...string) domain.NamespaceScope {
	t.Helper()
	scope, err := domain.NewNamespaceScope(domain.WatchModeSelected, namespaces, nil)
	if err != nil {
		t.Fatal(err)
	}
	return scope
}

func setReadyWorkerStatus(t *testing.T, store *persistence.Store, namespaces ...string) {
	t.Helper()
	now := time.Now().UTC()
	namespaceStatuses := make([]domain.NamespaceAuthorityStatus, 0, len(namespaces))
	for _, namespace := range namespaces {
		namespaceStatuses = append(namespaceStatuses, domain.NamespaceAuthorityStatus{
			Namespace: namespace, InformerSynced: true, Authorized: true, ObservedAt: &now,
		})
	}
	if err := store.UpdateWorkerStatus(context.Background(), domain.WorkerStatus{
		State: domain.WorkerStateReady, HeartbeatAt: &now,
		LastSuccessfulReconciliationAt: &now,
		WatchMode:                      domain.WatchModeSelected,
		EffectiveNamespaces:            namespaces,
		Namespaces:                     namespaceStatuses,
		GlobalConcurrency:              10,
		NamespaceConcurrency:           5,
		ReleaseVersion:                 "test",
	}); err != nil {
		t.Fatal(err)
	}
}
