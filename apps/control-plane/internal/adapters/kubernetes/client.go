package kubernetes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	authorizationv1 "k8s.io/api/authorization/v1"
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

const (
	jobIDLabel       = "kubequeue.io/job-id"
	managedLabel     = "kubequeue.io/managed"
	ignoreAnnotation = "kubequeue.io/ignore"
	internalLabel    = "kubequeue.io/internal"
	helmHook         = "helm.sh/hook"
)

type Client struct {
	client  clientset.Interface
	changes chan struct{}
	mu      sync.RWMutex
	listers map[string]batchlisters.JobLister
	synced  map[string]bool
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
		synced:  make(map[string]bool),
	}
}

func (c *Client) Start(ctx context.Context, namespaces []string) (<-chan struct{}, error) {
	type namespaceInformer struct {
		namespace string
		factory   informers.SharedInformerFactory
	}
	factories := make([]namespaceInformer, 0, len(namespaces))
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
		c.synced[namespace] = false
		c.mu.Unlock()
		factories = append(factories, namespaceInformer{namespace: namespace, factory: factory})
	}
	for _, informer := range factories {
		informer.factory.Start(ctx.Done())
	}
	for _, informer := range factories {
		go func() {
			status := informer.factory.WaitForCacheSync(ctx.Done())
			for _, ready := range status {
				if !ready {
					return
				}
			}
			c.mu.Lock()
			c.synced[informer.namespace] = true
			c.mu.Unlock()
			c.notify()
		}()
	}
	return c.changes, nil
}

func (c *Client) ListJobs(_ context.Context, namespace string) ([]batchv1.Job, error) {
	c.mu.RLock()
	lister := c.listers[namespace]
	synced := c.synced[namespace]
	c.mu.RUnlock()
	if lister == nil {
		return nil, fmt.Errorf("job informer for namespace %s is not started", namespace)
	}
	if !synced {
		return nil, fmt.Errorf("job informer for namespace %s is not synchronized", namespace)
	}
	var items []*batchv1.Job
	var err error
	if namespace == metav1.NamespaceAll {
		items, err = lister.List(labels.Everything())
	} else {
		items, err = lister.Jobs(namespace).List(labels.Everything())
	}
	if err != nil {
		return nil, err
	}
	result := make([]batchv1.Job, 0, len(items))
	for _, item := range items {
		if IsIgnored(*item) {
			continue
		}
		result = append(result, *item.DeepCopy())
	}
	return result, nil
}

func (c *Client) InformerSynced(namespace string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.synced[namespace]
}

func (c *Client) ListNamespaces(ctx context.Context) ([]string, error) {
	items, err := c.client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	namespaces := make([]string, 0, len(items.Items))
	for _, item := range items.Items {
		namespaces = append(namespaces, item.Name)
	}
	return namespaces, nil
}

func (c *Client) CheckJobAccess(ctx context.Context, namespace string) (bool, string, error) {
	for _, verb := range []string{"get", "list", "watch", "create", "patch", "delete"} {
		review, err := c.client.AuthorizationV1().SelfSubjectAccessReviews().Create(
			ctx,
			&authorizationv1.SelfSubjectAccessReview{
				Spec: authorizationv1.SelfSubjectAccessReviewSpec{
					ResourceAttributes: &authorizationv1.ResourceAttributes{
						Namespace: namespace,
						Verb:      verb,
						Group:     "batch",
						Resource:  "jobs",
					},
				},
			},
			metav1.CreateOptions{},
		)
		if err != nil {
			return false, "", fmt.Errorf("check %s Job access: %w", verb, err)
		}
		if !review.Status.Allowed {
			message := strings.TrimSpace(review.Status.Reason)
			if message == "" {
				message = fmt.Sprintf("%s access to Jobs is denied", verb)
			}
			return false, message, nil
		}
	}
	return true, "", nil
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
	job.Labels[managedLabel] = "true"
	suspended := true
	job.Spec.Suspend = &suspended
	job.Status = batchv1.JobStatus{}

	created, err := c.client.BatchV1().Jobs(namespace).Create(ctx, &job, metav1.CreateOptions{})
	if err != nil {
		return batchv1.Job{}, err
	}
	return *created, nil
}

