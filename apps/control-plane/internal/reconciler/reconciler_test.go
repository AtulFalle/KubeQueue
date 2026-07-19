package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	kube "github.com/AtulFalle/KubeQueue/apps/control-plane/internal/adapters/kubernetes"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/adapters/persistence"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain/policyquota"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/leadership"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/ports"
)

func TestReconcileAdmitsJobOnlyAfterCreatingItSuspended(t *testing.T) {
	t.Setenv("KUBEQUEUE_WATCH_NAMESPACES", "default")
	ctx := t.Context()
	store, err := persistence.Open(ctx, "file:test-reconcile-admit?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	clientset := fake.NewClientset()
	client := kube.New(clientset)
	changes, err := client.Start(ctx, []string{"default"})
	if err != nil {
		t.Fatal(err)
	}
	waitForInformerSync(t, changes)
	job, err := store.Create(ctx, domain.CreateJob{
		Name: "report", Namespace: "default",
		Template: json.RawMessage(`{
			"spec":{"template":{"spec":{"restartPolicy":"Never","containers":[{"name":"job","image":"busybox"}]}}}
		}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := New(store, client, selectedScope(t, "default")).Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	created, err := clientset.BatchV1().Jobs("default").Get(ctx, "report", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if created.Spec.Suspend == nil || *created.Spec.Suspend {
		t.Fatal("admitted Job remained suspended")
	}
	stored, err := store.Get(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ObservedState != domain.StatePaused || stored.SyncStatus != domain.SyncStatusPending {
		t.Fatalf("stored admission state = %#v", stored)
	}
}

func TestReconcileCommitsFairQuotaBeforeKubernetesAdmission(t *testing.T) {
	ctx := t.Context()
	scope := selectedScope(t, "default")
	store, err := persistence.Open(
		ctx, "file:test-fair-runtime-admission?mode=memory&cache=shared",
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.BackfillCompatibility(ctx, scope); err != nil {
		t.Fatal(err)
	}
	policy := reconcilerPolicy(2)
	if err := store.CompareAndSetPolicy(ctx, "default", 0, policy); err != nil {
		t.Fatal(err)
	}
	effective, err := policyquota.Compose(policy)
	if err != nil {
		t.Fatal(err)
	}
	target := policyquota.Scope{
		Kind: policyquota.ScopeNamespace, Project: "default", Namespace: "default",
	}
	submission, err := store.CreateJobWithQuota(ctx, ports.QuotaSubmission{
		InstallationID: "default", Target: target, Policy: effective,
		IdempotencyKey: "fair-runtime",
		Job: domain.CreateJob{
			Name: "fair-runtime", Namespace: "default",
			Template: json.RawMessage(`{
				"spec":{"template":{"spec":{"restartPolicy":"Never","containers":[{
					"name":"job","image":"busybox"
				}]}}}
			}`),
			ProjectID: "default", NamespaceBindingID: "default__default",
			CreatorPrincipalID: "legacy_admin",
			SubmissionSource:   domain.SubmissionSourceAPI,
		},
		Demand: policyquota.Usage{
			Global:    policyquota.Counters{Queued: 1, Retained: 1},
			Project:   policyquota.Counters{Queued: 1, Retained: 1},
			Namespace: policyquota.Counters{Queued: 1, Retained: 1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	clientset := fake.NewClientset()
	client := kube.New(clientset)
	changes, err := client.Start(ctx, []string{"default"})
	if err != nil {
		t.Fatal(err)
	}
	waitForInformerSync(t, changes)
	if err := New(store, client, scope).Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := clientset.BatchV1().Jobs("default").Get(
		ctx, "fair-runtime", metav1.GetOptions{},
	); err != nil {
		t.Fatal(err)
	}
	usage, err := store.QuotaUsage(ctx, "default", target)
	if err != nil {
		t.Fatal(err)
	}
	if usage.Global.Concurrent != 1 || usage.Global.Queued != 0 {
		t.Fatalf("runtime quota usage = %#v", usage)
	}
	if submission.Job.ID == "" {
		t.Fatal("atomic submission returned no Job")
	}
}

func TestTerminationRemainsPendingUntilDeletionIsObserved(t *testing.T) {
	ctx := t.Context()
	store, err := persistence.Open(ctx, "file:test-termination-convergence?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	job, err := store.Create(ctx, domain.CreateJob{
		Name: "terminate-me", Namespace: "default",
		Template: json.RawMessage(`{
			"spec":{"template":{"spec":{"restartPolicy":"Never","containers":[{"name":"job","image":"busybox"}]}}}
		}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err = store.SetObserved(ctx, job.ID, domain.Observation{
		State: domain.StateRunning, KubernetesUID: "terminate-uid", ResourceVersion: "1",
		ExpectedResourceVersion: job.ResourceVersion, ManagementMode: domain.ManagementManaged,
		SyncStatus: domain.SyncStatusSynced,
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err = store.SetDesiredState(ctx, job.ID, domain.StateCancelled)
	if err != nil {
		t.Fatal(err)
	}

	clientset := fake.NewClientset(&batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: "terminate-me", Namespace: "default", UID: "terminate-uid", ResourceVersion: "1",
			Labels: map[string]string{
				"kubequeue.io/job-id":  job.ID,
				"kubequeue.io/managed": "true",
			},
		},
		Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
		}}},
		Status: batchv1.JobStatus{Active: 1},
	})
	client := kube.New(clientset)
	changes, err := client.Start(ctx, []string{"default"})
	if err != nil {
		t.Fatal(err)
	}
	waitForInformerReady(t, client, changes, "default")
	reconciler := New(store, client, selectedScope(t, "default"))

	if err := reconciler.leadership.TryAcquire(ctx); err != nil {
		t.Fatal(err)
	}
	authority, err := reconciler.leadership.Authority(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.applyCommands(ctx, []domain.Job{job}, authority); err != nil {
		t.Fatal(err)
	}
	pending, err := store.Get(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !pending.ActionPending || pending.ObservedState == domain.StateCancelled {
		t.Fatalf("termination converged before deletion observation: %#v", pending)
	}

	deadline := time.Now().Add(time.Second)
	for {
		if err := reconciler.discover(ctx, "default"); err != nil {
			t.Fatal(err)
		}
		converged, err := store.Get(ctx, job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !converged.ActionPending && converged.ObservedState == domain.StateCancelled &&
			converged.SyncStatus == domain.SyncStatusSynced {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("termination did not converge after deletion observation: %#v", converged)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestLeadershipLossPreventsKubernetesMutation(t *testing.T) {
	ctx := t.Context()
	store, err := persistence.Open(ctx, "file:test-fenced-mutation?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	job, err := store.Create(ctx, domain.CreateJob{
		Name: "report", Namespace: "default",
		Template: json.RawMessage(`{
			"spec":{"template":{"spec":{"restartPolicy":"Never","containers":[{"name":"job","image":"busybox"}]}}}
		}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err = store.SetObserved(ctx, job.ID, domain.Observation{
		State: domain.StateRunning, KubernetesUID: "report-uid", ResourceVersion: "7",
		ExpectedResourceVersion: job.ResourceVersion,
		ManagementMode:          domain.ManagementManaged, SyncStatus: domain.SyncStatusSynced,
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err = store.SetDesiredState(ctx, job.ID, domain.StatePaused)
	if err != nil {
		t.Fatal(err)
	}
	suspended := false
	clientset := fake.NewClientset(&batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: "report", Namespace: "default", UID: "report-uid", ResourceVersion: "7",
		},
		Spec: batchv1.JobSpec{
			Suspend: &suspended,
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				RestartPolicy: corev1.RestartPolicyNever,
			}},
		},
	})
	client := kube.New(clientset)
	reconciler := New(store, client, selectedScope(t, "default"))
	lease, err := store.AcquireLeadership(ctx, "reconciler", "old-worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	reconciler.leadership = rejectingFence{err: leadership.ErrStaleGeneration}
	authority := leadership.Authority{
		Holder: lease.Holder, Generation: lease.Generation, ValidUntil: lease.ExpiresAt,
	}

	err = reconciler.applyCommands(ctx, []domain.Job{job}, authority)
	if !errors.Is(err, leadership.ErrStaleGeneration) {
		t.Fatalf("applyCommands() error = %v, want stale generation", err)
	}
	actions := clientset.Actions()
	for _, action := range actions {
		if action.Matches("update", "jobs") {
			t.Fatalf("stale leader issued Kubernetes update: %#v", action)
		}
	}
}

func TestRestartDoesNotRepeatUncertainKubernetesMutationWithoutObservation(t *testing.T) {
	ctx := t.Context()
	store, err := persistence.Open(ctx, "file:test-uncertain-write-restart?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	job, err := store.Create(ctx, domain.CreateJob{
		Name: "report", Namespace: "default",
		Template: json.RawMessage(`{
			"spec":{"template":{"spec":{"restartPolicy":"Never","containers":[{"name":"job","image":"busybox"}]}}}
		}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err = store.SetObserved(ctx, job.ID, domain.Observation{
		State: domain.StateRunning, KubernetesUID: "report-uid", ResourceVersion: "7",
		ExpectedResourceVersion: job.ResourceVersion,
		ManagementMode:          domain.ManagementManaged, SyncStatus: domain.SyncStatusSynced,
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err = store.SetDesiredState(ctx, job.ID, domain.StatePaused)
	if err != nil {
		t.Fatal(err)
	}
	suspended := false
	clientset := fake.NewClientset(&batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: "report", Namespace: "default", UID: "report-uid", ResourceVersion: "7",
		},
		Spec: batchv1.JobSpec{
			Suspend: &suspended,
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				RestartPolicy: corev1.RestartPolicyNever,
			}},
		},
	})
	patches := 0
	clientset.PrependReactor("patch", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
		patches++
		return true, nil, io.ErrUnexpectedEOF
	})
	client := kube.New(clientset)
	firstManager, err := leadership.NewManager(store, "reconciler", "worker-a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	first := NewWithLeadership(store, client, selectedScope(t, "default"), firstManager)
	if err := firstManager.TryAcquire(ctx); err != nil {
		t.Fatal(err)
	}
	authority, err := firstManager.Authority(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.applyCommands(ctx, []domain.Job{job}, authority); !errors.Is(
		err, io.ErrUnexpectedEOF,
	) {
		t.Fatalf("first applyCommands() error = %v", err)
	}

	restartedManager, err := leadership.NewManager(store, "reconciler", "worker-a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewWithLeadership(
		store, client, selectedScope(t, "default"), restartedManager,
	)
	if err := restartedManager.TryAcquire(ctx); err != nil {
		t.Fatal(err)
	}
	restartedAuthority, err := restartedManager.Authority(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.applyCommands(
		ctx, []domain.Job{job}, restartedAuthority,
	); err != nil && !errors.Is(err, leadership.ErrObservationRequired) {
		t.Fatalf("restart applyCommands() error = %v", err)
	}
	if patches != 1 {
		t.Fatalf("Kubernetes patch count = %d, want 1 before observation", patches)
	}
}

func TestReconcileAdoptsExistingJob(t *testing.T) {
	t.Setenv("KUBEQUEUE_WATCH_NAMESPACES", "default")
	ctx := t.Context()
	clientset := fake.NewClientset(&batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: "existing", Namespace: "default", UID: types.UID("external-uid"),
			Labels: map[string]string{"team": "analytics"},
		},
		Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
		}}},
		Status: batchv1.JobStatus{Active: 1},
	})
	client := kube.New(clientset)
	changes, err := client.Start(ctx, []string{"default"})
	if err != nil {
		t.Fatal(err)
	}
	waitForInformerSync(t, changes)
	store, err := persistence.Open(ctx, "file:test-reconcile-adopt?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err := New(store, client, selectedScope(t, "default")).Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	jobs, err := store.List(ctx, ports.JobFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].KubernetesUID != "external-uid" ||
		jobs[0].Team != "analytics" || jobs[0].ManagementMode != domain.ManagementObserved {
		t.Fatalf("adopted jobs = %#v", jobs)
	}
}

func TestReconcileDoesNotAdoptHelmHooks(t *testing.T) {
	t.Setenv("KUBEQUEUE_WATCH_NAMESPACES", "default")
	ctx := t.Context()
	clientset := fake.NewClientset(&batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: "migration", Namespace: "default", UID: "hook-uid",
			Annotations: map[string]string{"helm.sh/hook": "pre-upgrade"},
		},
		Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
		}}},
	})
	client := kube.New(clientset)
	changes, err := client.Start(ctx, []string{"default"})
	if err != nil {
		t.Fatal(err)
	}
	waitForInformerSync(t, changes)
	store, err := persistence.Open(ctx, "file:test-ignore-hook?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err := New(store, client, selectedScope(t, "default")).Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	jobs, err := store.List(ctx, ports.JobFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 0 {
		t.Fatalf("adopted ignored Jobs = %#v", jobs)
	}
}

