package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	kube "github.com/AtulFalle/KubeQueue/apps/control-plane/internal/adapters/kubernetes"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/ports"
	"github.com/google/uuid"
)

type Reconciler struct {
	repository        ports.Repository
	kubernetes        *kube.Client
	namespaces        []string
	globalLimit       int
	namespaceLimit    int
	reconcileInterval time.Duration
	leaseHolder       string
}

func New(repository ports.Repository, client *kube.Client) *Reconciler {
	return &Reconciler{
		repository: repository, kubernetes: client, namespaces: namespacesFromEnvironment(),
		globalLimit:       envInt("KUBEQUEUE_GLOBAL_CONCURRENCY", 10),
		namespaceLimit:    envInt("KUBEQUEUE_NAMESPACE_CONCURRENCY", 5),
		reconcileInterval: 2 * time.Second,
		leaseHolder:       uuid.NewString(),
	}
}

func (r *Reconciler) Run(ctx context.Context) error {
	changes, err := r.kubernetes.Start(ctx, r.namespaces)
	if err != nil {
		return fmt.Errorf("start Kubernetes informers: %w", err)
	}
	recovery := time.NewTicker(30 * time.Second)
	defer recovery.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-changes:
			if err := r.Reconcile(ctx); err != nil {
				slog.Error("reconciliation failed", "error", err)
			}
		case <-recovery.C:
			if err := r.Reconcile(ctx); err != nil {
				slog.Error("recovery reconciliation failed", "error", err)
			}
		}
	}
}

func (r *Reconciler) Reconcile(ctx context.Context) error {
	acquired, err := r.repository.AcquireSchedulerLease(
		ctx, r.leaseHolder, 3*r.reconcileInterval,
	)
	if err != nil {
		return fmt.Errorf("acquire reconciliation lease: %w", err)
	}
	if !acquired {
		return nil
	}
	var reconcileErrors []error
	for _, namespace := range r.namespaces {
		if err := r.discover(ctx, namespace); err != nil {
			reconcileErrors = append(
				reconcileErrors, fmt.Errorf("discover namespace %s: %w", namespace, err),
			)
		}
	}
	jobs, err := r.repository.List(ctx, ports.JobFilter{})
	if err != nil {
		return errors.Join(append(reconcileErrors, fmt.Errorf("list jobs: %w", err))...)
	}
	if err := r.applyCommands(ctx, jobs); err != nil {
		reconcileErrors = append(reconcileErrors, err)
	}
	if err := r.schedule(ctx, jobs); err != nil {
		reconcileErrors = append(reconcileErrors, err)
	}
	return errors.Join(reconcileErrors...)
}

func (r *Reconciler) discover(ctx context.Context, namespace string) error {
	jobs, err := r.kubernetes.ListJobs(ctx, namespace)
	if err != nil {
		return err
	}
	seenUIDs := make(map[string]struct{}, len(jobs))
	var discoveryErrors []error
	for _, observed := range jobs {
		uid := string(observed.UID)
		seenUIDs[uid] = struct{}{}
		if kube.IsIgnored(observed) {
			continue
		}
		state, reason, message := kube.ObservationOf(observed)
		mode := kube.ManagementModeOf(observed)
		observedAt := time.Now().UTC()
		id := kube.JobID(observed)
		if id == "" {
			desired := state
			_, err := r.repository.Adopt(ctx, domain.Job{
				Name: observed.Name, Namespace: namespace, Team: observed.Labels["team"],
				DesiredState: desired, ObservedState: state, KubernetesUID: uid,
				ManagementMode: mode, SyncStatus: domain.SyncStatusSynced,
				ResourceVersion: observed.ResourceVersion, LastSeenAt: &observedAt,
				ObservedAt: &observedAt, ObservedReason: reason, ObservedMessage: message,
				Template: kube.Template(observed), Attempt: 1,
			})
			if err != nil {
				discoveryErrors = append(
					discoveryErrors, fmt.Errorf("adopt Job %s/%s: %w", namespace, observed.Name, err),
				)
			}
			continue
		}
		current, err := r.repository.Get(ctx, id)
		if errors.Is(err, ports.ErrNotFound) {
			_, err = r.repository.Adopt(ctx, domain.Job{
				Name: observed.Name, Namespace: namespace,
				Team: observed.Labels["team"], DesiredState: state, ObservedState: state,
				KubernetesUID: uid, ManagementMode: domain.ManagementConflicted,
				SyncStatus: domain.SyncStatusConflicted, ResourceVersion: observed.ResourceVersion,
				LastSeenAt: &observedAt, ObservedAt: &observedAt,
				ObservedReason:  "UnknownDurableID",
				ObservedMessage: "Job claims a KubeQueue ID that does not exist",
				Template:        kube.Template(observed), Attempt: 1,
			})
		} else if err == nil {
			if current.Namespace != namespace || current.Name != observed.Name ||
				(current.KubernetesUID != "" && current.KubernetesUID != uid) {
				_, err = r.repository.SetObserved(ctx, id, domain.Observation{
					State: current.ObservedState, ResourceVersion: observed.ResourceVersion,
					ExpectedResourceVersion: current.ResourceVersion,
					Reason:                  "IdentityConflict",
					Message:                 "Kubernetes identity does not match the durable Job association",
					ObservedAt:              observedAt, ManagementMode: domain.ManagementConflicted,
					SyncStatus: domain.SyncStatusConflicted,
				})
			} else {
				if current.ManagementMode == domain.ManagementManaged {
					mode = domain.ManagementManaged
				}
				_, err = r.repository.SetObserved(ctx, id, domain.Observation{
					State: state, KubernetesUID: uid, ResourceVersion: observed.ResourceVersion,
					ExpectedResourceVersion: current.ResourceVersion, Reason: reason, Message: message,
					ObservedAt: observedAt, ManagementMode: mode,
				})
			}
		}
		if err != nil {
			jobErr := fmt.Errorf("observe Job %s/%s: %w", namespace, observed.Name, err)
			if current.ID != "" {
				jobErr = r.recordJobError(ctx, current, jobErr)
			}
			discoveryErrors = append(discoveryErrors, jobErr)
		}
	}
	storedJobs, err := r.repository.List(ctx, ports.JobFilter{Namespace: namespace})
	if err != nil {
		return errors.Join(append(discoveryErrors, err)...)
	}
	for _, stored := range storedJobs {
		if stored.KubernetesUID == "" || stored.Terminal() {
			continue
		}
		if _, exists := seenUIDs[stored.KubernetesUID]; exists {
			continue
		}
		if _, err := r.repository.MarkMissing(
			ctx, stored.ID, stored.KubernetesUID, stored.ResourceVersion, time.Now().UTC(),
		); err != nil {
			discoveryErrors = append(
				discoveryErrors,
				r.recordJobError(ctx, stored, fmt.Errorf("mark Job %s missing: %w", stored.ID, err)),
			)
		}
	}
	return errors.Join(discoveryErrors...)
}

