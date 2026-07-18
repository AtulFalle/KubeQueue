package kubernetes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/informers"
	clientset "k8s.io/client-go/kubernetes"
	batchlisters "k8s.io/client-go/listers/batch/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
)

const jobIDLabel = "kubequeue.io/job-id"

type Client struct {
	client  clientset.Interface
	changes chan struct{}
	mu      sync.RWMutex
	listers map[string]batchlisters.JobLister
}

func InCluster() (*Client, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("configure in-cluster Kubernetes client: %w", err)
	}
	client, err := clientset.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes clientset: %w", err)
	}
	return New(client), nil
}

func New(client clientset.Interface) *Client {
	return &Client{
		client: client, changes: make(chan struct{}, 1),
		listers: make(map[string]batchlisters.JobLister),
	}
}

func (c *Client) Start(ctx context.Context, namespaces []string) (<-chan struct{}, error) {
	factories := make([]informers.SharedInformerFactory, 0, len(namespaces))
	for _, namespace := range namespaces {
		factory := informers.NewSharedInformerFactoryWithOptions(
			c.client, 0, informers.WithNamespace(namespace),
		)
		jobs := factory.Batch().V1().Jobs()
		handler := cache.ResourceEventHandlerFuncs{
			AddFunc:    func(any) { c.notify() },
			UpdateFunc: func(any, any) { c.notify() },
			DeleteFunc: func(any) { c.notify() },
		}
		if _, err := jobs.Informer().AddEventHandler(handler); err != nil {
			return nil, fmt.Errorf("register Job informer for namespace %s: %w", namespace, err)
		}
		if _, err := factory.Core().V1().Pods().Informer().AddEventHandler(handler); err != nil {
			return nil, fmt.Errorf("register Pod informer for namespace %s: %w", namespace, err)
		}
		c.mu.Lock()
		c.listers[namespace] = jobs.Lister()
		c.mu.Unlock()
		factories = append(factories, factory)
	}
	for _, factory := range factories {
		factory.Start(ctx.Done())
	}
	for _, factory := range factories {
		for resource, synced := range factory.WaitForCacheSync(ctx.Done()) {
			if !synced {
				return nil, fmt.Errorf("synchronize Kubernetes informer cache for %v", resource)
			}
		}
	}
	c.notify()
	return c.changes, nil
}

func (c *Client) ListJobs(_ context.Context, namespace string) ([]batchv1.Job, error) {
	c.mu.RLock()
	lister := c.listers[namespace]
	c.mu.RUnlock()
	if lister == nil {
		return nil, fmt.Errorf("job informer for namespace %s is not started", namespace)
	}
	items, err := lister.Jobs(namespace).List(labels.Everything())
	if err != nil {
		return nil, err
	}
	result := make([]batchv1.Job, 0, len(items))
	for _, item := range items {
		result = append(result, *item.DeepCopy())
	}
	return result, nil
}

func (c *Client) CreateJob(
	ctx context.Context, namespace, id, name string, template json.RawMessage,
) (batchv1.Job, error) {
	var job batchv1.Job
	if err := json.Unmarshal(template, &job); err != nil {
		return batchv1.Job{}, fmt.Errorf("decode Job template: %w", err)
	}
	if job.Spec.Template.Spec.RestartPolicy == "" {
		return batchv1.Job{}, errors.New("job template restartPolicy is required")
	}
	job.TypeMeta = metav1.TypeMeta{APIVersion: "batch/v1", Kind: "Job"}
	job.Namespace = namespace
	job.Name = name
	job.GenerateName = ""
	job.UID = ""
	job.ResourceVersion = ""
	job.Generation = 0
	job.ManagedFields = nil
	job.CreationTimestamp = metav1.Time{}
	job.DeletionTimestamp = nil
	job.DeletionGracePeriodSeconds = nil
	if job.Labels == nil {
		job.Labels = make(map[string]string)
	}
	job.Labels[jobIDLabel] = id
	suspended := true
	job.Spec.Suspend = &suspended
	job.Status = batchv1.JobStatus{}

	created, err := c.client.BatchV1().Jobs(namespace).Create(ctx, &job, metav1.CreateOptions{})
	if err != nil {
		return batchv1.Job{}, err
	}
	return *created, nil
}

func (c *Client) Suspend(ctx context.Context, namespace, name string, suspended bool) error {
	patch, err := json.Marshal(map[string]any{"spec": map[string]any{"suspend": suspended}})
	if err != nil {
		return err
	}
	_, err = c.client.BatchV1().Jobs(namespace).Patch(
		ctx, name, types.MergePatchType, patch, metav1.PatchOptions{},
	)
	return err
}

func (c *Client) DeleteJob(ctx context.Context, namespace, name string) error {
	policy := metav1.DeletePropagationForeground
	return c.client.BatchV1().Jobs(namespace).Delete(
		ctx, name, metav1.DeleteOptions{PropagationPolicy: &policy},
	)
}

func StateOf(job batchv1.Job) (domain.State, string) {
	if job.Spec.Suspend != nil && *job.Spec.Suspend {
		return domain.StatePaused, "Job is suspended"
	}
	for _, condition := range job.Status.Conditions {
		if condition.Status != "True" {
			continue
		}
		switch condition.Type {
		case batchv1.JobComplete:
			return domain.StateCompleted, condition.Message
		case batchv1.JobFailed, batchv1.JobFailureTarget:
			return domain.StateFailed, condition.Message
		case batchv1.JobSuspended:
			return domain.StatePaused, condition.Message
		case batchv1.JobSuccessCriteriaMet:
			return domain.StateRunning, condition.Message
		}
	}
	if job.Status.Active > 0 {
		return domain.StateRunning, "Job has active Pods"
	}
	return domain.StateQueued, "Job is waiting for execution"
}

func Template(job batchv1.Job) json.RawMessage {
	copy := job.DeepCopy()
	copy.TypeMeta = metav1.TypeMeta{APIVersion: "batch/v1", Kind: "Job"}
	copy.Name = ""
	copy.GenerateName = ""
	copy.Namespace = ""
	copy.UID = ""
	copy.ResourceVersion = ""
	copy.Generation = 0
	copy.ManagedFields = nil
	copy.OwnerReferences = nil
	copy.Finalizers = nil
	copy.CreationTimestamp = metav1.Time{}
	copy.DeletionTimestamp = nil
	copy.DeletionGracePeriodSeconds = nil
	copy.Spec.Selector = nil
	copy.Spec.ManualSelector = nil
	removeControllerLabels(copy.Labels)
	removeControllerLabels(copy.Spec.Template.Labels)
	copy.Status = batchv1.JobStatus{}
	encoded, _ := json.Marshal(copy)
	return encoded
}

func removeControllerLabels(labels map[string]string) {
	for _, key := range []string{
		jobIDLabel,
		"batch.kubernetes.io/controller-uid",
		"batch.kubernetes.io/job-name",
		"controller-uid",
		"job-name",
	} {
		delete(labels, key)
	}
}

func JobID(job batchv1.Job) string { return job.Labels[jobIDLabel] }

func IsNotFound(err error) bool { return apierrors.IsNotFound(err) }

func (c *Client) notify() {
	select {
	case c.changes <- struct{}{}:
	default:
	}
}
