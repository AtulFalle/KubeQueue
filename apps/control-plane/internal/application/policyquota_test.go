package application_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/adapters/persistence"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/application"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain/policyquota"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/ports"
)

func TestPolicyQuotaSubmissionEnforcesPolicyAndReplays(t *testing.T) {
	ctx := legacyContext()
	store, err := persistence.Open(
		ctx, "file:test-policy-quota-application?mode=memory&cache=shared",
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	scope := selectedScope(t, "default")
	if err := store.BackfillCompatibility(ctx, scope); err != nil {
		t.Fatal(err)
	}
	policy := applicationPolicy(1)
	if err := store.CompareAndSetPolicy(ctx, "default", 0, policy); err != nil {
		t.Fatal(err)
	}
	service := application.NewPolicyQuotaService(store)
	target := policyquota.Scope{
		Kind: policyquota.ScopeNamespace, Project: "default", Namespace: "default",
	}
	template := json.RawMessage(`{
		"spec":{"template":{"spec":{"restartPolicy":"Never","containers":[{
			"name":"job","image":"registry.example.com/team/job:v1"
		}]}}}
	}`)
	submission := application.PolicyQuotaSubmission{
		IdempotencyKey: "application-replay",
		Job: domain.CreateJob{
			Name: "policy-job", Namespace: "default", Template: template,
			ProjectID: "default", NamespaceBindingID: "default__default",
			CreatorPrincipalID: "legacy_admin", SubmissionSource: domain.SubmissionSourceAPI,
		},
	}
	created, err := service.Submit(ctx, "default", target, submission)
	if err != nil {
		t.Fatal(err)
	}
	if created.Priority != 4 {
		t.Fatalf("default priority = %d, want 4", created.Priority)
	}
	replayed, err := service.Submit(ctx, "default", target, submission)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.ID != created.ID {
		t.Fatalf("replayed Job = %q, want %q", replayed.ID, created.ID)
	}
	conflict := submission
	conflict.Job.Name = "changed-policy-job"
	_, err = service.Submit(ctx, "default", target, conflict)
	var idempotencyRejection *application.PolicyQuotaRejection
	if !errors.As(err, &idempotencyRejection) ||
		idempotencyRejection.Detail.Reason != policyquota.ReasonIdempotencyConflict {
		t.Fatalf("idempotency rejection = %#v, error %v", idempotencyRejection, err)
	}

	submission.IdempotencyKey = "over-capacity"
	submission.Job.Name = "second-policy-job"
	_, err = service.Submit(ctx, "default", target, submission)
	var rejection *application.PolicyQuotaRejection
	if !errors.As(err, &rejection) ||
		rejection.Detail.Reason != policyquota.ReasonGlobalQueued ||
		rejection.Detail.Current != 1 || rejection.Detail.Limit != 1 {
		t.Fatalf("capacity rejection = %#v, error %v", rejection, err)
	}
	jobs, err := store.List(ctx, ports.JobFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("rejected submissions persisted %d Jobs, want 1", len(jobs))
	}

	admitted, err := service.Admit(ctx, "default", target, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !admitted.Accepted || admitted.Replay ||
		admitted.Usage.Global.Queued != 0 || admitted.Usage.Global.Concurrent != 1 {
		t.Fatalf("admission decision = %#v", admitted)
	}
	admissionReplay, err := service.Admit(ctx, "default", target, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !admissionReplay.Replay {
		t.Fatalf("admission replay = %#v", admissionReplay)
	}
	if _, err := store.SetObserved(ctx, created.ID, domain.Observation{
		State: domain.StateCompleted,
	}); err != nil {
		t.Fatal(err)
	}
	usage, err := store.QuotaUsage(ctx, "default", target)
	if err != nil {
		t.Fatal(err)
	}
	if usage.Global.Concurrent != 0 || usage.Global.Retained != 1 {
		t.Fatalf("terminal usage = %#v", usage)
	}
	if err := store.Archive(ctx, created.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	usage, err = store.QuotaUsage(ctx, "default", target)
	if err != nil {
		t.Fatal(err)
	}
	if usage != (policyquota.Usage{}) {
		t.Fatalf("archived usage = %#v", usage)
	}
}

func TestPolicyQuotaSubmissionRejectsPriorityDelayAndRegistry(t *testing.T) {
	ctx := legacyContext()
	store, err := persistence.Open(
		ctx, "file:test-policy-admission-rules?mode=memory&cache=shared",
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.BackfillCompatibility(ctx, selectedScope(t, "default")); err != nil {
		t.Fatal(err)
	}
	if err := store.CompareAndSetPolicy(ctx, "default", 0, applicationPolicy(10)); err != nil {
		t.Fatal(err)
	}
	service := application.NewPolicyQuotaService(store)
	target := policyquota.Scope{
		Kind: policyquota.ScopeNamespace, Project: "default", Namespace: "default",
	}
	base := domain.CreateJob{
		Name: "policy-check", Namespace: "default",
		Template: json.RawMessage(`{
			"spec":{"template":{"spec":{"restartPolicy":"Never","containers":[{
				"name":"job","image":"registry.example.com/team/job:v1"
			}]}}}
		}`),
		ProjectID: "default", NamespaceBindingID: "default__default",
		CreatorPrincipalID: "legacy_admin", SubmissionSource: domain.SubmissionSourceAPI,
	}
	tests := []struct {
		name       string
		mutate     func(*domain.CreateJob)
		specified  bool
		wantReason policyquota.RejectionReason
	}{
		{
			name: "priority",
			mutate: func(job *domain.CreateJob) {
				job.Priority = 9
			},
			specified:  true,
			wantReason: "policy.priority_above_maximum",
		},
		{
			name: "delay",
			mutate: func(job *domain.CreateJob) {
				value := time.Now().UTC().Add(2 * time.Hour)
				job.ScheduledFor = &value
			},
			wantReason: "policy.delay_horizon_exceeded",
		},
		{
			name: "registry",
			mutate: func(job *domain.CreateJob) {
				job.Template = json.RawMessage(`{
					"spec":{"template":{"spec":{"restartPolicy":"Never","containers":[{
						"name":"job","image":"docker.io/library/busybox:latest"
					}]}}}
				}`)
			},
			wantReason: "policy.image_registry_denied",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := base
			test.mutate(&input)
			_, err := service.Submit(ctx, "default", target, application.PolicyQuotaSubmission{
				Job: input, IdempotencyKey: "reject-" + test.name,
				PrioritySpecified: test.specified,
			})
			var rejection *application.PolicyQuotaRejection
			if !errors.As(err, &rejection) || rejection.Detail.Reason != test.wantReason {
				t.Fatalf("rejection = %#v, error %v", rejection, err)
			}
		})
	}
}

func TestJobsCreateUsesAtomicPolicyQuotaSubmission(t *testing.T) {
	ctx := legacyContext()
	store, err := persistence.Open(
		ctx, "file:test-jobs-policy-quota?mode=memory&cache=shared",
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	scope := selectedScope(t, "default")
	if err := store.BackfillCompatibility(ctx, scope); err != nil {
		t.Fatal(err)
	}
	if err := store.CompareAndSetPolicy(ctx, "default", 0, applicationPolicy(10)); err != nil {
		t.Fatal(err)
	}
	setReadyWorkerStatus(t, store, "default")
	jobs := authorizedJobs(t, store, scope)
	created, err := jobs.Create(ctx, domain.CreateJob{
		Name: "jobs-policy", Namespace: "default",
		Template: json.RawMessage(`{
			"spec":{"template":{"spec":{"restartPolicy":"Never","containers":[{
				"name":"job","image":"registry.example.com/team/job:v1"
			}]}}}
		}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Priority != 4 {
		t.Fatalf("Jobs.Create priority = %d, want policy default 4", created.Priority)
	}
	usage, err := store.QuotaUsage(ctx, "default", policyquota.Scope{
		Kind: policyquota.ScopeNamespace, Project: "default", Namespace: "default",
	})
	if err != nil {
		t.Fatal(err)
	}
	if usage.Global.Queued != 1 || usage.Project.Retained != 1 {
		t.Fatalf("Jobs.Create quota usage = %#v", usage)
	}
}

func applicationPolicy(limit uint64) policyquota.Policy {
	delay := time.Hour
	execution := 24 * time.Hour
	return policyquota.Policy{
		Ref: policyquota.PolicyRef{
			ID: "application-policy", Version: 1,
			Scope: policyquota.Scope{Kind: policyquota.ScopeInstallation},
		},
		Rules: policyquota.Rules{
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
			Priority:             &policyquota.PriorityRange{Min: -5, Max: 5, Default: 4},
			MaxDelayedStart:      &delay,
			MaxExecutionDuration: &execution,
			AllowedImageRegistries: []string{
				"registry.example.com",
			},
			HasImageRegistryAllowlist: true,
		},
	}
}
