package persistence

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain/policyquota"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/ports"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/scheduler"
	"github.com/google/uuid"
)

func TestDurableFoundationPersistence(t *testing.T) {
	backends := []struct {
		name string
		url  string
	}{
		{name: "sqlite", url: "file:durable-foundation?mode=memory&cache=shared"},
	}
	if postgresURL := os.Getenv("KUBEQUEUE_TEST_POSTGRES_URL"); postgresURL != "" {
		backends = append(backends, struct {
			name string
			url  string
		}{name: "postgres", url: postgresURL})
	}

	for _, backend := range backends {
		t.Run(backend.name, func(t *testing.T) {
			store, err := Open(t.Context(), backend.url)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			if !store.postgres {
				store.db.SetMaxOpenConns(1)
			}
			installationID, projectID := createDurableFoundationFixture(t, store)

			t.Run("versioned policy hierarchy uses compare and set", func(t *testing.T) {
				testPolicyPersistence(t, store, installationID, projectID)
			})
			t.Run("quota reservation updates and releases usage atomically", func(t *testing.T) {
				testQuotaPersistence(t, store, installationID, projectID)
			})
			t.Run("fairness state and decision commit together", func(t *testing.T) {
				testSchedulingPersistence(t, store, installationID, projectID)
			})
			t.Run("runtime admission is fenced and attributed", func(t *testing.T) {
				runtimeInstallationID, runtimeProjectID :=
					createDurableFoundationFixture(t, store)
				testPolicyPersistence(t, store, runtimeInstallationID, runtimeProjectID)
				testRuntimeAdmission(t, store, runtimeInstallationID, runtimeProjectID)
			})
			t.Run("concurrent submissions respect quota and replay", func(t *testing.T) {
				testAtomicSubmissionConcurrency(t, store, installationID, projectID)
			})
		})
	}
}

func testAtomicSubmissionConcurrency(
	t *testing.T,
	store *Store,
	installationID domain.InstallationID,
	projectID domain.ProjectID,
) {
	t.Helper()
	target := policyquota.Scope{
		Kind: policyquota.ScopeNamespace, Project: string(projectID), Namespace: "concurrent",
	}
	policy := policyquota.EffectivePolicy{
		Applied: []policyquota.PolicyRef{{
			ID: "installation_policy_" + string(installationID), Version: 1,
			Scope: policyquota.Scope{Kind: policyquota.ScopeInstallation},
		}},
		Rules: completePolicyRules(),
	}
	demand := policyquota.Usage{
		Global:    policyquota.Counters{Queued: 1, Retained: 1},
		Project:   policyquota.Counters{Queued: 1, Retained: 1},
		Namespace: policyquota.Counters{Queued: 1, Retained: 1},
	}
	const attempts = 16
	results := make([]ports.QuotaSubmissionResult, attempts)
	errs := make([]error, attempts)
	var wait sync.WaitGroup
	for index := range attempts {
		wait.Add(1)
		go func() {
			defer wait.Done()
			results[index], errs[index] = store.CreateJobWithQuota(t.Context(), ports.QuotaSubmission{
				InstallationID: installationID,
				Target:         target,
				Policy:         policy,
				IdempotencyKey: fmt.Sprintf("concurrent-%s-%d", projectID, index),
				Job: domain.CreateJob{
					Name: fmt.Sprintf("concurrent-%02d", index), Namespace: target.Namespace,
					Template: validJobTemplate, ProjectID: projectID,
					SubmissionSource: domain.SubmissionSourceLegacyCompatibility,
				},
				Demand: demand,
			})
		}()
	}
	wait.Wait()

	accepted := 0
	var replayIndex int
	for index := range attempts {
		if errs[index] != nil {
			t.Fatalf("submission %d error = %v", index, errs[index])
		}
		if results[index].Decision.Accepted {
			accepted++
			replayIndex = index
			continue
		}
		if results[index].Decision.Rejection == nil {
			t.Fatalf("submission %d has no decision: %#v", index, results[index])
		}
	}
	if accepted != 10 {
		t.Fatalf("accepted submissions = %d, want 10", accepted)
	}
	usage, err := store.QuotaUsage(t.Context(), installationID, target)
	if err != nil {
		t.Fatal(err)
	}
	if usage.Global.Queued != 10 || usage.Project.Retained != 10 {
		t.Fatalf("quota usage = %#v", usage)
	}

	replayInput := ports.QuotaSubmission{
		InstallationID: installationID,
		Target:         target,
		Policy:         policy,
		IdempotencyKey: fmt.Sprintf("concurrent-%s-%d", projectID, replayIndex),
		Job: domain.CreateJob{
			Name: fmt.Sprintf("concurrent-%02d", replayIndex), Namespace: target.Namespace,
			Template: validJobTemplate, ProjectID: projectID,
			SubmissionSource: domain.SubmissionSourceLegacyCompatibility,
		},
		Demand: demand,
	}
	replay, err := store.CreateJobWithQuota(t.Context(), replayInput)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Decision.Replay || replay.Job.ID != results[replayIndex].Job.ID {
		t.Fatalf("replay = %#v, want Job %q", replay, results[replayIndex].Job.ID)
	}
}