func (r *Reconciler) applyCommands(ctx context.Context, jobs []domain.Job) error {
	var commandErrors []error
	for _, job := range jobs {
		if job.KubernetesUID == "" || job.ManagementMode != domain.ManagementManaged ||
			retryPending(job) || job.SyncStatus == domain.SyncStatusMissing ||
			job.SyncStatus == domain.SyncStatusOutOfScope ||
			job.SyncStatus == domain.SyncStatusConflicted {
			continue
		}
		name := templateName(job)
		if name == "" {
			name = job.Name
		}
		switch job.DesiredState {
		case domain.StateCreated, domain.StateQueued, domain.StateRunning,
			domain.StateCompleted, domain.StateFailed:
			// These states require no direct command against an existing Job.
		case domain.StatePaused:
			if job.ObservedState != domain.StatePaused && !job.Terminal() {
				if err := r.kubernetes.Suspend(
					ctx, job.Namespace, name, job.KubernetesUID, job.ResourceVersion, true,
				); err != nil {
					commandErrors = append(
						commandErrors,
						r.recordJobError(ctx, job, fmt.Errorf("pause Job %s: %w", job.ID, err)),
					)
				}
			}
		case domain.StateCancelled:
			if job.ObservedState != domain.StateCancelled {
				if err := r.kubernetes.DeleteJob(
					ctx, job.Namespace, name, job.KubernetesUID, job.ResourceVersion,
				); err != nil &&
					!kube.IsNotFound(err) {
					commandErrors = append(
						commandErrors,
						r.recordJobError(ctx, job, fmt.Errorf("terminate Job %s: %w", job.ID, err)),
					)
					continue
				}
				if _, err := r.repository.SetObserved(ctx, job.ID, domain.Observation{
					State: domain.StateCancelled, KubernetesUID: job.KubernetesUID,
					ResourceVersion:         job.ResourceVersion,
					ExpectedResourceVersion: job.ResourceVersion,
					Reason:                  "Terminated", Message: "Kubernetes Job was deleted",
					ObservedAt: time.Now().UTC(), ManagementMode: domain.ManagementManaged,
					SyncStatus: domain.SyncStatusSynced,
				}); err != nil {
					commandErrors = append(
						commandErrors,
						r.recordJobError(ctx, job, fmt.Errorf("record Job %s termination: %w", job.ID, err)),
					)
				}
			}
		}
	}
	return errors.Join(commandErrors...)
}

