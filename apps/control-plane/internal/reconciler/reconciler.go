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
	for _, namespace := range r.namespaces {
		if err := r.discover(ctx, namespace); err != nil {
			return fmt.Errorf("discover namespace %s: %w", namespace, err)
		}
	}
	jobs, err := r.repository.List(ctx, ports.JobFilter{})
	if err != nil {
		return err
	}
	if err := r.applyCommands(ctx, jobs); err != nil {
		return err
	}
	return r.schedule(ctx, jobs)
}

func (r *Reconciler) discover(ctx context.Context, namespace string) error {
	jobs, err := r.kubernetes.ListJobs(ctx, namespace)
	if err != nil {
		return err
	}
	seenUIDs := make(map[string]struct{}, len(jobs))
	for _, observed := range jobs {
		seenUIDs[string(observed.UID)] = struct{}{}
		state, _ := kube.StateOf(observed)
		id := kube.JobID(observed)
		if id == "" {
			desired := state
			if state == domain.StateRunning {
				desired = domain.StateRunning
			}
			_, err := r.repository.Adopt(ctx, domain.Job{
				Name: observed.Name, Namespace: namespace, Team: observed.Labels["team"],
				DesiredState: desired, ObservedState: state, KubernetesUID: string(observed.UID),
				Template: kube.Template(observed), Attempt: 1,
			})
			if err != nil {
				return err
			}
			continue
		}
		current, err := r.repository.Get(ctx, id)
		if errors.Is(err, ports.ErrNotFound) {
			_, err = r.repository.Adopt(ctx, domain.Job{
				ID: id, Name: observed.Name, Namespace: namespace,
				Team: observed.Labels["team"], DesiredState: state, ObservedState: state,
				KubernetesUID: string(observed.UID), Template: kube.Template(observed), Attempt: 1,
			})
		} else if err == nil && (current.ObservedState != state || current.KubernetesUID == "") {
			_, err = r.repository.SetObserved(ctx, id, state, string(observed.UID))
		}
		if err != nil {
			return err
		}
	}
	storedJobs, err := r.repository.List(ctx, ports.JobFilter{Namespace: namespace})
	if err != nil {
		return err
	}
	for _, stored := range storedJobs {
		if stored.KubernetesUID == "" || stored.Terminal() {
			continue
		}
		if _, exists := seenUIDs[stored.KubernetesUID]; exists {
			continue
		}
		if _, err := r.repository.SetObserved(
			ctx, stored.ID, domain.StateCancelled, stored.KubernetesUID,
		); err != nil {
			return err
		}
	}
	return nil
}

func (r *Reconciler) applyCommands(ctx context.Context, jobs []domain.Job) error {
	for _, job := range jobs {
		if job.KubernetesUID == "" {
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
				if err := r.kubernetes.Suspend(ctx, job.Namespace, name, true); err != nil {
					return err
				}
			}
		case domain.StateCancelled:
			if job.ObservedState != domain.StateCancelled {
				if err := r.kubernetes.DeleteJob(ctx, job.Namespace, name); err != nil &&
					!kube.IsNotFound(err) {
					return err
				}
				if _, err := r.repository.SetObserved(ctx, job.ID, domain.StateCancelled, job.KubernetesUID); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (r *Reconciler) schedule(ctx context.Context, jobs []domain.Job) error {
	acquired, err := r.repository.AcquireSchedulerLease(
		ctx, r.leaseHolder, 3*r.reconcileInterval,
	)
	if err != nil {
		return fmt.Errorf("acquire scheduler lease: %w", err)
	}
	if !acquired {
		return nil
	}
	globalRunning := 0
	byNamespace := make(map[string]int)
	for _, job := range jobs {
		if job.ObservedState == domain.StateRunning {
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
	for index, job := range eligible {
		if globalRunning >= r.globalLimit {
			for _, unprocessed := range eligible[index:] {
				if err := r.repository.ReleaseSchedulerClaim(
					ctx, unprocessed.ID, r.leaseHolder,
				); err != nil {
					return err
				}
			}
			return nil
		}
		if byNamespace[job.Namespace] >= r.namespaceLimit {
			if err := r.repository.ReleaseSchedulerClaim(ctx, job.ID, r.leaseHolder); err != nil {
				return err
			}
			continue
		}
		admitErr := r.admit(ctx, job)
		releaseErr := r.repository.ReleaseSchedulerClaim(ctx, job.ID, r.leaseHolder)
		if admitErr != nil {
			return admitErr
		}
		if releaseErr != nil {
			return releaseErr
		}
		globalRunning++
		byNamespace[job.Namespace]++
	}
	return nil
}

func (r *Reconciler) admit(ctx context.Context, job domain.Job) error {
	if job.KubernetesUID == "" {
		created, err := r.kubernetes.CreateJob(ctx, job.Namespace, job.ID, job.Name, job.Template)
		if err != nil {
			return err
		}
		if _, err := r.repository.SetObserved(
			ctx, job.ID, domain.StatePaused, string(created.UID),
		); err != nil {
			return err
		}
		if err := r.kubernetes.Suspend(ctx, job.Namespace, created.Name, false); err != nil {
			return err
		}
		_, err = r.repository.SetObserved(
			ctx, job.ID, domain.StateRunning, string(created.UID),
		)
		return err
	}
	name := templateName(job)
	if name == "" {
		name = job.Name
	}
	if err := r.kubernetes.Suspend(ctx, job.Namespace, name, false); err != nil {
		return err
	}
	_, err := r.repository.SetObserved(ctx, job.ID, domain.StateRunning, job.KubernetesUID)
	return err
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
