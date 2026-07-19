package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain/policyquota"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/ports"
	"github.com/google/uuid"
)

const workerHeartbeatTimeout = 15 * time.Second

type Jobs struct {
	repository  ports.ScopedJobRepository
	scope       domain.NamespaceScope
	authorizer  Authorizer
	policyQuota *PolicyQuotaService
}

func NewJobs(
	repository ports.ScopedJobRepository, scope domain.NamespaceScope, authorizer Authorizer,
) *Jobs {
	jobs := &Jobs{repository: repository, scope: scope, authorizer: authorizer}
	if policyRepository, ok := repository.(ports.PolicyQuotaJobRepository); ok {
		jobs.policyQuota = NewPolicyQuotaService(policyRepository)
	}
	return jobs
}

func (j *Jobs) Create(ctx context.Context, input domain.CreateJob) (domain.Job, error) {
	if input.IdempotencyKey == "" {
		input.IdempotencyKey = "internal:" + uuid.NewString()
	}
	if err := input.Validate(); err != nil {
		return domain.Job{}, err
	}
	if !j.scope.Allows(input.Namespace) {
		return domain.Job{}, fmt.Errorf(
			"%w: namespace %q is not managed", domain.ErrNamespaceOutOfScope, input.Namespace,
		)
	}
	actor, err := ActorFromContext(ctx)
	if err != nil {
		return domain.Job{}, err
	}
	binding, installationID, err := j.repository.NamespaceBinding(ctx, input.Namespace)
	if err != nil {
		return domain.Job{}, err
	}
	if err := j.authorizer.Authorize(ctx, actor, domain.PermissionJobsSubmit, domain.AuthorizationScope{
		InstallationID: installationID, ProjectID: binding.ProjectID,
	}); err != nil {
		return domain.Job{}, err
	}
	if err := j.requireNamespaceReady(ctx, input.Namespace); err != nil {
		return domain.Job{}, err
	}
	input.ProjectID = binding.ProjectID
	input.NamespaceBindingID = binding.ID
	input.CreatorPrincipalID = actor.PrincipalID
	input.SubmissionSource = domain.SubmissionSourceAPI
	input.ID = uuid.NewString()
	ctx, err = withTransactionalAudit(
		ctx, actor, "jobs.submit", "job", input.ID, input.ProjectID,
		"SUBMITTED", "request.accepted",
		"name", "namespace", "team", "priority", "scheduled_for",
	)
	if err != nil {
		return domain.Job{}, err
	}
	if j.policyQuota != nil {
		job, policyErr := j.policyQuota.Submit(ctx, installationID, policyquota.Scope{
			Kind: policyquota.ScopeNamespace, Project: string(binding.ProjectID),
			Namespace: binding.Namespace,
		}, PolicyQuotaSubmission{
			Job: input, IdempotencyKey: string(actor.PrincipalID) + ":" + input.IdempotencyKey,
			PrioritySpecified: input.Priority != 0,
		})
		if policyErr == nil || !errors.Is(policyErr, ErrPolicyNotConfigured) {
			return job, policyErr
		}
		// Compatibility installations without a versioned policy continue on
		// the legacy path until policy bootstrap is wired into setup.
	}
	return j.repository.Create(ctx, input)
}

func (j *Jobs) List(ctx context.Context, filter ports.JobFilter) ([]domain.Job, error) {
	projectIDs, err := j.accessibleProjects(ctx, domain.PermissionJobsList)
	if err != nil {
		return nil, err
	}
	filter.ProjectIDs = projectIDs
	return j.repository.List(ctx, filter)
}

func (j *Jobs) ListPage(ctx context.Context, request ports.JobPageRequest) (ports.JobPage, error) {
	projectIDs, err := j.accessibleProjects(ctx, domain.PermissionJobsList)
	if err != nil {
		return ports.JobPage{}, err
	}
	if request.Filter.ProjectID != "" && !projectAllowed(request.Filter.ProjectID, projectIDs) {
		return ports.JobPage{Items: make([]domain.Job, 0)}, nil
	}
	request.Filter.ProjectIDs = projectIDs
	return j.repository.ListPage(ctx, request)
}

