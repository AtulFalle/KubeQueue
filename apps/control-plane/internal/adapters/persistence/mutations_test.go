package persistence

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/leadership"
)

func TestUncertainMutationRequiresObservationAcrossRestart(t *testing.T) {
	ctx := t.Context()
	store, err := Open(ctx, "file:test-uncertain-restart?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	job := createMutationTestJob(t, store)
	lease, err := store.AcquireLeadership(ctx, "reconciler", "worker-a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	authority := leadership.Authority{
		Holder: lease.Holder, Generation: lease.Generation, ValidUntil: lease.ExpiresAt,
	}
	request := leadership.MutationRequest{
		Operation: "suspend", JobID: job.ID,
		AttemptIdentity: "attempt:1", RequestIdentity: "version:1",
	}

	started, err := store.BeginMutation(ctx, request, authority)
	if err != nil {
		t.Fatal(err)
	}
	if started.State != leadership.MutationInFlight || started.AttemptID == "" {
		t.Fatalf("started mutation = %#v", started)
	}

	recovered, err := store.BeginMutation(ctx, request, authority)
	if !errors.Is(err, leadership.ErrObservationRequired) {
		t.Fatalf("restart BeginMutation() error = %v", err)
	}
	if recovered.State != leadership.MutationObservationRequired {
		t.Fatalf("recovered mutation = %#v", recovered)
	}
	if _, err := store.BeginMutation(ctx, request, authority); !errors.Is(
		err, leadership.ErrObservationRequired,
	) {
		t.Fatalf("repeated BeginMutation() error = %v", err)
	}

	ready, err := store.ObserveMutation(
		ctx, request, authority, leadership.ObservationEffectAbsent,
	)
	if err != nil {
		t.Fatal(err)
	}
	if ready.State != leadership.MutationReady || ready.ObservedAt == nil {
		t.Fatalf("observed mutation = %#v", ready)
	}
	retried, err := store.BeginMutation(ctx, request, authority)
	if err != nil {
		t.Fatal(err)
	}
	if retried.State != leadership.MutationInFlight ||
		retried.AttemptID == started.AttemptID {
		t.Fatalf("retried mutation = %#v", retried)
	}
}

func TestUncertainMutationSurvivesLeadershipFailover(t *testing.T) {
	ctx := t.Context()
	store, err := Open(ctx, "file:test-uncertain-failover?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	job := createMutationTestJob(t, store)
	lease, err := store.AcquireLeadership(ctx, "reconciler", "worker-a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	first := leadership.Authority{
		Holder: lease.Holder, Generation: lease.Generation, ValidUntil: lease.ExpiresAt,
	}
	request := leadership.MutationRequest{
		Operation: "delete", JobID: job.ID,
		AttemptIdentity: "attempt:1", RequestIdentity: "version:2",
	}
	started, err := store.BeginMutation(ctx, request, first)
	if err != nil {
		t.Fatal(err)
	}
	uncertain, err := store.CompleteMutation(
		ctx, request, started.Generation, leadership.OutcomeUncertain, strings.Repeat("X", 200),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(uncertain.ErrorClass) != 64 {
		t.Fatalf("bounded error class length = %d", len(uncertain.ErrorClass))
	}
	if _, err := store.db.ExecContext(
		ctx, `UPDATE leadership_leases SET expires_at=? WHERE name='reconciler'`,
		time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano),
	); err != nil {
		t.Fatal(err)
	}
	successorLease, err := store.AcquireLeadership(ctx, "reconciler", "worker-b", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	successor := leadership.Authority{
		Holder: successorLease.Holder, Generation: successorLease.Generation,
		ValidUntil: successorLease.ExpiresAt,
	}

	if _, err := store.BeginMutation(ctx, request, successor); !errors.Is(
		err, leadership.ErrObservationRequired,
	) {
		t.Fatalf("successor BeginMutation() error = %v", err)
	}
	applied, err := store.ObserveMutation(
		ctx, request, successor, leadership.ObservationEffectPresent,
	)
	if err != nil {
		t.Fatal(err)
	}
	if applied.State != leadership.MutationSucceeded ||
		applied.Generation != successor.Generation {
		t.Fatalf("successor observation = %#v", applied)
	}
}

func createMutationTestJob(t *testing.T, store *Store) domain.Job {
	t.Helper()
	job, err := store.Create(t.Context(), domain.CreateJob{
		Name: "mutation-test", Namespace: "default",
		Template: json.RawMessage(`{
			"spec":{"template":{"spec":{"restartPolicy":"Never","containers":[{"name":"job","image":"busybox"}]}}}
		}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return job
}