func TestReconcilePreservesCronJobOwnedWorkloadAsObserved(t *testing.T) {
	t.Setenv("KUBEQUEUE_WATCH_NAMESPACES", "default")
	ctx := t.Context()
	controller := true
	clientset := fake.NewClientset(&batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: "scheduled-report", Namespace: "default", UID: "cronjob-child",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "batch/v1", Kind: "CronJob", Name: "reports",
				UID: "cronjob-owner", Controller: &controller,
			}},
		},
		Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
		}}},
	})
	client := kube.New(clientset)
	changes, err := client.Start(ctx, []string{"default"})
	if err != nil {
		t.Fatal(err)
	}
	waitForInformerSync(t, changes)
	store, err := persistence.Open(ctx, "file:test-cronjob-observed?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err := New(store, client, selectedScope(t, "default")).Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	jobs, err := store.List(ctx, ports.JobFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].ManagementMode != domain.ManagementObserved {
		t.Fatalf("CronJob-owned Jobs = %#v", jobs)
	}
}

func TestReconcilePersistsClaimedIdentityConflict(t *testing.T) {
	t.Setenv("KUBEQUEUE_WATCH_NAMESPACES", "default")
	ctx := t.Context()
	store, err := persistence.Open(ctx, "file:test-identity-conflict?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	job, err := store.Create(ctx, domain.CreateJob{
		Name: "expected", Namespace: "default",
		Template: json.RawMessage(`{
			"spec":{"template":{"spec":{
				"restartPolicy":"Never",
				"containers":[{"name":"job","image":"busybox"}]
			}}}
		}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	clientset := fake.NewClientset(&batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: "spoofed", Namespace: "default", UID: "spoofed-uid",
			Labels: map[string]string{
				"kubequeue.io/job-id":  job.ID,
				"kubequeue.io/managed": "true",
			},
		},
		Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
		}}},
	})
	client := kube.New(clientset)
	changes, err := client.Start(ctx, []string{"default"})
	if err != nil {
		t.Fatal(err)
	}
	waitForInformerSync(t, changes)

	if err := New(store, client, selectedScope(t, "default")).Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ManagementMode != domain.ManagementConflicted ||
		stored.SyncStatus != domain.SyncStatusConflicted {
		t.Fatalf("conflicted Job = %#v", stored)
	}
}

func TestReconcileAllModeExcludesConfiguredNamespaces(t *testing.T) {
	ctx := t.Context()
	clientset := fake.NewClientset(
		&batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name: "batch-job", Namespace: "batch", UID: "batch-uid",
			},
			Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				RestartPolicy: corev1.RestartPolicyNever,
			}}},
		},
		&batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name: "system-job", Namespace: "kube-system", UID: "system-uid",
			},
			Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				RestartPolicy: corev1.RestartPolicyNever,
			}}},
		},
	)
	client := kube.New(clientset)
	changes, err := client.Start(ctx, []string{""})
	if err != nil {
		t.Fatal(err)
	}
	waitForInformerSync(t, changes)
	store, err := persistence.Open(ctx, "file:test-all-mode?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	scope, err := domain.NewNamespaceScope(
		domain.WatchModeAll, nil, []string{"kube-system"},
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := New(store, client, scope).Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	jobs, err := store.List(ctx, ports.JobFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].Namespace != "batch" {
		t.Fatalf("all-mode jobs = %#v", jobs)
	}
}

func TestReconcileMarksRemovedNamespaceOutOfScope(t *testing.T) {
	ctx := t.Context()
	store, err := persistence.Open(ctx, "file:test-removed-namespace?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	job, err := store.Create(ctx, domain.CreateJob{
		Name: "old-job", Namespace: "removed",
		Template: json.RawMessage(`{
			"spec":{"template":{"spec":{
				"restartPolicy":"Never",
				"containers":[{"name":"job","image":"busybox"}]
			}}}
		}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	client := kube.New(fake.NewClientset())
	changes, err := client.Start(ctx, []string{"default"})
	if err != nil {
		t.Fatal(err)
	}
	waitForInformerSync(t, changes)

	if err := New(store, client, selectedScope(t, "default")).Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SyncStatus != domain.SyncStatusOutOfScope {
		t.Fatalf("removed namespace sync status = %s", stored.SyncStatus)
	}
	eligible, err := store.Eligible(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(eligible) != 0 {
		t.Fatalf("out-of-scope Job remained eligible: %#v", eligible)
	}
}

func TestRecordStatusReportsSynchronizedAuthorizedNamespace(t *testing.T) {
	ctx := t.Context()
	clientset := fake.NewClientset()
	clientset.PrependReactor(
		"create", "selfsubjectaccessreviews",
		func(ktesting.Action) (bool, runtime.Object, error) {
			return true, &authorizationv1.SelfSubjectAccessReview{
				Status: authorizationv1.SubjectAccessReviewStatus{Allowed: true},
			}, nil
		},
	)
	client := kube.New(clientset)
	changes, err := client.Start(ctx, []string{"default"})
	if err != nil {
		t.Fatal(err)
	}
	waitForInformerReady(t, client, changes, "default")
	store, err := persistence.Open(ctx, "file:test-worker-status-ready?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	reconciler := New(store, client, selectedScope(t, "default"))
	if err := reconciler.leadership.TryAcquire(ctx); err != nil {
		t.Fatal(err)
	}

	if err := reconciler.recordStatus(ctx, nil); err != nil {
		t.Fatal(err)
	}
	status, err := store.WorkerStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != domain.WorkerStateReady || !reconciler.Ready() ||
		len(status.Namespaces) != 1 || !status.Namespaces[0].Authorized ||
		!status.Namespaces[0].InformerSynced {
		t.Fatalf("worker status = %#v", status)
	}
}

func TestRecordStatusKeepsReadinessForIsolatedReconciliationFailure(t *testing.T) {
	ctx := t.Context()
	clientset := fake.NewClientset()
	clientset.PrependReactor(
		"create", "selfsubjectaccessreviews",
		func(ktesting.Action) (bool, runtime.Object, error) {
			return true, &authorizationv1.SelfSubjectAccessReview{
				Status: authorizationv1.SubjectAccessReviewStatus{Allowed: true},
			}, nil
		},
	)
	client := kube.New(clientset)
	changes, err := client.Start(ctx, []string{"default"})
	if err != nil {
		t.Fatal(err)
	}
	waitForInformerReady(t, client, changes, "default")
	store, err := persistence.Open(ctx, "file:test-worker-status-degraded?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	reconciler := New(store, client, selectedScope(t, "default"))
	if err := reconciler.leadership.TryAcquire(ctx); err != nil {
		t.Fatal(err)
	}

	if err := reconciler.recordStatus(ctx, errors.New("one Job failed")); err != nil {
		t.Fatal(err)
	}
	status, err := store.WorkerStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != domain.WorkerStateDegraded || !reconciler.Ready() {
		t.Fatalf("worker status = %#v, ready = %t", status, reconciler.Ready())
	}
}

func TestReconcileContinuesAfterNamespaceFailure(t *testing.T) {
	t.Setenv("KUBEQUEUE_WATCH_NAMESPACES", "healthy,forbidden")
	ctx := t.Context()
	clientset := fake.NewClientset(&batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: "healthy-job", Namespace: "healthy", UID: "healthy-uid",
		},
		Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
		}}},
	})
	clientset.PrependReactor("list", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		if action.GetNamespace() != "forbidden" {
			return false, nil, nil
		}
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Group: "batch", Resource: "jobs"}, "", nil,
		)
	})
	client := kube.New(clientset)
	changes, err := client.Start(ctx, []string{"healthy", "forbidden"})
	if err != nil {
		t.Fatal(err)
	}
	waitForInformerSync(t, changes)
	store, err := persistence.Open(ctx, "file:test-namespace-isolation?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err := New(store, client, selectedScope(t, "healthy", "forbidden")).Reconcile(ctx); err == nil {
		t.Fatal("Reconcile() error = nil, want forbidden namespace error")
	}
	jobs, err := store.List(ctx, ports.JobFilter{Namespace: "healthy"})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].Name != "healthy-job" {
		t.Fatalf("healthy namespace jobs = %#v", jobs)
	}
}

