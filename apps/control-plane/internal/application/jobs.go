package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/ports"
)

const workerHeartbeatTimeout = 15 * time.Second

type Jobs struct {
	repository ports.Repository
	scope      domain.NamespaceScope
}

func NewJobs(repository ports.Repository, scope domain.NamespaceScope) *Jobs {
	return &Jobs{repository: repository, scope: scope}
}

func (j *Jobs) Create(ctx context.Context, input domain.CreateJob) (domain.Job, error) {
	if err := input.Validate(); err != nil {
		return domain.Job{}, err
	}
	if !j.scope.Allows(input.Namespace) {
		return domain.Job{}, fmt.Errorf(
			"%w: namespace %q is not managed", domain.ErrNamespaceOutOfScope, input.Namespace,
		)
	}
	if err := j.requireNamespaceReady(ctx, input.Namespace); err != nil {
		return domain.Job{}, err
	}
	return j.repository.Create(ctx, input)
}

func (j *Jobs) List(ctx context.Context, filter ports.JobFilter) ([]domain.Job, error) {
	return j.repository.List(ctx, filter)
}

func (j *Jobs) Facets(ctx context.Context) (domain.JobFacets, error) {
	return j.repository.Facets(ctx)
}

func (j *Jobs) Queue(ctx context.Context) ([]domain.Job, int64, error) {
	return j.repository.Queue(ctx)
}

func (j *Jobs) UpdateQueue(
	ctx context.Context, id string, priority int, position, version int64, scheduledFor *time.Time,
) (domain.Job, error) {
	current, err := j.Get(ctx, id)
	if err != nil {
		return domain.Job{}, err
	}
	if !j.scope.Allows(current.Namespace) || current.SyncStatus == domain.SyncStatusOutOfScope {
		return domain.Job{}, fmt.Errorf(
			"%w: namespace %q is not managed", domain.ErrNamespaceOutOfScope, current.Namespace,
		)
	}
	if current.ManagementMode != domain.ManagementManaged {
		return domain.Job{}, domain.ErrUnmanagedJob
	}
	return j.repository.UpdateQueue(ctx, id, priority, position, version, scheduledFor)
}

func (j *Jobs) Reorder(ctx context.Context, ids []string, version int64) (int64, error) {
	queue, _, err := j.Queue(ctx)
	if err != nil {
		return 0, err
	}
	for _, job := range queue {
		if !j.scope.Allows(job.Namespace) || job.SyncStatus == domain.SyncStatusOutOfScope {
			return 0, fmt.Errorf(
				"%w: namespace %q is not managed", domain.ErrNamespaceOutOfScope, job.Namespace,
			)
		}
	}
	return j.repository.Reorder(ctx, ids, version)
}

func (j *Jobs) Get(ctx context.Context, id string) (domain.Job, error) {
	job, err := j.repository.Get(ctx, id)
	if err != nil {
		return domain.Job{}, err
	}
	if job.ArchivedAt != nil {
		return domain.Job{}, ports.ErrNotFound
	}
	return job, nil
}

func (j *Jobs) Events(ctx context.Context, id string) ([]domain.Event, error) {
	if _, err := j.Get(ctx, id); err != nil {
		return nil, err
	}
	return j.repository.Events(ctx, id)
}

