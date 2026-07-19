package persistence

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain/policyquota"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/leadership"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/ports"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/scheduler"
)

func testRuntimeAdmission(
	t *testing.T,
	store *Store,
	installationID domain.InstallationID,
	projectID domain.ProjectID,
) {
	t.Helper()
	rules := completePolicyRules()
	one := uint64(1)
	rules.Quotas.Global.MaxConcurrent = &one
	rules.Quotas.Project.MaxConcurrent = &one
	rules.Quotas.Namespace.MaxConcurrent = &one
	installationPolicy := policyquota.Policy{
		Ref: policyquota.PolicyRef{
			ID: "installation_policy_" + string(installationID), Version: 2,
			Scope: policyquota.Scope{Kind: policyquota.ScopeInstallation},
		},
		Rules: rules,
	}
	if err := store.CompareAndSetPolicy(
		t.Context(), installationID, 1, installationPolicy,
	); err != nil {
		t.Fatal(err)
	}
	target := policyquota.Scope{
		Kind: policyquota.ScopeNamespace, Project: string(projectID), Namespace: "runtime",
	}
	policies, err := store.PolicyHierarchy(t.Context(), installationID, target)
	if err != nil {
		t.Fatal(err)
	}
	effective, err := policyquota.Compose(policies...)
	if err != nil {
		t.Fatal(err)
	}
	for index := range 2 {
		result, err := store.CreateJobWithQuota(t.Context(), ports.QuotaSubmission{
			InstallationID: installationID, Target: target, Policy: effective,
			IdempotencyKey: fmt.Sprintf("runtime-%s-%d", projectID, index),
			Job: domain.CreateJob{
				Name: fmt.Sprintf("runtime-%d", index), Namespace: target.Namespace,
				Template: validJobTemplate, ProjectID: projectID,
				SubmissionSource: domain.SubmissionSourceLegacyCompatibility,
			},
			Demand: policyquota.Usage{
				Global:    policyquota.Counters{Queued: 1, Retained: 1},
				Project:   policyquota.Counters{Queued: 1, Retained: 1},
				Namespace: policyquota.Counters{Queued: 1, Retained: 1},
			},
		})
		if err != nil || !result.Decision.Accepted {
			t.Fatalf("create runtime Job %d = %#v, %v", index, result, err)
		}
	}
	manager, err := leadership.NewManager(
		store, "reconciler", "runtime-worker-a", time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.TryAcquire(t.Context()); err != nil {
		t.Fatal(err)
	}
	authority, err := manager.Authority(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	projects, err := store.SchedulingCandidates(t.Context(), 10, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || len(projects[0].Candidates) != 2 {
		t.Fatalf("runtime candidates = %#v", projects)
	}
	fairness, err := store.FairnessState(
		t.Context(), installationID, []domain.ProjectID{projectID},
	)
	if err != nil {
		t.Fatal(err)
	}
	firstRequest := runtimeAdmissionRequest(
		t, authority, effective, fairness, projects[0], projects[0].Candidates,
	)
	first, err := store.CommitRuntimeAdmission(t.Context(), firstRequest)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Quota.Accepted || first.Quota.Rejection != nil {
		t.Fatalf("first runtime admission = %#v", first)
	}
	storedDecision, err := store.AdmissionDecision(
		t.Context(), installationID, firstRequest.Decision.ID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if storedDecision.Policy != effective.Applied[len(effective.Applied)-1] ||
		storedDecision.Scheduling.Basis.ProjectWeight != projects[0].Weight {
		t.Fatalf("stored runtime attribution = %#v", storedDecision)
	}

	projects, err = store.SchedulingCandidates(t.Context(), 10, 10)
	if err != nil {
		t.Fatal(err)
	}
	fairness = first.Fairness
	secondRequest := runtimeAdmissionRequest(
		t, authority, effective, fairness, projects[0], projects[0].Candidates,
	)
	second, err := store.CommitRuntimeAdmission(t.Context(), secondRequest)
	if err != nil {
		t.Fatal(err)
	}
	if second.Quota.Rejection == nil ||
		second.Quota.Rejection.Reason != policyquota.ReasonGlobalConcurrency {
		t.Fatalf("second runtime admission = %#v", second)
	}
	rejected, err := store.Get(t.Context(), secondRequest.Decision.Scheduling.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if rejected.DesiredState != domain.StateQueued ||
		rejected.LastErrorCode != string(policyquota.ReasonGlobalConcurrency) {
		t.Fatalf("quota-rejected Job = %#v", rejected)
	}

	if _, err := store.db.ExecContext(t.Context(), store.bind(
		`UPDATE leadership_leases SET expires_at=? WHERE name='reconciler'`,
	), time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	successor, err := leadership.NewManager(
		store, "reconciler", "runtime-worker-b", time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := successor.TryAcquire(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := store.AbandonRuntimeAdmission(
		t.Context(), authority, installationID,
		firstRequest.Decision.Scheduling.JobID, "stale worker",
	); !errors.Is(err, leadership.ErrStaleGeneration) &&
		!errors.Is(err, leadership.ErrNotLeaseHolder) {
		t.Fatalf("stale worker abandon error = %v", err)
	}
	successorAuthority, err := successor.Authority(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(t.Context(), store.bind(
		`UPDATE jobs SET next_reconcile_at=NULL WHERE id=?`,
	), secondRequest.Decision.Scheduling.JobID); err != nil {
		t.Fatal(err)
	}
	secondRequest.Authority = successorAuthority
	secondRequest.ExpectedFairnessVersion--
	secondRequest.Decision.ID += "-stale"
	if _, err := store.CommitRuntimeAdmission(
		t.Context(), secondRequest,
	); !errors.Is(err, ports.ErrConflict) {
		t.Fatalf("stale fairness commit error = %v, want conflict", err)
	}
}

func runtimeAdmissionRequest(
	t *testing.T,
	authority leadership.Authority,
	effective policyquota.EffectivePolicy,
	fairness ports.FairnessState,
	project ports.SchedulingProject,
	candidates []ports.SchedulingCandidate,
) ports.RuntimeAdmissionRequest {
	t.Helper()
	jobs := make([]scheduler.Job, 0, len(candidates))
	for _, candidate := range candidates {
		jobs = append(jobs, scheduler.Job{
			ID: candidate.Job.ID, Priority: int64(candidate.Job.Priority),
			Age: candidate.Age, Eligible: true, Lane: candidate.Lane,
			EmergencyRequested:     candidate.EmergencyRequested,
			EmergencyAuthorized:    candidate.EmergencyAuthorized,
			EmergencyAuthorization: candidate.EmergencyAuthorization,
		})
	}
	outcome, err := scheduler.Select(
		scheduler.Policy{Version: "runtime-test", AgingStep: 1},
		fairness.State,
		[]scheduler.Project{{
			ID: string(project.ProjectID), Weight: project.Weight, Jobs: jobs,
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	ref := effective.Applied[len(effective.Applied)-1]
	outcome.Decision.AppliedPolicyVersion = fmt.Sprintf("%s:%d", ref.ID, ref.Version)
	return ports.RuntimeAdmissionRequest{
		Authority: authority, InstallationID: project.InstallationID,
		ExpectedFairnessVersion: fairness.Version,
		NextFairnessState:       outcome.State,
		Decision: ports.AdmissionDecision{
			ID:             "runtime-decision-" + outcome.Decision.JobID,
			InstallationID: project.InstallationID, Policy: ref,
			Scheduling: *outcome.Decision, DecidedBy: authority.Holder,
		},
		Policy: effective, ClaimTTL: time.Minute, RejectionRetry: time.Second,
	}
}
