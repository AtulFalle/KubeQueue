package reconciler

import (
	"encoding/json"
	"testing"
	"time"

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

	if err := New(store, client).Reconcile(ctx); err != nil {
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

	if err := New(store, client).Reconcile(ctx); err != nil {
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

	if err := New(store, client).Reconcile(ctx); err != nil {
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

	if err := New(store, client).Reconcile(ctx); err == nil {
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
