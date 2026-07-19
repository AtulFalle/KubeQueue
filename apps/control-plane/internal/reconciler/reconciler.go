package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	kube "github.com/AtulFalle/KubeQueue/apps/control-plane/internal/adapters/kubernetes"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain/policyquota"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/leadership"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/platform/runtimemetrics"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/ports"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/scheduler"
	"github.com/google/uuid"
	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

type mutationFence interface {
	Run(context.Context) error
	TryAcquire(context.Context) error
	Authority(context.Context) (leadership.Authority, error)
	Revalidate(context.Context, leadership.Authority) error
	Snapshot() leadership.Snapshot
	Holder() string
}

type durableMutationStore interface {
	BeginMutation(
		context.Context, leadership.MutationRequest, leadership.Authority,
	) (leadership.MutationRecord, error)
	CompleteMutation(
		context.Context, leadership.MutationRequest, uint64,
		leadership.MutationOutcome, string,
	) (leadership.MutationRecord, error)
	ObserveMutation(
		context.Context, leadership.MutationRequest, leadership.Authority,
		leadership.MutationObservation,
	) (leadership.MutationRecord, error)
	PendingMutations(context.Context, string) ([]leadership.MutationRecord, error)
}

type Reconciler struct {
	repository        ports.Repository
	scheduling        ports.RuntimeSchedulingRepository
	kubernetes        *kube.Client
	leadership        mutationFence
	mutations         durableMutationStore
	scope             domain.NamespaceScope
	globalLimit       int
	namespaceLimit    int
	reconcileInterval time.Duration
	leaseHolder       string
	releaseVersion    string
	observedAt        time.Time
	ready             atomic.Bool
}

func New(
	repository ports.Repository, client *kube.Client, scope domain.NamespaceScope,
) *Reconciler {
	holder := uuid.NewString()
	manager, err := leadership.NewManager(
		repository.(leadership.LeaseStore), "reconciler", holder, 6*time.Second,
	)
	if err != nil {
		panic(fmt.Sprintf("construct reconciliation leadership: %v", err))
	}
	return NewWithLeadership(repository, client, scope, manager)
}

func NewWithLeadership(
	repository ports.Repository,
	client *kube.Client,
	scope domain.NamespaceScope,
	manager mutationFence,
) *Reconciler {
	reconciler := &Reconciler{
		repository: repository, kubernetes: client, leadership: manager,
		mutations: repository.(durableMutationStore), scope: scope,
		globalLimit:       envInt("KUBEQUEUE_GLOBAL_CONCURRENCY", 10),
		namespaceLimit:    envInt("KUBEQUEUE_NAMESPACE_CONCURRENCY", 5),
		reconcileInterval: 2 * time.Second,
		leaseHolder:       manager.Holder(),
		releaseVersion:    env("KUBEQUEUE_RELEASE_VERSION", "dev"),
	}
	if scheduling, ok := repository.(ports.RuntimeSchedulingRepository); ok {
		reconciler.scheduling = scheduling
	}
	return reconciler
}

func (r *Reconciler) Run(ctx context.Context) error {
	changes, err := r.kubernetes.Start(ctx, r.discoveryNamespaces())
	if err != nil {
		return fmt.Errorf("start Kubernetes informers: %w", err)
	}
	leadershipErrors := make(chan error, 1)
	go func() {
		leadershipErrors <- r.leadership.Run(ctx)
	}()
	recovery := time.NewTicker(30 * time.Second)
	defer recovery.Stop()
	heartbeat := time.NewTicker(5 * time.Second)
	defer heartbeat.Stop()
	if err := r.recordStatus(ctx, errors.New("informer synchronization is pending")); err != nil {
		slog.Error("record initial worker status", "error", err)
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-leadershipErrors:
			if err != nil {
				return fmt.Errorf("run reconciliation leadership: %w", err)
			}
			return nil
		case <-changes:
			if err := r.reconcileAndRecord(ctx); err != nil {
				slog.Error("reconciliation failed", "error", err)
			}
		case <-recovery.C:
			if err := r.reconcileAndRecord(ctx); err != nil {
				slog.Error("recovery reconciliation failed", "error", err)
			}
		case <-heartbeat.C:
			if err := r.recordHeartbeat(ctx); err != nil {
				slog.Error("record worker heartbeat", "error", err)
			}
		}
	}
}