func (j *Jobs) Facets(ctx context.Context) (domain.JobFacets, error) {
	projectIDs, err := j.accessibleProjects(ctx, domain.PermissionJobsList)
	if err != nil {
		return domain.JobFacets{}, err
	}
	return j.repository.FacetsInProjects(ctx, projectIDs)
}

func (j *Jobs) Queue(ctx context.Context) ([]domain.Job, int64, error) {
	projectIDs, err := j.accessibleProjects(ctx, domain.PermissionQueueRead)
	if err != nil {
		return nil, 0, err
	}
	return j.repository.QueueInProjects(ctx, projectIDs)
}

func (j *Jobs) UpdateQueue(
	ctx context.Context, id string, priority int, position, version int64, scheduledFor *time.Time,
) (domain.Job, error) {
	current, err := j.getAuthorized(ctx, id, domain.PermissionQueueEntryUpdate)
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
	actor, err := ActorFromContext(ctx)
	if err != nil {
		return domain.Job{}, err
	}
	ctx, err = withTransactionalAudit(
		ctx, actor, "queue.update", "job", current.ID, current.ProjectID,
		"UPDATED", "request.accepted", "priority", "position", "scheduled_for",
	)
	if err != nil {
		return domain.Job{}, err
	}
	return j.repository.UpdateQueue(ctx, id, priority, position, version, scheduledFor)
}

func (j *Jobs) Reorder(ctx context.Context, ids []string, version int64) (int64, error) {
	actor, err := ActorFromContext(ctx)
	if err != nil {
		return 0, err
	}
	if err := j.authorizer.Authorize(ctx, actor, domain.PermissionQueueGlobalReorder,
		domain.AuthorizationScope{InstallationID: actor.InstallationID}); err != nil {
		return 0, err
	}
	queue, _, err := j.repository.Queue(ctx)
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
	ctx, err = withTransactionalAudit(
		ctx, actor, "queue.reorder", "queue", string(actor.InstallationID), "",
		"REORDERED", "request.accepted", "job_ids", "queue_version",
	)
	if err != nil {
		return 0, err
	}
	return j.repository.Reorder(ctx, ids, version)
}

func (j *Jobs) ReorderProject(
	ctx context.Context,
	projectID domain.ProjectID,
	ids []string,
	version int64,
) (int64, error) {
	actor, err := ActorFromContext(ctx)
	if err != nil {
		return 0, err
	}
	if err := j.authorizer.Authorize(
		ctx, actor, domain.PermissionQueueProjectReorder,
		domain.AuthorizationScope{
			InstallationID: actor.InstallationID,
			ProjectID:      projectID,
		},
	); err != nil {
		return 0, err
	}
	queue, _, err := j.repository.QueueInProjects(ctx, []domain.ProjectID{projectID})
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
	ctx, err = withTransactionalAudit(
		ctx, actor, "queue.project.reorder", "project_queue", string(projectID), projectID,
		"REORDERED", "request.accepted", "job_ids", "queue_version",
	)
	if err != nil {
		return 0, err
	}
	return j.repository.ReorderProject(ctx, projectID, ids, version)
}

func (j *Jobs) Get(ctx context.Context, id string) (domain.Job, error) {
	return j.getAuthorized(ctx, id, domain.PermissionJobsRead)
}

func (j *Jobs) Manifest(ctx context.Context, id string) (json.RawMessage, error) {
	job, err := j.getAuthorized(ctx, id, domain.PermissionJobsManifestRead)
	if err != nil {
		return nil, err
	}
	return append(json.RawMessage(nil), job.Template...), nil
}

func (j *Jobs) getAuthorized(
	ctx context.Context, id string, permission domain.Permission,
) (domain.Job, error) {
	return j.getAuthorizedRecord(ctx, id, permission, false)
}