func (c *Client) Suspend(
	ctx context.Context, namespace, name, expectedUID, expectedResourceVersion string, suspended bool,
) error {
	current, err := c.client.BatchV1().Jobs(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if expectedUID != "" && string(current.UID) != expectedUID {
		return fmt.Errorf("job %s/%s UID changed", namespace, name)
	}
	if expectedResourceVersion != "" && current.ResourceVersion != expectedResourceVersion {
		return fmt.Errorf("job %s/%s resource version changed", namespace, name)
	}
	metadata := map[string]any{}
	if expectedResourceVersion != "" {
		metadata["resourceVersion"] = expectedResourceVersion
	}
	patch, err := json.Marshal(map[string]any{
		"metadata": metadata,
		"spec":     map[string]any{"suspend": suspended},
	})
	if err != nil {
		return err
	}
	_, err = c.client.BatchV1().Jobs(namespace).Patch(
		ctx, name, types.MergePatchType, patch, metav1.PatchOptions{},
	)
	return err
}

func (c *Client) DeleteJob(
	ctx context.Context, namespace, name, expectedUID, expectedResourceVersion string,
) error {
	policy := metav1.DeletePropagationForeground
	preconditions := &metav1.Preconditions{}
	if expectedUID != "" {
		uid := types.UID(expectedUID)
		preconditions.UID = &uid
	}
	if expectedResourceVersion != "" {
		preconditions.ResourceVersion = &expectedResourceVersion
	}
	return c.client.BatchV1().Jobs(namespace).Delete(
		ctx, name, metav1.DeleteOptions{
			PropagationPolicy: &policy,
			Preconditions:     preconditions,
		},
	)
}

func StateOf(job batchv1.Job) (domain.State, string) {
	state, _, message := ObservationOf(job)
	return state, message
}

func ObservationOf(job batchv1.Job) (domain.State, string, string) {
	if job.Spec.Suspend != nil && *job.Spec.Suspend {
		return domain.StatePaused, "Suspended", "Job is suspended"
	}
	for _, condition := range job.Status.Conditions {
		if condition.Status != "True" {
			continue
		}
		switch condition.Type {
		case batchv1.JobComplete:
			return domain.StateCompleted, condition.Reason, condition.Message
		case batchv1.JobFailed, batchv1.JobFailureTarget:
			return domain.StateFailed, condition.Reason, condition.Message
		case batchv1.JobSuspended:
			return domain.StatePaused, condition.Reason, condition.Message
		case batchv1.JobSuccessCriteriaMet:
			return domain.StateRunning, condition.Reason, condition.Message
		}
	}
	if job.Status.Active > 0 {
		return domain.StateRunning, "Active", "Job has active Pods"
	}
	return domain.StateQueued, "Waiting", "Job is waiting for execution"
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
		managedLabel,
		"batch.kubernetes.io/controller-uid",
		"batch.kubernetes.io/job-name",
		"controller-uid",
		"job-name",
	} {
		delete(labels, key)
	}
}

func JobID(job batchv1.Job) string { return job.Labels[jobIDLabel] }

func ManagementModeOf(job batchv1.Job) domain.ManagementMode {
	if IsIgnored(job) {
		return domain.ManagementIgnored
	}
	if strings.EqualFold(strings.TrimSpace(job.Labels[managedLabel]), "true") {
		return domain.ManagementManaged
	}
	return domain.ManagementObserved
}

func IsIgnored(job batchv1.Job) bool {
	return strings.EqualFold(strings.TrimSpace(job.Annotations[ignoreAnnotation]), "true") ||
		strings.TrimSpace(job.Annotations[helmHook]) != "" ||
		strings.EqualFold(strings.TrimSpace(job.Labels[internalLabel]), "true")
}

func IsNotFound(err error) bool { return apierrors.IsNotFound(err) }

func ClassifyError(err error) (string, string) {
	switch {
	case apierrors.IsForbidden(err), apierrors.IsUnauthorized(err):
		return "KUBERNETES_AUTHORIZATION_FAILED",
			"Grant the worker the required Job permissions for the namespace"
	case apierrors.IsConflict(err):
		return "KUBERNETES_VERSION_CONFLICT",
			"Wait for the latest Kubernetes observation and retry the action"
	case apierrors.IsInvalid(err), apierrors.IsBadRequest(err):
		return "KUBERNETES_REQUEST_INVALID",
			"Correct the Job template or requested lifecycle operation"
	case apierrors.IsTooManyRequests(err), apierrors.IsTimeout(err), apierrors.IsServerTimeout(err):
		return "KUBERNETES_API_RETRY",
			"Check Kubernetes API availability and throttling"
	default:
		return "RECONCILIATION_FAILED",
			"Check worker logs and Kubernetes status, then retry the operation"
	}
}

func (c *Client) notify() {
	select {
	case c.changes <- struct{}{}:
	default:
	}
}