func (r *Reconciler) schedule(ctx context.Context, jobs []domain.Job) error {
	globalRunning := 0
	byNamespace := make(map[string]int)
	for _, job := range jobs {
		if job.ObservedState == domain.StateRunning && countsTowardConcurrency(job) {
			globalRunning++
			byNamespace[job.Namespace]++
		}
	}
	available := r.globalLimit - globalRunning
	if available <= 0 {
		return nil
	}
	eligible, err := r.repository.ClaimEligible(
		ctx, r.leaseHolder, len(jobs)+available, time.Minute,
	)
	if err != nil {
		return err
	}
	var admissionErrors []error
	for index, job := range eligible {
		if globalRunning >= r.globalLimit {
			for _, unprocessed := range eligible[index:] {
				if err := r.repository.ReleaseSchedulerClaim(
					ctx, unprocessed.ID, r.leaseHolder,
				); err != nil {
					admissionErrors = append(
						admissionErrors,
						fmt.Errorf("release scheduler claim for Job %s: %w", unprocessed.ID, err),
					)
				}
			}
			break
		}
		if byNamespace[job.Namespace] >= r.namespaceLimit {
			if err := r.repository.ReleaseSchedulerClaim(ctx, job.ID, r.leaseHolder); err != nil {
				admissionErrors = append(
					admissionErrors,
					fmt.Errorf("release scheduler claim for Job %s: %w", job.ID, err),
				)
			}
			continue
		}
		admitErr := r.admit(ctx, job)
		releaseErr := r.repository.ReleaseSchedulerClaim(ctx, job.ID, r.leaseHolder)
		if admitErr != nil {
			admissionErrors = append(
				admissionErrors,
				r.recordJobError(ctx, job, fmt.Errorf("admit Job %s: %w", job.ID, admitErr)),
			)
		}
		if releaseErr != nil {
			admissionErrors = append(
				admissionErrors,
				fmt.Errorf("release scheduler claim for Job %s: %w", job.ID, releaseErr),
			)
		}
		if admitErr != nil {
			continue
		}
		globalRunning++
		byNamespace[job.Namespace]++
	}
	return errors.Join(admissionErrors...)
}

func (r *Reconciler) admit(ctx context.Context, job domain.Job) error {
	if job.KubernetesUID == "" {
		created, err := r.kubernetes.CreateJob(ctx, job.Namespace, job.ID, job.Name, job.Template)
		if err != nil {
			return err
		}
		if _, err := r.repository.SetObserved(ctx, job.ID, domain.Observation{
			State: domain.StatePaused, KubernetesUID: string(created.UID),
			ResourceVersion:         created.ResourceVersion,
			ExpectedResourceVersion: job.ResourceVersion,
			Reason:                  "CreatedSuspended", Message: "Job was created suspended before admission",
			ObservedAt: time.Now().UTC(), ManagementMode: domain.ManagementManaged,
			SyncStatus: domain.SyncStatusPending,
		}); err != nil {
			return err
		}
		if err := r.kubernetes.Suspend(
			ctx, job.Namespace, created.Name, string(created.UID), created.ResourceVersion, false,
		); err != nil {
			return err
		}
		return nil
	}
	name := templateName(job)
	if name == "" {
		name = job.Name
	}
	if err := r.kubernetes.Suspend(
		ctx, job.Namespace, name, job.KubernetesUID, job.ResourceVersion, false,
	); err != nil {
		return err
	}
	return nil
}

func (r *Reconciler) recordJobError(ctx context.Context, job domain.Job, reconcileErr error) error {
	if job.ID == "" {
		return reconcileErr
	}
	exponent := job.ReconcileRetries
	if exponent > 5 {
		exponent = 5
	}
	delay := 2 * time.Second * time.Duration(1<<exponent)
	if delay > time.Minute {
		delay = time.Minute
	}
	if err := r.repository.RecordReconcileError(
		ctx, job.ID, job.ResourceVersion, reconcileErr.Error(), time.Now().UTC().Add(delay),
	); err != nil {
		return errors.Join(reconcileErr, fmt.Errorf("record reconciliation error: %w", err))
	}
	return reconcileErr
}

func retryPending(job domain.Job) bool {
	return job.NextReconcileAt != nil && job.NextReconcileAt.After(time.Now().UTC())
}

func countsTowardConcurrency(job domain.Job) bool {
	if job.ManagementMode != domain.ManagementManaged {
		return false
	}
	switch job.SyncStatus {
	case domain.SyncStatusMissing, domain.SyncStatusStale, domain.SyncStatusOutOfScope,
		domain.SyncStatusConflicted:
		return false
	case domain.SyncStatusSynced, domain.SyncStatusPending, domain.SyncStatusError:
		return true
	}
	return false
}

func templateName(job domain.Job) string {
	var template struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
	}
	_ = json.Unmarshal(job.Template, &template)
	return template.Metadata.Name
}

func namespacesFromEnvironment() []string {
	value := strings.TrimSpace(env("KUBEQUEUE_WATCH_NAMESPACES", "default"))
	parts := strings.Split(value, ",")
	namespaces := make([]string, 0, len(parts))
	for _, part := range parts {
		if namespace := strings.TrimSpace(part); namespace != "" {
			namespaces = append(namespaces, namespace)
		}
	}
	return namespaces
}

func envInt(name string, fallback int) int {
	value, err := strconv.Atoi(env(name, strconv.Itoa(fallback)))
	if err != nil || value < 1 {
		return fallback
	}
	return value
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