func testPolicyPersistence(
	t *testing.T,
	store *Store,
	installationID domain.InstallationID,
	projectID domain.ProjectID,
) {
	t.Helper()
	installation := policyquota.Policy{
		Ref: policyquota.PolicyRef{
			ID: "installation_policy_" + string(installationID), Version: 1,
			Scope: policyquota.Scope{Kind: policyquota.ScopeInstallation},
		},
		Rules: completePolicyRules(),
	}
	if err := store.CompareAndSetPolicy(t.Context(), installationID, 0, installation); err != nil {
		t.Fatal(err)
	}
	project := policyquota.Policy{
		Ref: policyquota.PolicyRef{
			ID: "project_policy_" + string(projectID), Version: 1,
			Scope: policyquota.Scope{
				Kind: policyquota.ScopeProject, Project: string(projectID),
			},
		},
	}
	if err := store.CompareAndSetPolicy(t.Context(), installationID, 0, project); err != nil {
		t.Fatal(err)
	}
	project.Ref.Version = 2
	project.Rules.Priority = &policyquota.PriorityRange{Min: -5, Max: 5, Default: 0}
	if err := store.CompareAndSetPolicy(t.Context(), installationID, 1, project); err != nil {
		t.Fatal(err)
	}
	if err := store.CompareAndSetPolicy(t.Context(), installationID, 1, project); !errors.Is(err, ports.ErrConflict) {
		t.Fatalf("stale policy update error = %v, want conflict", err)
	}

	hierarchy, err := store.PolicyHierarchy(t.Context(), installationID, policyquota.Scope{
		Kind: policyquota.ScopeNamespace, Project: string(projectID), Namespace: "batch",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hierarchy) != 2 || hierarchy[0].Ref.Version != 1 ||
		hierarchy[1].Ref.Version != 2 {
		t.Fatalf("policy hierarchy = %#v", hierarchy)
	}
}

func testQuotaPersistence(
	t *testing.T,
	store *Store,
	installationID domain.InstallationID,
	projectID domain.ProjectID,
) {
	t.Helper()
	target := policyquota.Scope{
		Kind: policyquota.ScopeNamespace, Project: string(projectID), Namespace: "batch",
	}
	policy := policyquota.EffectivePolicy{
		Applied: []policyquota.PolicyRef{{
			ID: "installation_policy_" + string(installationID), Version: 1,
			Scope: policyquota.Scope{Kind: policyquota.ScopeInstallation},
		}},
		Rules: completePolicyRules(),
	}
	demand := policyquota.Usage{
		Global:    policyquota.Counters{Queued: 1, Retained: 1},
		Project:   policyquota.Counters{Queued: 1, Retained: 1},
		Namespace: policyquota.Counters{Queued: 1, Retained: 1},
	}
	request := policyquota.ReservationRequest{
		IdempotencyKey: "reservation_" + string(projectID),
		JobID:          "job_" + string(projectID),
		Demand:         demand,
	}
	decision, err := store.ReserveQuota(t.Context(), installationID, target, policy, request)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Accepted || decision.Replay || decision.Usage.Project.Queued != 1 {
		t.Fatalf("reservation decision = %#v", decision)
	}
	replay, err := store.ReserveQuota(t.Context(), installationID, target, policy, request)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Accepted || !replay.Replay {
		t.Fatalf("reservation replay = %#v", replay)
	}
	if _, err := store.MarkQuotaReserved(
		t.Context(), installationID, request.IdempotencyKey,
	); err != nil {
		t.Fatal(err)
	}
	released, usage, err := store.ReleaseQuota(
		t.Context(), installationID, request.IdempotencyKey, policyquota.ReleaseCompleted,
	)
	if err != nil {
		t.Fatal(err)
	}
	if released.State != policyquota.ReservationReleased || usage != (policyquota.Usage{}) {
		t.Fatalf("released reservation = %#v, usage = %#v", released, usage)
	}
	_, replayUsage, err := store.ReleaseQuota(
		t.Context(), installationID, request.IdempotencyKey, policyquota.ReleaseCompleted,
	)
	if err != nil {
		t.Fatal(err)
	}
	if replayUsage != (policyquota.Usage{}) {
		t.Fatalf("idempotent release usage = %#v", replayUsage)
	}
}

func testSchedulingPersistence(
	t *testing.T,
	store *Store,
	installationID domain.InstallationID,
	projectID domain.ProjectID,
) {
	t.Helper()
	configuration, err := store.ProjectScheduling(
		t.Context(), installationID, []domain.ProjectID{projectID},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(configuration) != 1 || configuration[0].Weight != 1 || configuration[0].Version != 1 {
		t.Fatalf("project scheduling = %#v", configuration)
	}
	updated, err := store.CompareAndSetProjectWeight(
		t.Context(), installationID, projectID, 1, 3,
	)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Weight != 3 || updated.Version != 2 {
		t.Fatalf("updated project scheduling = %#v", updated)
	}
	if _, err := store.CompareAndSetProjectWeight(
		t.Context(), installationID, projectID, 1, 4,
	); !errors.Is(err, ports.ErrConflict) {
		t.Fatalf("stale weight update error = %v, want conflict", err)
	}

	state := scheduler.State{
		NextProjectID: string(projectID),
		Deficits:      map[string]uint64{string(projectID): 2},
	}
	record := ports.AdmissionDecision{
		ID:             "decision_" + string(projectID),
		InstallationID: installationID,
		Policy: policyquota.PolicyRef{
			ID: "installation_policy_" + string(installationID), Version: 1,
			Scope: policyquota.Scope{Kind: policyquota.ScopeInstallation},
		},
		Scheduling: scheduler.Decision{
			ProjectID: string(projectID), JobID: "job_" + string(projectID),
			AppliedPolicyVersion: "scheduler-v1",
			Basis: scheduler.Basis{
				Lane: scheduler.LaneStandard, ProjectWeight: 3,
				DeficitBefore: 3, DeficitAfter: 2, BasePriority: 10,
				Age: 1, AgingStep: 2, EffectivePriority: 12,
			},
		},
		QuotaReservationKey: "reservation_" + string(projectID),
		DecidedBy:           "worker-one",
	}
	committed, err := store.CommitSchedulingDecision(
		t.Context(), installationID, 0, state, record,
	)
	if err != nil {
		t.Fatal(err)
	}
	if committed.Version != 1 || committed.State.Deficits[string(projectID)] != 2 {
		t.Fatalf("committed fairness state = %#v", committed)
	}
	record.ID += "_stale"
	if _, err := store.CommitSchedulingDecision(
		t.Context(), installationID, 0, state, record,
	); !errors.Is(err, ports.ErrConflict) {
		t.Fatalf("stale fairness update error = %v, want conflict", err)
	}
	stored, err := store.AdmissionDecision(
		t.Context(), installationID, "decision_"+string(projectID),
	)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Policy.Version != 1 || stored.Scheduling.Basis.DeficitAfter != 2 ||
		stored.DecidedBy != "worker-one" {
		t.Fatalf("stored admission decision = %#v", stored)
	}

	tooMany := make([]domain.ProjectID, ports.MaxSchedulingProjects+1)
	for index := range tooMany {
		tooMany[index] = domain.ProjectID("project")
	}
	if _, err := store.ProjectScheduling(t.Context(), installationID, tooMany); err == nil {
		t.Fatal("unbounded project scheduling read was accepted")
	}
}

func createDurableFoundationFixture(
	t *testing.T,
	store *Store,
) (domain.InstallationID, domain.ProjectID) {
	t.Helper()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	installationID := domain.InstallationID("foundation_" + suffix)
	projectID := domain.ProjectID("project_" + suffix)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(t.Context(), sBind(store,
		`INSERT INTO installations(id,name,created_at) VALUES(?,?,?)`,
	), installationID, "Foundation test", now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(t.Context(), sBind(store,
		`INSERT INTO projects(id,installation_id,name,created_at) VALUES(?,?,?,?)`,
	), projectID, installationID, "Foundation project", now); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		for _, statement := range []string{
			`DELETE FROM admission_decisions WHERE installation_id=?`,
			`DELETE FROM scheduler_project_deficits WHERE installation_id=?`,
			`DELETE FROM scheduler_fairness_state WHERE installation_id=?`,
			`DELETE FROM quota_reservations WHERE installation_id=?`,
			`DELETE FROM quota_usage WHERE installation_id=?`,
			`DELETE FROM policy_scopes WHERE installation_id=?`,
			`DELETE FROM projects WHERE installation_id=?`,
			`DELETE FROM installations WHERE id=?`,
		} {
			_, _ = store.db.ExecContext(ctx, sBind(store, statement), installationID)
		}
	})
	return installationID, projectID
}

func completePolicyRules() policyquota.Rules {
	limit := uint64(10)
	delay := time.Hour
	execution := 24 * time.Hour
	return policyquota.Rules{
		Quotas: policyquota.QuotaLimits{
			Global: policyquota.ScopedLimits{
				MaxConcurrent: &limit, MaxQueued: &limit, MaxRetained: &limit,
			},
			Project: policyquota.ScopedLimits{
				MaxConcurrent: &limit, MaxQueued: &limit, MaxRetained: &limit,
			},
			Namespace: policyquota.ScopedLimits{
				MaxConcurrent: &limit, MaxQueued: &limit, MaxRetained: &limit,
			},
		},
		Priority:             &policyquota.PriorityRange{Min: -10, Max: 10, Default: 0},
		MaxDelayedStart:      &delay,
		MaxExecutionDuration: &execution,
	}
}

func sBind(store *Store, query string) string { return store.bind(query) }
