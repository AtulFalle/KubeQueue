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
	if _, err := store.SetObserved(context.Background(), job.ID, domain.StateFailed, "uid"); err != nil {
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