func (j *Jobs) Command(ctx context.Context, id, command string) (domain.Job, error) {
	current, err := j.Get(ctx, id)
	if err != nil {
		return domain.Job{}, err
	}
	if !j.scope.Allows(current.Namespace) || current.SyncStatus == domain.SyncStatusOutOfScope {
		return domain.Job{}, fmt.Errorf(
			"%w: namespace %q is not managed", domain.ErrNamespaceOutOfScope, current.Namespace,
		)
	}
	if current.ManagementMode != domain.ManagementManaged {
		return domain.Job{}, domain.ErrUnmanagedJob
	}
	switch strings.ToLower(command) {
	case "start", "resume":
		if current.DesiredState == domain.StateQueued && !current.Terminal() {
			return current, nil
		}
		if current.Terminal() {
			return domain.Job{}, terminalCommandError(command)
		}
		return j.repository.SetDesiredState(ctx, id, domain.StateQueued)
	case "pause":
		if current.DesiredState == domain.StatePaused && !current.Terminal() {
			return current, nil
		}
		if current.Terminal() {
			return domain.Job{}, terminalCommandError(command)
		}
		return j.repository.SetDesiredState(ctx, id, domain.StatePaused)
	case "terminate":
		if current.DesiredState == domain.StateCancelled {
			return current, nil
		}
		if current.Terminal() {
			return domain.Job{}, terminalCommandError(command)
		}
		return j.repository.SetDesiredState(ctx, id, domain.StateCancelled)
	case "retry":
		if current.ObservedState != domain.StateFailed &&
			current.DesiredState != domain.StateCancelled {
			return domain.Job{}, fmt.Errorf("%w: only failed or cancelled jobs can be retried",
				domain.ErrInvalidTransition)
		}
		return j.Create(ctx, domain.CreateJob{
			Name:      retryName(current),
			Namespace: current.Namespace, Team: current.Team, Priority: current.Priority,
			Template: current.Template, ParentID: current.ID, Attempt: current.Attempt + 1,
		})
	default:
		return domain.Job{}, fmt.Errorf("unknown command %q", command)
	}
}

func (j *Jobs) Archive(ctx context.Context, id string) error {
	current, err := j.repository.Get(ctx, id)
	if err != nil {
		return err
	}
	if current.ArchivedAt != nil {
		return nil
	}
	switch current.SyncStatus {
	case domain.SyncStatusMissing, domain.SyncStatusStale, domain.SyncStatusOutOfScope,
		domain.SyncStatusConflicted:
		return j.repository.Archive(ctx, id, time.Now().UTC())
	case domain.SyncStatusSynced, domain.SyncStatusPending, domain.SyncStatusError:
		return domain.ErrNotArchivable
	}
	return errors.New("job has an unknown synchronization status")
}

func terminalCommandError(command string) error {
	return fmt.Errorf("%w: cannot %s a terminal job", domain.ErrInvalidTransition, command)
}

func retryName(current domain.Job) string {
	base := current.Name
	if current.Attempt > 1 {
		base = strings.TrimSuffix(base, fmt.Sprintf("-retry-%d", current.Attempt))
	}
	suffix := fmt.Sprintf("-retry-%d", current.Attempt+1)
	if len(base)+len(suffix) > 63 {
		base = strings.TrimRight(base[:63-len(suffix)], "-")
	}
	return base + suffix
}

func (j *Jobs) requireNamespaceReady(ctx context.Context, namespace string) error {
	status, err := j.repository.WorkerStatus(ctx)
	if err != nil {
		return fmt.Errorf("read worker status: %w", err)
	}
	if status.HeartbeatAt == nil ||
		time.Since(status.HeartbeatAt.UTC()) > workerHeartbeatTimeout ||
		status.State == domain.WorkerStateUnavailable {
		return fmt.Errorf(
			"%w: worker status is unavailable", domain.ErrNamespaceUnavailable,
		)
	}
	for _, namespaceStatus := range status.Namespaces {
		if namespaceStatus.Namespace != namespace {
			continue
		}
		if namespaceStatus.InformerSynced && namespaceStatus.Authorized {
			return nil
		}
		message := strings.TrimSpace(namespaceStatus.Message)
		if message == "" {
			message = "informer synchronization or Kubernetes authorization is incomplete"
		}
		return fmt.Errorf("%w: namespace %q: %s",
			domain.ErrNamespaceUnavailable, namespace, message)
	}
	return fmt.Errorf(
		"%w: namespace %q has not been observed by the worker",
		domain.ErrNamespaceUnavailable, namespace,
	)
}
