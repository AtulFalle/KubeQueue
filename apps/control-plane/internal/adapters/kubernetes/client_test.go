package kubernetes

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
)

func TestCreateJobLabelsAndSuspendsBeforeAdmission(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clientset := fake.NewClientset()
	client := New(clientset)
	created, err := client.CreateJob(
		ctx, "batch", "job-id", "report",
		json.RawMessage(`{
			"spec":{"template":{"spec":{"restartPolicy":"Never","containers":[{"name":"job","image":"busybox"}]}}}
		}`),
	)
	if err != nil {
		t.Fatalf("CreateJob() error = %v", err)
	}
	if created.Labels[jobIDLabel] != "job-id" {
		t.Errorf("management label = %q", created.Labels[jobIDLabel])
	}
	if created.Labels[managedLabel] != "true" {
		t.Errorf("managed label = %q", created.Labels[managedLabel])
	}
	if created.Spec.Suspend == nil || !*created.Spec.Suspend {
		t.Error("created Job must remain suspended until durable admission is recorded")
	}
}

func TestSuspendRejectsReplacementJob(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	suspended := false
	clientset := fake.NewClientset(&batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: "job", Namespace: "default", UID: "replacement-uid", ResourceVersion: "2",
		},
		Spec: batchv1.JobSpec{Suspend: &suspended},
	})
	client := New(clientset)
	err := client.Suspend(ctx, "default", "job", "original-uid", "1", true)
	if err == nil {
		t.Fatal("Suspend() error = nil, want identity conflict")
	}
	stored, err := clientset.BatchV1().Jobs("default").Get(ctx, "job", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if stored.Spec.Suspend == nil || *stored.Spec.Suspend {
		t.Fatal("replacement Job was suspended")
	}
}

func TestIgnoredWorkloadClassification(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		job  batchv1.Job
	}{
		{
			name: "explicit opt out",
			job: batchv1.Job{ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{ignoreAnnotation: "true"},
			}},
		},
		{
			name: "Helm hook",
			job: batchv1.Job{ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{helmHook: "pre-upgrade"},
			}},
		},
		{
			name: "internal workload",
			job: batchv1.Job{ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{internalLabel: "true"},
			}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if !IsIgnored(test.job) {
				t.Fatal("IsIgnored() = false")
			}
			if mode := ManagementModeOf(test.job); mode != domain.ManagementIgnored {
				t.Fatalf("ManagementModeOf() = %s", mode)
			}
		})
	}
}

func TestInformerPublishesChangesAndListsCachedJobs(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	clientset := fake.NewClientset()
	client := New(clientset)
	changes, err := client.Start(ctx, []string{"default"})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	select {
	case <-changes:
	case <-time.After(time.Second):
		t.Fatal("initial informer synchronization was not published")
	}

	suspended := true
	_, err = clientset.BatchV1().Jobs("default").Create(ctx, &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "existing", Namespace: "default"},
		Spec: batchv1.JobSpec{
			Suspend: &suspended,
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				RestartPolicy: corev1.RestartPolicyNever,
			}},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-changes:
	case <-time.After(time.Second):
		t.Fatal("informer did not publish the Job creation")
	}
	jobs, err := client.ListJobs(ctx, "default")
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].Name != "existing" {
		t.Fatalf("ListJobs() = %#v", jobs)
	}
}

func TestStateOf(t *testing.T) {
	t.Parallel()
	suspended := true
	tests := []struct {
		name string
		job  batchv1.Job
		want domain.State
	}{
		{"suspended", batchv1.Job{Spec: batchv1.JobSpec{Suspend: &suspended}}, domain.StatePaused},
		{"active", batchv1.Job{Status: batchv1.JobStatus{Active: 1}}, domain.StateRunning},
		{"complete", batchv1.Job{Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{
			Type: batchv1.JobComplete, Status: corev1.ConditionTrue,
		}}}}, domain.StateCompleted},
		{"failed", batchv1.Job{Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{
			Type: batchv1.JobFailed, Status: corev1.ConditionTrue,
		}}}}, domain.StateFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			state, _ := StateOf(test.job)
			if state != test.want {
				t.Errorf("StateOf() = %s, want %s", state, test.want)
			}
		})
	}
}

func TestTemplateRemovesServerOwnedIdentity(t *testing.T) {
	t.Parallel()
	manual := true
	template := Template(batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: "original", Namespace: "default", UID: "uid",
			Finalizers:      []string{"example/finalizer"},
			OwnerReferences: []metav1.OwnerReference{{Name: "owner"}},
			Labels:          map[string]string{jobIDLabel: "id"},
		},
		Spec: batchv1.JobSpec{
			ManualSelector: &manual,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"batch.kubernetes.io/controller-uid": "uid"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{
					"batch.kubernetes.io/controller-uid": "uid",
					"batch.kubernetes.io/job-name":       "original",
					"team":                               "analytics",
				}},
				Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever},
			},
		},
	})
	var sanitized batchv1.Job
	if err := json.Unmarshal(template, &sanitized); err != nil {
		t.Fatal(err)
	}
	if sanitized.Name != "" || sanitized.Spec.Selector != nil || sanitized.Spec.ManualSelector != nil {
		t.Fatalf("server identity retained in template: %#v", sanitized)
	}
	if len(sanitized.OwnerReferences) != 0 || len(sanitized.Finalizers) != 0 {
		t.Fatal("ownership metadata retained in retry template")
	}
	if sanitized.Spec.Template.Labels["team"] != "analytics" {
		t.Fatal("user labels were removed")
	}
	if _, exists := sanitized.Spec.Template.Labels["batch.kubernetes.io/controller-uid"]; exists {
		t.Fatal("controller label retained in retry template")
	}
}
