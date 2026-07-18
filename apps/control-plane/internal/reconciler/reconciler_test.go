package reconciler

import (
	"context"
	"encoding/json"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

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
	if _, err := client.Start(ctx, []string{"default"}); err != nil {
		t.Fatal(err)
	}
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
	if stored.ObservedState != domain.StateRunning {
		t.Fatalf("observed state = %s, want RUNNING", stored.ObservedState)
	}
}

func TestReconcileAdoptsExistingJob(t *testing.T) {
	t.Setenv("KUBEQUEUE_WATCH_NAMESPACES", "default")
	ctx := context.Background()
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
	if _, err := client.Start(ctx, []string{"default"}); err != nil {
		t.Fatal(err)
	}
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
	if len(jobs) != 1 || jobs[0].KubernetesUID != "external-uid" || jobs[0].Team != "analytics" {
		t.Fatalf("adopted jobs = %#v", jobs)
	}
}