func (j *Jobs) getAuthorizedRecord(
	ctx context.Context, id string, permission domain.Permission, includeArchived bool,
) (domain.Job, error) {
	projectIDs, err := j.accessibleProjects(ctx, permission)
	if err != nil {
		if errors.Is(err, domain.ErrAccessDenied) {
			return domain.Job{}, ports.ErrNotFound
		}
		return domain.Job{}, err
	}
	job, err := j.repository.GetInProjects(ctx, id, projectIDs)
	if err != nil {
		return domain.Job{}, err
	}
	if job.ArchivedAt != nil && !includeArchived {
		return domain.Job{}, ports.ErrNotFound
	}
	return job, nil
}

func (j *Jobs) EventsPage(
	ctx context.Context, id string, request ports.EventPageRequest,
) (ports.EventPage, error) {
	if _, err := j.getAuthorized(ctx, id, domain.PermissionJobEventsRead); err != nil {
		return ports.EventPage{}, err
	}
	return j.repository.EventsPage(ctx, id, request)
}

func (j *Jobs) Command(ctx context.Context, id, command string) (domain.Job, error) {
	permission, err := commandPermission(command)
	if err != nil {
		return domain.Job{}, err
	}
	current, err := j.getAuthorized(ctx, id, permission)
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
	actor, err := ActorFromContext(ctx)
	if err != nil {
		return domain.Job{}, err
	}
	switch strings.ToLower(command) {
	case "start", "resume":
		if current.DesiredState == domain.StateQueued && !current.Terminal() {
			return current, nil
		}
		if current.Terminal() {
			return domain.Job{}, terminalCommandError(command)
		}
		ctx, err = withTransactionalAudit(
			ctx, actor, "jobs.resume", "job", current.ID, current.ProjectID,
			"RESUMED", "request.accepted", "desired_state",
		)
		if err != nil {
			return domain.Job{}, err
		}
		return j.repository.SetDesiredState(ctx, id, domain.StateQueued)
	case "pause":
		if current.DesiredState == domain.StatePaused && !current.Terminal() {
			return current, nil
		}
		if current.Terminal() {
			return domain.Job{}, terminalCommandError(command)
		}
		ctx, err = withTransactionalAudit(
			ctx, actor, "jobs.pause", "job", current.ID, current.ProjectID,
			"PAUSED", "request.accepted", "desired_state",
		)
		if err != nil {
			return domain.Job{}, err
		}
		return j.repository.SetDesiredState(ctx, id, domain.StatePaused)
	case "terminate":
		if current.DesiredState == domain.StateCancelled {
			return current, nil
		}
		if current.Terminal() {
			return domain.Job{}, terminalCommandError(command)
		}
		ctx, err = withTransactionalAudit(
			ctx, actor, "jobs.terminate", "job", current.ID, current.ProjectID,
			"TERMINATED", "request.accepted", "desired_state",
		)
		if err != nil {
			return domain.Job{}, err
		}
		return j.repository.SetDesiredState(ctx, id, domain.StateCancelled)
	case "retry":
		if current.ObservedState != domain.StateFailed &&
			current.DesiredState != domain.StateCancelled {
			return domain.Job{}, fmt.Errorf("%w: only failed or cancelled jobs can be retried",
				domain.ErrInvalidTransition)
		}
		input := domain.CreateJob{
			ID:        uuid.NewString(),
			Name:      retryName(current),
			Namespace: current.Namespace, Team: current.Team, Priority: current.Priority,
			Template: current.Template, ParentID: current.ID, Attempt: current.Attempt + 1,
			ProjectID: current.ProjectID, NamespaceBindingID: current.NamespaceBindingID,
			CreatorPrincipalID: actor.PrincipalID, SubmissionSource: domain.SubmissionSourceAPI,
		}
		ctx, err = withTransactionalAudit(
			ctx, actor, "jobs.retry", "job", input.ID, current.ProjectID,
			"RETRIED", "request.accepted", "parent_id", "attempt", "priority",
		)
		if err != nil {
			return domain.Job{}, err
		}
		if j.policyQuota != nil {
			job, policyErr := j.policyQuota.Submit(ctx, actor.InstallationID, policyquota.Scope{
				Kind: policyquota.ScopeNamespace, Project: string(current.ProjectID),
				Namespace: current.Namespace,
			}, PolicyQuotaSubmission{
				Job: input, IdempotencyKey: "retry:" + current.ID, PrioritySpecified: true,
			})
			if policyErr == nil || !errors.Is(policyErr, ErrPolicyNotConfigured) {
				return job, policyErr
			}
		}
		return j.repository.Create(ctx, input)
	default:
		return domain.Job{}, fmt.Errorf("unknown command %q", command)
	}
}