func (r *Reconciler) Ready() bool {
	return r.ready.Load()
}

func (r *Reconciler) reconcileAndRecord(ctx context.Context) error {
	started := time.Now()
	reconcileErr := r.Reconcile(ctx)
	runtimemetrics.ObserveReconciliation(time.Since(started), reconcileErr != nil)
	statusErr := r.recordStatus(ctx, reconcileErr)
	return errors.Join(reconcileErr, statusErr)
}

func (r *Reconciler) recordHeartbeat(ctx context.Context) error {
	status, err := r.repository.WorkerStatus(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	status.HeartbeatAt = &now
	return r.repository.UpdateWorkerStatus(ctx, status)
}

func (r *Reconciler) recordStatus(ctx context.Context, reconcileErr error) error {
	namespaceStatuses, effectiveNamespaces, authorityErr := r.authorityStatuses(ctx)
	leadershipErr := r.leadershipStatusError()
	now := time.Now().UTC()
	status := domain.WorkerStatus{
		State:                domain.WorkerStateReady,
		HeartbeatAt:          &now,
		WatchMode:            r.scope.Mode(),
		EffectiveNamespaces:  effectiveNamespaces,
		ExcludedNamespaces:   r.scope.ExcludedNamespaces(),
		Namespaces:           namespaceStatuses,
		GlobalConcurrency:    r.globalLimit,
		NamespaceConcurrency: r.namespaceLimit,
		ReleaseVersion:       r.releaseVersion,
	}
	if reconcileErr == nil {
		status.LastSuccessfulReconciliationAt = &now
	}
	if statusErr := errors.Join(reconcileErr, authorityErr, leadershipErr); statusErr != nil {
		status.State = domain.WorkerStateDegraded
		status.ActiveError = statusErr.Error()
	}
	if err := r.repository.UpdateWorkerStatus(ctx, status); err != nil {
		r.ready.Store(false)
		runtimemetrics.SetWorkerReadiness(false, 0, len(namespaceStatuses))
		return err
	}
	ready := authorityErr == nil && leadershipErr == nil
	r.ready.Store(ready)
	synced := 0
	for _, namespace := range namespaceStatuses {
		if namespace.InformerSynced {
			synced++
		}
	}
	runtimemetrics.SetWorkerReadiness(ready, synced, len(namespaceStatuses))
	leadershipStatus := r.leadership.Snapshot()
	runtimemetrics.SetLeadership(
		leadershipStatus.Generation, leadershipStatus.Role == leadership.RoleLeader,
	)
	return nil
}

func (r *Reconciler) leadershipStatusError() error {
	status := r.leadership.Snapshot()
	if status.Role == leadership.RoleLeader {
		return nil
	}
	return fmt.Errorf(
		"reconciliation leadership is %s at generation %d", status.Role, status.Generation,
	)
}

func (r *Reconciler) authorityStatuses(
	ctx context.Context,
) ([]domain.NamespaceAuthorityStatus, []string, error) {
	namespaces := r.scope.Namespaces()
	clusterAuthorized := false
	clusterMessage := ""
	var authorityErrors []error
	if r.scope.Mode() == domain.WatchModeAll {
		discovered, err := r.kubernetes.ListNamespaces(ctx)
		if err != nil {
			authorityErrors = append(authorityErrors, fmt.Errorf("list namespaces: %w", err))
		} else {
			namespaces = namespaces[:0]
			for _, namespace := range discovered {
				if r.scope.Allows(namespace) {
					namespaces = append(namespaces, namespace)
				}
			}
			sort.Strings(namespaces)
		}
		var accessErr error
		clusterAuthorized, clusterMessage, accessErr = r.kubernetes.CheckJobAccess(ctx, "")
		if accessErr != nil {
			authorityErrors = append(authorityErrors, accessErr)
		} else if !clusterAuthorized {
			authorityErrors = append(authorityErrors, errors.New(clusterMessage))
		}
	}

	statuses := make([]domain.NamespaceAuthorityStatus, 0, len(namespaces))
	now := time.Now().UTC()
	for _, namespace := range namespaces {
		authorized, message := clusterAuthorized, clusterMessage
		var err error
		if r.scope.Mode() == domain.WatchModeSelected {
			authorized, message, err = r.kubernetes.CheckJobAccess(ctx, namespace)
			if err != nil {
				authorityErrors = append(
					authorityErrors, fmt.Errorf("check namespace %s: %w", namespace, err),
				)
			} else if !authorized {
				authorityErrors = append(
					authorityErrors, fmt.Errorf("namespace %s: %s", namespace, message),
				)
			}
		}
		informerNamespace := namespace
		if r.scope.Mode() == domain.WatchModeAll {
			informerNamespace = ""
		}
		synced := r.kubernetes.InformerSynced(informerNamespace)
		if !synced {
			authorityErrors = append(
				authorityErrors, fmt.Errorf("namespace %s informer is not synchronized", namespace),
			)
		}
		statuses = append(statuses, domain.NamespaceAuthorityStatus{
			Namespace: namespace, InformerSynced: synced, Authorized: authorized,
			Message: message, ObservedAt: &now,
		})
	}
	return statuses, namespaces, errors.Join(authorityErrors...)
}

func (r *Reconciler) Reconcile(ctx context.Context) error {
	var reconcileErrors []error
	if err := r.markOutOfScope(ctx); err != nil {
		reconcileErrors = append(reconcileErrors, fmt.Errorf("mark out-of-scope jobs: %w", err))
	}
	observationComplete := true
	for _, namespace := range r.discoveryNamespaces() {
		if err := r.discover(ctx, namespace); err != nil {
			observationComplete = false
			reconcileErrors = append(
				reconcileErrors, fmt.Errorf("discover namespace %s: %w", namespace, err),
			)
		}
	}
	if observationComplete {
		r.observedAt = time.Now().UTC()
	} else {
		r.observedAt = time.Time{}
	}
	jobs, err := r.listJobsByPage(ctx, ports.JobFilter{})
	if err != nil {
		return errors.Join(append(reconcileErrors, fmt.Errorf("list jobs: %w", err))...)
	}
	queueDepth := 0
	for _, job := range jobs {
		if job.DesiredState == domain.StateQueued && countsTowardConcurrency(job) {
			queueDepth++
		}
	}
	runtimemetrics.SetQueueDepth(queueDepth)
	if err := r.leadership.TryAcquire(ctx); err != nil {
		if errors.Is(err, leadership.ErrLeaseHeld) {
			return errors.Join(reconcileErrors...)
		}
		return errors.Join(append(
			reconcileErrors, fmt.Errorf("acquire reconciliation leadership: %w", err),
		)...)
	}
	authority, err := r.leadership.Authority(ctx)
	if err != nil {
		return errors.Join(append(
			reconcileErrors, fmt.Errorf("validate reconciliation leadership: %w", err),
		)...)
	}
	if err := r.applyCommands(ctx, jobs, authority); err != nil {
		reconcileErrors = append(reconcileErrors, err)
	}
	if err := r.schedule(ctx, jobs, authority); err != nil {
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
		observedNamespace := observed.Namespace
		if observedNamespace == "" {
			observedNamespace = namespace
		}
		if !r.scope.Allows(observedNamespace) {
			continue
		}
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
			_, err := r.repository.Adopt(ctx, domain.Job{
				Name: observed.Name, Namespace: observedNamespace, Team: observed.Labels["team"],
				DesiredState: state, ObservedState: state, KubernetesUID: uid,
				ManagementMode: mode, SyncStatus: domain.SyncStatusSynced,
				ResourceVersion: observed.ResourceVersion, LastSeenAt: &observedAt,
				ObservedAt: &observedAt, ObservedReason: reason, ObservedMessage: message,
				Template: kube.Template(observed), Attempt: 1,
			})
			if err != nil {
				discoveryErrors = append(
					discoveryErrors,
					fmt.Errorf("adopt Job %s/%s: %w", observedNamespace, observed.Name, err),
				)
			}
			continue
		}
		current, err := r.repository.Get(ctx, id)
		if errors.Is(err, ports.ErrNotFound) {
			_, err = r.repository.Adopt(ctx, domain.Job{
				Name: observed.Name, Namespace: observedNamespace,
				Team: observed.Labels["team"], DesiredState: state, ObservedState: state,
				KubernetesUID: uid, ManagementMode: domain.ManagementConflicted,
				SyncStatus: domain.SyncStatusConflicted, ResourceVersion: observed.ResourceVersion,
				LastSeenAt: &observedAt, ObservedAt: &observedAt,
				ObservedReason:  "UnknownDurableID",
				ObservedMessage: "Job claims a KubeQueue ID that does not exist",
				Template:        kube.Template(observed), Attempt: 1,
			})
		} else if err == nil {
			if current.Namespace != observedNamespace || current.Name != observed.Name ||
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
			jobErr := fmt.Errorf("observe Job %s/%s: %w", observedNamespace, observed.Name, err)
			if current.ID != "" {
				jobErr = r.recordJobError(ctx, current, jobErr)
			}
			discoveryErrors = append(discoveryErrors, jobErr)
		}
	}
	storedJobs, err := r.listJobsByPage(ctx, ports.JobFilter{Namespace: namespace})
	if err != nil {
		return errors.Join(append(discoveryErrors, err)...)
	}
	for _, stored := range storedJobs {
		if !r.scope.Allows(stored.Namespace) || stored.KubernetesUID == "" {
			continue
		}
		if _, exists := seenUIDs[stored.KubernetesUID]; exists {
			continue
		}
		if stored.DesiredState == domain.StateCancelled &&
			stored.ObservedState != domain.StateCancelled {
			if _, err := r.repository.SetObserved(ctx, stored.ID, domain.Observation{
				State:                   domain.StateCancelled,
				KubernetesUID:           stored.KubernetesUID,
				ResourceVersion:         stored.ResourceVersion,
				ExpectedResourceVersion: stored.ResourceVersion,
				Reason:                  "Terminated",
				Message:                 "Kubernetes Job deletion was observed",
				ObservedAt:              time.Now().UTC(),
				ManagementMode:          domain.ManagementManaged,
				SyncStatus:              domain.SyncStatusSynced,
			}); err != nil {
				discoveryErrors = append(
					discoveryErrors,
					r.recordJobError(ctx, stored, fmt.Errorf("observe Job %s termination: %w", stored.ID, err)),
				)
			}
			continue
		}
		if stored.Terminal() {
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

func (r *Reconciler) markOutOfScope(ctx context.Context) error {
	jobs, err := r.listJobsByPage(ctx, ports.JobFilter{})
	if err != nil {
		return err
	}
	var scopeErrors []error
	for _, job := range jobs {
		if r.scope.Allows(job.Namespace) || job.SyncStatus == domain.SyncStatusOutOfScope ||
			job.ArchivedAt != nil {
			continue
		}
		if _, err := r.repository.MarkOutOfScope(
			ctx, job.ID, job.ResourceVersion, time.Now().UTC(),
		); err != nil {
			scopeErrors = append(scopeErrors, fmt.Errorf("mark Job %s out of scope: %w", job.ID, err))
		}
	}
	return errors.Join(scopeErrors...)
}

func (r *Reconciler) listJobsByPage(
	ctx context.Context,
	filter ports.JobFilter,
) ([]domain.Job, error) {
	const pageSize = 100
	jobs := make([]domain.Job, 0, pageSize)
	var after *ports.JobPageCursor
	for {
		page, err := r.repository.ListPage(ctx, ports.JobPageRequest{
			Filter: filter, Sort: ports.JobSortQueue, Limit: pageSize, After: after,
		})
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, page.Items...)
		if page.Next == nil {
			return jobs, nil
		}
		after = page.Next
	}
}

func (r *Reconciler) discoveryNamespaces() []string {
	if r.scope.Mode() == domain.WatchModeAll {
		return []string{""}
	}
	return r.scope.Namespaces()
}

func (r *Reconciler) applyCommands(
	ctx context.Context, jobs []domain.Job, authority leadership.Authority,
) error {
	var commandErrors []error
	for _, job := range jobs {
		if job.KubernetesUID == "" || job.ManagementMode != domain.ManagementManaged ||
			retryPending(job) || job.SyncStatus == domain.SyncStatusMissing ||
			job.SyncStatus == domain.SyncStatusOutOfScope ||
			job.SyncStatus == domain.SyncStatusConflicted {
			continue
		}
		if err := r.resolveOutstandingMutations(ctx, job, authority); err != nil {
			if errors.Is(err, leadership.ErrObservationRequired) {
				continue
			}
			return errors.Join(append(commandErrors, err)...)
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
				request := mutationRequest(job, "suspend")
				if err := r.mutateKubernetes(ctx, authority, request, false, func() error {
					return r.kubernetes.Suspend(
						ctx, job.Namespace, name, job.KubernetesUID, job.ResourceVersion, true,
					)
				}); err != nil {
					if isLeadershipError(err) || errors.Is(err, leadership.ErrObservationRequired) {
						return errors.Join(append(commandErrors, err)...)
					}
					commandErrors = append(
						commandErrors,
						r.recordJobError(ctx, job, fmt.Errorf("pause Job %s: %w", job.ID, err)),
					)
				}
			}
		case domain.StateCancelled:
			if job.ObservedState != domain.StateCancelled {
				request := mutationRequest(job, "delete")
				deleteErr := r.mutateKubernetes(ctx, authority, request, false, func() error {
					return r.kubernetes.DeleteJob(
						ctx, job.Namespace, name, job.KubernetesUID, job.ResourceVersion,
					)
				})
				if deleteErr != nil && !kube.IsNotFound(deleteErr) {
					if isLeadershipError(deleteErr) ||
						errors.Is(deleteErr, leadership.ErrObservationRequired) {
						return errors.Join(append(commandErrors, deleteErr)...)
					}
					commandErrors = append(
						commandErrors,
						r.recordJobError(ctx, job, fmt.Errorf("terminate Job %s: %w", job.ID, deleteErr)),
					)
					continue
				}
				if kube.IsNotFound(deleteErr) {
					if _, err := r.repository.SetObserved(ctx, job.ID, domain.Observation{
						State: domain.StateCancelled, KubernetesUID: job.KubernetesUID,
						ResourceVersion:         job.ResourceVersion,
						ExpectedResourceVersion: job.ResourceVersion,
						Reason:                  "Terminated", Message: "Kubernetes Job deletion was observed",
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
	}
	return errors.Join(commandErrors...)
}

func (r *Reconciler) schedule(
	ctx context.Context, jobs []domain.Job, authority leadership.Authority,
) error {
	globalRunning := 0
	for _, job := range jobs {
		if job.ObservedState == domain.StateRunning && countsTowardConcurrency(job) {
			globalRunning++
		}
	}
	available := r.globalLimit - globalRunning
	if available <= 0 {
		return nil
	}
	if r.scheduling == nil {
		return r.scheduleLegacy(ctx, jobs, authority, available)
	}
	projects, err := r.scheduling.SchedulingCandidates(
		ctx, ports.MaxSchedulingProjects, ports.MaxSchedulingCandidatesProject,
	)
	if err != nil {
		return err
	}
	if len(projects) == 0 {
		return r.scheduleLegacy(ctx, jobs, authority, available)
	}
	installationID := projects[0].InstallationID
	for _, project := range projects {
		if project.InstallationID != installationID {
			return errors.New("local scheduler received candidates from multiple installations")
		}
	}
	projectIDs := make([]domain.ProjectID, 0, len(projects))
	for _, project := range projects {
		projectIDs = append(projectIDs, project.ProjectID)
	}
	fairness, err := r.scheduling.FairnessState(ctx, installationID, projectIDs)
	if err != nil {
		return err
	}

	var admissionErrors []error
	casConflicts := 0
	for admitted := 0; admitted < available; {
		input, candidates := runtimeSchedulerInput(projects)
		outcome, err := scheduler.Select(scheduler.Policy{
			Version: "weighted-fair-v1", AgingStep: 1,
		}, fairness.State, input)
		if err != nil {
			return errors.Join(append(admissionErrors, err)...)
		}
		if outcome.Decision == nil {
			break
		}
		selected, found := candidates[outcome.Decision.JobID]
		if !found {
			return errors.New("scheduler selected an unknown bounded candidate")
		}
		target := policyquota.Scope{
			Kind:    policyquota.ScopeNamespace,
			Project: string(selected.Job.ProjectID), Namespace: selected.Job.Namespace,
		}
		policies, err := r.scheduling.PolicyHierarchy(ctx, installationID, target)
		if err != nil {
			return errors.Join(append(admissionErrors, err)...)
		}
		effective, err := policyquota.Compose(policies...)
		if err != nil {
			return errors.Join(append(admissionErrors, err)...)
		}
		ref := effective.Applied[len(effective.Applied)-1]
		outcome.Decision.AppliedPolicyVersion = fmt.Sprintf("%s:%d", ref.ID, ref.Version)
		if err := r.leadership.Revalidate(ctx, authority); err != nil {
			return errors.Join(append(admissionErrors,
				fmt.Errorf("validate leadership before fair admission: %w", err))...)
		}
		result, err := r.scheduling.CommitRuntimeAdmission(
			ctx, ports.RuntimeAdmissionRequest{
				Authority: authority, InstallationID: installationID,
				ExpectedFairnessVersion: fairness.Version,
				NextFairnessState:       outcome.State,
				Decision: ports.AdmissionDecision{
					ID: uuid.NewString(), InstallationID: installationID,
					Policy: ref, Scheduling: *outcome.Decision,
					DecidedBy: fmt.Sprintf(
						"%s:generation:%d", authority.Holder, authority.Generation,
					),
				},
				Policy: effective, ClaimTTL: 15 * time.Second, RejectionRetry: 2 * time.Second,
			},
		)
		if errors.Is(err, ports.ErrConflict) {
			casConflicts++
			if casConflicts >= 3 {
				return errors.Join(append(admissionErrors,
					fmt.Errorf("fair scheduling CAS retries exhausted: %w", ports.ErrConflict))...)
			}
			fairness, err = r.scheduling.FairnessState(ctx, installationID, projectIDs)
			if err != nil {
				return errors.Join(append(admissionErrors, err)...)
			}
			continue
		}
		if err != nil {
			return errors.Join(append(admissionErrors, err)...)
		}
		casConflicts = 0
		fairness = result.Fairness
		disableRuntimeCandidate(projects, outcome.Decision.JobID)
		if result.Quota.Rejection != nil {
			runtimemetrics.RecordAdmissionRejection("quota")
			continue
		}

		admitErr := r.admit(ctx, selected.Job, authority)
		if admitErr == nil {
			admitted++
			continue
		}
		runtimemetrics.RecordAdmissionRejection("admission_error")
		if isLeadershipError(admitErr) ||
			errors.Is(admitErr, leadership.ErrObservationRequired) {
			return errors.Join(append(admissionErrors, admitErr)...)
		}
		if err := r.scheduling.AbandonRuntimeAdmission(
			ctx, authority, installationID, selected.Job.ID, admitErr.Error(),
		); err != nil {
			return errors.Join(append(admissionErrors, admitErr, err)...)
		}
		admissionErrors = append(admissionErrors,
			fmt.Errorf("admit Job %s: %w", selected.Job.ID, admitErr))
	}
	return errors.Join(admissionErrors...)
}

func runtimeSchedulerInput(
	projects []ports.SchedulingProject,
) ([]scheduler.Project, map[string]ports.SchedulingCandidate) {
	input := make([]scheduler.Project, 0, len(projects))
	candidates := make(map[string]ports.SchedulingCandidate)
	for _, project := range projects {
		jobs := make([]scheduler.Job, 0, len(project.Candidates))
		for _, candidate := range project.Candidates {
			jobs = append(jobs, scheduler.Job{
				ID: candidate.Job.ID, Priority: int64(candidate.Job.Priority),
				Age: candidate.Age, Eligible: true, Lane: candidate.Lane,
				EmergencyRequested:     candidate.EmergencyRequested,
				EmergencyAuthorized:    candidate.EmergencyAuthorized,
				EmergencyAuthorization: candidate.EmergencyAuthorization,
			})
			candidates[candidate.Job.ID] = candidate
		}
		input = append(input, scheduler.Project{
			ID: string(project.ProjectID), Weight: project.Weight, Jobs: jobs,
		})
	}
	return input, candidates
}

func disableRuntimeCandidate(projects []ports.SchedulingProject, jobID string) {
	for projectIndex := range projects {
		candidates := projects[projectIndex].Candidates
		for index := range candidates {
			if candidates[index].Job.ID != jobID {
				continue
			}
			candidates = append(candidates[:index], candidates[index+1:]...)
			projects[projectIndex].Candidates = candidates
			return
		}
	}
}

func (r *Reconciler) scheduleLegacy(
	ctx context.Context,
	jobs []domain.Job,
	authority leadership.Authority,
	available int,
) error {
	if err := r.leadership.Revalidate(ctx, authority); err != nil {
		return fmt.Errorf("validate leadership before scheduler claims: %w", err)
	}
	eligible, err := r.repository.ClaimEligible(
		ctx, r.leaseHolder, len(jobs)+available, time.Minute,
	)
	if err != nil {
		return err
	}
	byNamespace := make(map[string]int)
	for _, job := range jobs {
		if job.ObservedState == domain.StateRunning && countsTowardConcurrency(job) {
			byNamespace[job.Namespace]++
		}
	}
	var admissionErrors []error
	admitted := 0
	for index, job := range eligible {
		if admitted >= available {
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
			runtimemetrics.RecordAdmissionRejection("namespace_limit")
			if err := r.repository.ReleaseSchedulerClaim(ctx, job.ID, r.leaseHolder); err != nil {
				admissionErrors = append(
					admissionErrors,
					fmt.Errorf("release scheduler claim for Job %s: %w", job.ID, err),
				)
			}
			continue
		}
		admitErr := r.admit(ctx, job, authority)
		var releaseErr error
		if !isLeadershipError(admitErr) &&
			!errors.Is(admitErr, leadership.ErrObservationRequired) {
			releaseErr = r.repository.ReleaseSchedulerClaim(ctx, job.ID, r.leaseHolder)
		}
		if admitErr != nil {
			runtimemetrics.RecordAdmissionRejection("admission_error")
			if isLeadershipError(admitErr) ||
				errors.Is(admitErr, leadership.ErrObservationRequired) {
				admissionErrors = append(admissionErrors, admitErr)
			} else {
				admissionErrors = append(
					admissionErrors,
					r.recordJobError(ctx, job, fmt.Errorf("admit Job %s: %w", job.ID, admitErr)),
				)
			}
		}
		if releaseErr != nil {
			admissionErrors = append(
				admissionErrors,
				fmt.Errorf("release scheduler claim for Job %s: %w", job.ID, releaseErr),
			)
		}
		if admitErr != nil {
			if isLeadershipError(admitErr) ||
				errors.Is(admitErr, leadership.ErrObservationRequired) {
				break
			}
			continue
		}
		byNamespace[job.Namespace]++
		admitted++
	}
	return errors.Join(admissionErrors...)
}

func (r *Reconciler) admit(
	ctx context.Context, job domain.Job, authority leadership.Authority,
) error {
	if job.KubernetesUID == "" {
		var created batchv1.Job
		err := r.mutateKubernetes(
			ctx, authority, mutationRequest(job, "create"), false, func() error {
				var createErr error
				created, createErr = r.kubernetes.CreateJob(
					ctx, job.Namespace, job.ID, job.Name, job.Template,
				)
				return createErr
			})
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
		if err := r.mutateKubernetes(
			ctx, authority, mutationRequest(job, "resume"), false, func() error {
				return r.kubernetes.Suspend(
					ctx, job.Namespace, created.Name, string(created.UID), created.ResourceVersion, false,
				)
			}); err != nil {
			return err
		}
		return nil
	}
	name := templateName(job)
	if name == "" {
		name = job.Name
	}
	if err := r.mutateKubernetes(
		ctx, authority, mutationRequest(job, "resume"),
		job.ObservedState != domain.StatePaused,
		func() error {
			return r.kubernetes.Suspend(
				ctx, job.Namespace, name, job.KubernetesUID, job.ResourceVersion, false,
			)
		}); err != nil {
		return err
	}
	return nil
}

func (r *Reconciler) mutateKubernetes(
	ctx context.Context,
	authority leadership.Authority,
	request leadership.MutationRequest,
	effectPresent bool,
	mutate func() error,
) error {
	record, err := r.mutations.BeginMutation(ctx, request, authority)
	if errors.Is(err, leadership.ErrMutationNotReady) {
		return nil
	}
	if errors.Is(err, leadership.ErrObservationRequired) {
		if r.observedAt.IsZero() || !r.observedAt.After(record.StartedAt) {
			return leadership.ErrObservationRequired
		}
		observation := leadership.ObservationEffectAbsent
		if effectPresent {
			observation = leadership.ObservationEffectPresent
		}
		if _, observeErr := r.mutations.ObserveMutation(
			ctx, request, authority, observation,
		); observeErr != nil {
			return observeErr
		}
		if effectPresent {
			return nil
		}
		record, err = r.mutations.BeginMutation(ctx, request, authority)
	}
	if err != nil {
		return err
	}
	if err := r.leadership.Revalidate(ctx, authority); err != nil {
		_, completeErr := r.mutations.CompleteMutation(
			ctx, request, record.Generation, leadership.OutcomeFailed, "LEADERSHIP_FENCE",
		)
		return errors.Join(
			fmt.Errorf("leadership fence rejected Kubernetes mutation: %w", err), completeErr,
		)
	}
	mutationErr := mutate()
	outcome := leadership.OutcomeSucceeded
	errorClass := ""
	if mutationErr != nil {
		outcome = leadership.OutcomeFailed
		errorClass, _ = kube.ClassifyError(mutationErr)
		if ambiguousMutationError(mutationErr) {
			outcome = leadership.OutcomeUncertain
		}
	}
	if _, completeErr := r.mutations.CompleteMutation(
		ctx, request, record.Generation, outcome, errorClass,
	); completeErr != nil {
		return errors.Join(mutationErr, completeErr)
	}
	if outcome == leadership.OutcomeUncertain {
		return errors.Join(mutationErr, leadership.ErrObservationRequired)
	}
	return mutationErr
}

func (r *Reconciler) resolveOutstandingMutations(
	ctx context.Context, job domain.Job, authority leadership.Authority,
) error {
	records, err := r.mutations.PendingMutations(ctx, job.ID)
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.State == leadership.MutationInFlight {
			record, err = r.mutations.BeginMutation(ctx, record.MutationRequest, authority)
		}
		if errors.Is(err, leadership.ErrMutationNotReady) {
			continue
		}
		if err != nil && !errors.Is(err, leadership.ErrObservationRequired) {
			return err
		}
		if record.State != leadership.MutationObservationRequired {
			continue
		}
		if r.observedAt.IsZero() || !r.observedAt.After(record.StartedAt) {
			return leadership.ErrObservationRequired
		}
		effectPresent := mutationEffectPresent(job, record.Operation)
		observation := leadership.ObservationEffectAbsent
		if effectPresent {
			observation = leadership.ObservationEffectPresent
		}
		if _, err := r.mutations.ObserveMutation(
			ctx, record.MutationRequest, authority, observation,
		); err != nil {
			return err
		}
	}
	return nil
}

func mutationRequest(job domain.Job, operation string) leadership.MutationRequest {
	attemptIdentity := fmt.Sprintf("attempt:%d", job.Attempt)
	requestIdentity := attemptIdentity
	if operation != "create" {
		requestIdentity = fmt.Sprintf("version:%d", job.Version)
	}
	return leadership.MutationRequest{
		Operation: operation, JobID: job.ID,
		AttemptIdentity: attemptIdentity, RequestIdentity: requestIdentity,
	}
}

func mutationEffectPresent(job domain.Job, operation string) bool {
	switch operation {
	case "create":
		return job.KubernetesUID != ""
	case "suspend":
		return job.ObservedState == domain.StatePaused
	case "resume":
		return job.KubernetesUID != "" && job.ObservedState != domain.StatePaused
	case "delete":
		return job.ObservedState == domain.StateCancelled
	default:
		return false
	}
}

func ambiguousMutationError(err error) bool {
	if apierrors.IsTimeout(err) ||
		apierrors.IsServerTimeout(err) ||
		apierrors.IsInternalError(err) ||
		apierrors.IsServiceUnavailable(err) {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var networkError net.Error
	return errors.As(err, &networkError)
}

func isLeadershipError(err error) bool {
	return errors.Is(err, leadership.ErrLeadershipLost) ||
		errors.Is(err, leadership.ErrLeadershipPaused) ||
		errors.Is(err, leadership.ErrStaleGeneration) ||
		errors.Is(err, leadership.ErrNotLeaseHolder) ||
		errors.Is(err, leadership.ErrLeaseExpired)
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
	code, remediation := kube.ClassifyError(reconcileErr)
	if err := r.repository.RecordReconcileError(
		ctx, job.ID, job.ResourceVersion, code, reconcileErr.Error(), remediation,
		time.Now().UTC().Add(delay),
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