func waitForInformerSync(t *testing.T, changes <-chan struct{}) {
	t.Helper()
	select {
	case <-changes:
	case <-time.After(time.Second):
		t.Fatal("informer cache did not synchronize")
	}
}

func waitForInformerReady(
	t *testing.T, client *kube.Client, changes <-chan struct{}, namespace string,
) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-changes:
		default:
		}
		if client.InformerSynced(namespace) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("informer for namespace %q did not synchronize", namespace)
}

func selectedScope(t *testing.T, namespaces ...string) domain.NamespaceScope {
	t.Helper()
	scope, err := domain.NewNamespaceScope(domain.WatchModeSelected, namespaces, nil)
	if err != nil {
		t.Fatal(err)
	}
	return scope
}

func reconcilerPolicy(limit uint64) policyquota.Policy {
	delay := time.Hour
	execution := 24 * time.Hour
	return policyquota.Policy{
		Ref: policyquota.PolicyRef{
			ID: "reconciler-policy", Version: 1,
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
			Priority:             &policyquota.PriorityRange{Min: -10, Max: 10, Default: 0},
			MaxDelayedStart:      &delay,
			MaxExecutionDuration: &execution,
		},
	}
}

type rejectingFence struct {
	err error
}

func (f rejectingFence) Run(context.Context) error { return nil }
func (f rejectingFence) TryAcquire(context.Context) error {
	return f.err
}
func (f rejectingFence) Authority(context.Context) (leadership.Authority, error) {
	return leadership.Authority{}, f.err
}
func (f rejectingFence) Revalidate(context.Context, leadership.Authority) error {
	return f.err
}
func (f rejectingFence) Snapshot() leadership.Snapshot {
	return leadership.Snapshot{Role: leadership.RoleFollower, Generation: 5}
}
func (f rejectingFence) Holder() string { return "old-worker" }
