package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/ports"
)

var validJobTemplate = json.RawMessage(`{
	"spec":{"template":{"spec":{"restartPolicy":"Never","containers":[{"name":"job","image":"busybox"}]}}}
}`)

func TestStoreCreateListAndLifecycle(t *testing.T) {
	t.Parallel()
	store, err := Open(context.Background(), "file:test-create?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	job, err := store.Create(context.Background(), domain.CreateJob{
		Name: "report", Namespace: "batch", Team: "data", Priority: 25,
		Template: validJobTemplate,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if job.Position != 1 || job.DesiredState != domain.StateQueued {
		t.Fatalf("created job = %#v", job)
	}
	if job.ManagementMode != domain.ManagementManaged ||
		job.SyncStatus != domain.SyncStatusPending || !job.ActionPending {
		t.Fatalf("created synchronization state = %#v", job)
	}

	jobs, err := store.List(context.Background(), ports.JobFilter{Namespace: "batch"})
	if err != nil || len(jobs) != 1 {
		t.Fatalf("List() = %d jobs, %v", len(jobs), err)
	}
	paused, err := store.SetDesiredState(context.Background(), job.ID, domain.StatePaused)
	if err != nil {
		t.Fatalf("SetDesiredState() error = %v", err)
	}
	if paused.DesiredState != domain.StatePaused {
		t.Errorf("desired state = %s", paused.DesiredState)
	}
	events, err := store.Events(context.Background(), job.ID)
	if err != nil || len(events) < 2 {
		t.Fatalf("Events() = %d events, %v", len(events), err)
	}
}

func TestStoreFacetsAggregateActiveInventory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := Open(ctx, "file:test-facets?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	for _, input := range []domain.CreateJob{
		{Name: "alpha", Namespace: "batch", Team: "data", Template: validJobTemplate},
		{Name: "beta", Namespace: "default", Team: "platform", Template: validJobTemplate},
	} {
		if _, err := store.Create(ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	observed, err := store.Adopt(ctx, domain.Job{
		Name: "external", Namespace: "batch", Team: "platform",
		DesiredState: domain.StateRunning, ObservedState: domain.StateRunning,
		ManagementMode: domain.ManagementObserved, SyncStatus: domain.SyncStatusSynced,
		KubernetesUID: "facets-external", Template: validJobTemplate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if observed.ID == "" {
		t.Fatal("Adopt() returned an empty id")
	}
	archived, err := store.Create(ctx, domain.CreateJob{
		Name: "archived", Namespace: "hidden", Team: "hidden", Template: validJobTemplate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Archive(ctx, archived.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	facets, err := store.Facets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if facets.Total != 3 ||
		facets.ObservedStateCounts[string(domain.StateCreated)] != 2 ||
		facets.ObservedStateCounts[string(domain.StateRunning)] != 1 {
		t.Fatalf("Facets() counts = %#v", facets)
	}
	if len(facets.Namespaces) != 2 || facets.Namespaces[0] != "batch" ||
		facets.Namespaces[1] != "default" {
		t.Fatalf("Facets() namespaces = %#v", facets.Namespaces)
	}
	if len(facets.Teams) != 2 || facets.Teams[0] != "data" ||
		facets.Teams[1] != "platform" {
		t.Fatalf("Facets() teams = %#v", facets.Teams)
	}
}

func TestStoreQueueReturnsOnlyManagedQueuedJobsAndVersion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := Open(ctx, "file:test-complete-queue?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	low, err := store.Create(ctx, domain.CreateJob{
		Name: "low", Namespace: "default", Priority: 1, Template: validJobTemplate,
	})
	if err != nil {
		t.Fatal(err)
	}
	high, err := store.Create(ctx, domain.CreateJob{
		Name: "high", Namespace: "default", Priority: 10, Template: validJobTemplate,
	})
	if err != nil {
		t.Fatal(err)
	}
	paused, err := store.Create(ctx, domain.CreateJob{
		Name: "paused", Namespace: "default", Priority: 20, Template: validJobTemplate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetDesiredState(ctx, paused.ID, domain.StatePaused); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Adopt(ctx, domain.Job{
		Name: "observed", Namespace: "default", Priority: 30,
		DesiredState: domain.StateQueued, ObservedState: domain.StateCreated,
		ManagementMode: domain.ManagementObserved, SyncStatus: domain.SyncStatusSynced,
		KubernetesUID: "queue-observed", Template: validJobTemplate,
	}); err != nil {
		t.Fatal(err)
	}
	archived, err := store.Create(ctx, domain.CreateJob{
		Name: "archived", Namespace: "default", Priority: 40, Template: validJobTemplate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Archive(ctx, archived.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	wantVersion, err := store.QueueVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	queue, version, err := store.Queue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if version != wantVersion {
		t.Fatalf("Queue() version = %d, want %d", version, wantVersion)
	}
	if len(queue) != 2 || queue[0].ID != high.ID || queue[1].ID != low.ID {
		t.Fatalf("Queue() = %#v", queue)
	}
	for _, job := range queue {
		if job.ManagementMode != domain.ManagementManaged ||
			job.DesiredState != domain.StateQueued || job.ArchivedAt != nil {
			t.Fatalf("Queue() included ineligible Job %#v", job)
		}
	}
}

func TestIgnoredWorkloadRepairArchivesExistingRecord(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := Open(ctx, "file:test-ignored-repair?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	job, err := store.Create(ctx, domain.CreateJob{
		Name: "migration", Namespace: "default", Template: validJobTemplate,
	})
	if err != nil {
		t.Fatal(err)
	}
	ignoredTemplate := `{
		"metadata":{"annotations":{"helm.sh/hook":"pre-upgrade"}},
		"spec":{"template":{"spec":{
			"restartPolicy":"Never",
			"containers":[{"name":"migration","image":"example"}]
		}}}
	}`
	if _, err := store.db.ExecContext(
		ctx, store.bind(`UPDATE jobs SET template=? WHERE id=?`), ignoredTemplate, job.ID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, store.archiveIgnoredJobsStatement()); err != nil {
		t.Fatal(err)
	}

	jobs, err := store.List(ctx, ports.JobFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 0 {
		t.Fatalf("List() returned repaired ignored Job: %#v", jobs)
	}
	repaired, err := store.Get(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if repaired.ManagementMode != domain.ManagementIgnored || repaired.ArchivedAt == nil {
		t.Fatalf("repaired Job = %#v", repaired)
	}
}

func TestStoreRejectsStaleObservationCompareAndSet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := Open(ctx, "file:test-observation-cas?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	job, err := store.Create(ctx, domain.CreateJob{
		Name: "job", Namespace: "default", Template: validJobTemplate,
	})
	if err != nil {
		t.Fatal(err)
	}
	observedAt := time.Now().UTC()
	current, err := store.SetObserved(ctx, job.ID, domain.Observation{
		State: domain.StateRunning, KubernetesUID: "uid", ResourceVersion: "new",
		ExpectedResourceVersion: "", ObservedAt: observedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.SetObserved(ctx, job.ID, domain.Observation{
		State: domain.StateFailed, KubernetesUID: "uid", ResourceVersion: "old",
		ExpectedResourceVersion: "", ObservedAt: observedAt.Add(-time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if stored.ResourceVersion != current.ResourceVersion ||
		stored.ObservedState != domain.StateRunning {
		t.Fatalf("stale observation replaced current state: %#v", stored)
	}
}

func TestStoreMarksMissingWithoutCancelling(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := Open(ctx, "file:test-missing?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	job, err := store.Create(ctx, domain.CreateJob{
		Name: "job", Namespace: "default", Template: validJobTemplate,
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err = store.SetObserved(ctx, job.ID, domain.Observation{
		State: domain.StateRunning, KubernetesUID: "uid", ObservedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err = store.MarkMissing(ctx, job.ID, "uid", job.ResourceVersion, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if job.SyncStatus != domain.SyncStatusMissing ||
		job.ObservedState != domain.StateRunning {
		t.Fatalf("missing Job state = %#v", job)
	}
}

func TestStoreMarksOutOfScopeAndRejectsQueueMutation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := Open(ctx, "file:test-out-of-scope?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	job, err := store.Create(ctx, domain.CreateJob{
		Name: "job", Namespace: "removed", Template: validJobTemplate,
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err = store.MarkOutOfScope(ctx, job.ID, job.ResourceVersion, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if job.SyncStatus != domain.SyncStatusOutOfScope {
		t.Fatalf("sync status = %s", job.SyncStatus)
	}
	if _, err := store.UpdateQueue(
		ctx, job.ID, job.Priority, job.Position, job.Version, job.ScheduledFor,
	); !errors.Is(err, ports.ErrConflict) {
		t.Fatalf("UpdateQueue() error = %v, want conflict", err)
	}
}

func TestStorePersistsWorkerStatusAndLastSuccess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := Open(ctx, "file:test-worker-status?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC().Truncate(time.Microsecond)
	status := domain.WorkerStatus{
		State:                          domain.WorkerStateReady,
		HeartbeatAt:                    &now,
		LastSuccessfulReconciliationAt: &now,
		WatchMode:                      domain.WatchModeSelected,
		EffectiveNamespaces:            []string{"batch", "default"},
		Namespaces: []domain.NamespaceAuthorityStatus{{
			Namespace: "batch", InformerSynced: true, Authorized: true, ObservedAt: &now,
		}},
		GlobalConcurrency: 10, NamespaceConcurrency: 5, ReleaseVersion: "test",
	}
	if err := store.UpdateWorkerStatus(ctx, status); err != nil {
		t.Fatal(err)
	}
	later := now.Add(time.Second)
	status.State = domain.WorkerStateDegraded
	status.HeartbeatAt = &later
	status.LastSuccessfulReconciliationAt = nil
	status.ActiveError = "authorization failed"
	if err := store.UpdateWorkerStatus(ctx, status); err != nil {
		t.Fatal(err)
	}
	stored, err := store.WorkerStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != domain.WorkerStateDegraded || stored.HeartbeatAt == nil ||
		!stored.HeartbeatAt.Equal(later) || stored.LastSuccessfulReconciliationAt == nil ||
		!stored.LastSuccessfulReconciliationAt.Equal(now) ||
		len(stored.Namespaces) != 1 || stored.ActiveError != "authorization failed" {
		t.Fatalf("WorkerStatus() = %#v", stored)
	}
}

func TestStoreRejectsStaleQueueUpdate(t *testing.T) {
	t.Parallel()
	store, err := Open(context.Background(), "file:test-conflict?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	job, err := store.Create(context.Background(), domain.CreateJob{
		Name: "job", Namespace: "default", Template: validJobTemplate,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.UpdateQueue(context.Background(), job.ID, 1, 1, job.Version+10, nil)
	if !errors.Is(err, ports.ErrConflict) {
		t.Fatalf("UpdateQueue() error = %v, want %v", err, ports.ErrConflict)
	}
}

func TestStoreReorderRejectsUnknownJobs(t *testing.T) {
	t.Parallel()
	store, err := Open(context.Background(), "file:test-reorder?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	job, err := store.Create(context.Background(), domain.CreateJob{
		Name: "job", Namespace: "default", Template: validJobTemplate,
	})
	if err != nil {
		t.Fatal(err)
	}

	version, err := store.QueueVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Reorder(context.Background(), []string{job.ID, "missing"}, version)
	if !errors.Is(err, ports.ErrConflict) {
		t.Fatalf("Reorder() error = %v, want %v", err, ports.ErrConflict)
	}
}

func TestStoreReorderRejectsIncompleteAndDuplicateQueue(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := Open(ctx, "file:test-reorder-complete?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	first, err := store.Create(ctx, domain.CreateJob{
		Name: "first", Namespace: "default", Priority: 10, Template: validJobTemplate,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Create(ctx, domain.CreateJob{
		Name: "second", Namespace: "default", Template: validJobTemplate,
	})
	if err != nil {
		t.Fatal(err)
	}
	version, err := store.QueueVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, ids := range [][]string{
		{first.ID},
		{first.ID, first.ID},
		{second.ID, first.ID},
		{first.ID, second.ID, "extra"},
	} {
		if _, err := store.Reorder(ctx, ids, version); !errors.Is(err, ports.ErrConflict) {
			t.Fatalf("Reorder(%#v) error = %v, want conflict", ids, err)
		}
	}
}

func TestStoreReorderAdvancesDedicatedQueueVersion(t *testing.T) {
	t.Parallel()
	store, err := Open(context.Background(), "file:test-queue-version?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	first, err := store.Create(context.Background(), domain.CreateJob{
		Name: "first", Namespace: "default", Template: validJobTemplate,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Create(context.Background(), domain.CreateJob{
		Name: "second", Namespace: "default", Template: validJobTemplate,
	})
	if err != nil {
		t.Fatal(err)
	}

	currentVersion, err := store.QueueVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	version, err := store.Reorder(
		context.Background(), []string{second.ID, first.ID}, currentVersion,
	)
	if err != nil {
		t.Fatal(err)
	}
	storedVersion, err := store.QueueVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if version != currentVersion+1 || storedVersion != currentVersion+1 {
		t.Fatalf("queue versions = returned %d stored %d, started at %d",
			version, storedVersion, currentVersion)
	}
}

func TestStoreQueueMembershipChangesAdvanceVersion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := Open(ctx, "file:test-queue-membership-version?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	job, err := store.Create(ctx, domain.CreateJob{
		Name: "job", Namespace: "default", Template: validJobTemplate,
	})
	if err != nil {
		t.Fatal(err)
	}
	createdVersion, err := store.QueueVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetDesiredState(ctx, job.ID, domain.StatePaused); err != nil {
		t.Fatal(err)
	}
	pausedVersion, err := store.QueueVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pausedVersion != createdVersion+1 {
		t.Fatalf("paused queue version = %d, want %d", pausedVersion, createdVersion+1)
	}
	running, err := store.Create(ctx, domain.CreateJob{
		Name: "running", Namespace: "default", Template: validJobTemplate,
	})
	if err != nil {
		t.Fatal(err)
	}
	beforeRunning, err := store.QueueVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetObserved(ctx, running.ID, domain.Observation{
		State: domain.StateRunning, KubernetesUID: "running-uid", ObservedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	afterRunning, err := store.QueueVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if afterRunning != beforeRunning+1 {
		t.Fatalf("running queue version = %d, want %d", afterRunning, beforeRunning+1)
	}
}

func TestStoreDoesNotAutomaticallyRescheduleFailedJobs(t *testing.T) {
	t.Parallel()
	store, err := Open(context.Background(), "file:test-failed?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	job, err := store.Create(context.Background(), domain.CreateJob{
		Name: "job", Namespace: "default", Template: validJobTemplate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetObserved(context.Background(), job.ID, domain.Observation{
		State: domain.StateFailed, KubernetesUID: "uid", ObservedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	eligible, err := store.Eligible(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(eligible) != 0 {
		t.Fatalf("Eligible() returned failed jobs: %#v", eligible)
	}
}

func TestStoreSchedulerLeaseHasSingleHolder(t *testing.T) {
	t.Parallel()
	store, err := Open(context.Background(), "file:test-lease?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	acquired, err := store.AcquireSchedulerLease(context.Background(), "worker-a", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("first AcquireSchedulerLease() = %v, %v", acquired, err)
	}
	acquired, err = store.AcquireSchedulerLease(context.Background(), "worker-b", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if acquired {
		t.Fatal("second worker acquired an active scheduler lease")
	}
}

func TestStoreClaimsEligibleJobsOnce(t *testing.T) {
	t.Parallel()
	store, err := Open(context.Background(), "file:test-claims?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	job, err := store.Create(context.Background(), domain.CreateJob{
		Name: "job", Namespace: "default", Template: validJobTemplate,
	})
	if err != nil {
		t.Fatal(err)
	}

	first, err := store.ClaimEligible(context.Background(), "worker-a", 10, time.Minute)
	if err != nil || len(first) != 1 || first[0].ID != job.ID {
		t.Fatalf("first ClaimEligible() = %#v, %v", first, err)
	}
	second, err := store.ClaimEligible(context.Background(), "worker-b", 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 {
		t.Fatalf("second ClaimEligible() = %#v, want no jobs", second)
	}
	if err := store.ReleaseSchedulerClaim(context.Background(), job.ID, "worker-a"); err != nil {
		t.Fatal(err)
	}
	third, err := store.ClaimEligible(context.Background(), "worker-b", 10, time.Minute)
	if err != nil || len(third) != 1 {
		t.Fatalf("third ClaimEligible() = %#v, %v", third, err)
	}
}

func TestStoreAdoptIsIdempotentByKubernetesUID(t *testing.T) {
	t.Parallel()
	store, err := Open(context.Background(), "file:test-adopt-idempotent?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	input := domain.Job{
		Name: "adopted", Namespace: "default", DesiredState: domain.StateRunning,
		ObservedState: domain.StateRunning, KubernetesUID: "uid-1", Template: validJobTemplate,
	}
	first, err := store.Adopt(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Adopt(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("adoption ids = %q and %q", first.ID, second.ID)
	}
	jobs, err := store.List(context.Background(), ports.JobFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("adoption created %d records, want 1", len(jobs))
	}
}

func TestStorePersistsRetryLineage(t *testing.T) {
	t.Parallel()
	store, err := Open(context.Background(), "file:test-retry-lineage?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	parent, err := store.Create(context.Background(), domain.CreateJob{
		Name: "job", Namespace: "default", Template: validJobTemplate,
	})
	if err != nil {
		t.Fatal(err)
	}
	retry, err := store.Create(context.Background(), domain.CreateJob{
		Name: "job-retry-2", Namespace: "default", Template: parent.Template,
		ParentID: parent.ID, Attempt: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), retry.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ParentID != parent.ID || stored.Attempt != 2 {
		t.Fatalf("retry lineage = parent %q attempt %d", stored.ParentID, stored.Attempt)
	}
}