func (j *Jobs) Archive(ctx context.Context, id string) error {
	current, err := j.getAuthorizedRecord(ctx, id, domain.PermissionJobsArchive, true)
	if err != nil {
		return err
	}
	if current.ArchivedAt != nil {
		return nil
	}
	switch current.SyncStatus {
	case domain.SyncStatusMissing, domain.SyncStatusStale, domain.SyncStatusOutOfScope,
		domain.SyncStatusConflicted:
		actor, actorErr := ActorFromContext(ctx)
		if actorErr != nil {
			return actorErr
		}
		ctx, actorErr = withTransactionalAudit(
			ctx, actor, "jobs.archive", "job", current.ID, current.ProjectID,
			"ARCHIVED", "request.accepted", "archived_at",
		)
		if actorErr != nil {
			return actorErr
		}
		return j.repository.Archive(ctx, id, time.Now().UTC())
	case domain.SyncStatusSynced, domain.SyncStatusPending, domain.SyncStatusError:
		return domain.ErrNotArchivable
	}
	return errors.New("job has an unknown synchronization status")
}

func (j *Jobs) LatestStreamCursor(ctx context.Context) (int64, error) {
	projectIDs, err := j.accessibleProjects(ctx, domain.PermissionEventStreamRead)
	if err != nil {
		return 0, err
	}
	return j.repository.LatestJobChangeCursor(ctx, projectIDs)
}

func (j *Jobs) StreamChanges(
	ctx context.Context, after int64, limit int,
) (ports.JobChangePage, error) {
	projectIDs, err := j.accessibleProjects(ctx, domain.PermissionEventStreamRead)
	if err != nil {
		return ports.JobChangePage{}, err
	}
	return j.repository.JobChanges(ctx, projectIDs, after, limit)
}

func (j *Jobs) accessibleProjects(
	ctx context.Context, permission domain.Permission,
) ([]domain.ProjectID, error) {
	actor, err := ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	access, err := j.authorizer.AccessibleScope(ctx, actor, permission)
	if err != nil {
		return nil, err
	}
	if access.InstallationID != actor.InstallationID {
		return nil, domain.ErrAccessDenied
	}
	if access.InstallationWide {
		return nil, nil
	}
	if len(access.ProjectIDs) == 0 {
		return nil, domain.ErrAccessDenied
	}
	return access.ProjectIDs, nil
}

func projectAllowed(projectID domain.ProjectID, accessible []domain.ProjectID) bool {
	if accessible == nil {
		return true
	}
	for _, candidate := range accessible {
		if candidate == projectID {
			return true
		}
	}
	return false
}

func commandPermission(command string) (domain.Permission, error) {
	switch strings.ToLower(command) {
	case "start", "resume":
		return domain.PermissionJobsResume, nil
	case "pause":
		return domain.PermissionJobsPause, nil
	case "terminate":
		return domain.PermissionJobsTerminate, nil
	case "retry":
		return domain.PermissionJobsRetry, nil
	default:
		return "", fmt.Errorf("unknown command %q", command)
	}
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
