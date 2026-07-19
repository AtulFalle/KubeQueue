package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain/policyquota"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/leadership"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/ports"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/scheduler"
)

const emergencyLaneAnnotation = "kubequeue.io/emergency"

func (s *Store) SchedulingCandidates(
	ctx context.Context,
	maxProjects int,
	perProject int,
) ([]ports.SchedulingProject, error) {
	if maxProjects < 1 || maxProjects > ports.MaxSchedulingProjects {
		return nil, fmt.Errorf(
			"max projects must be between 1 and %d", ports.MaxSchedulingProjects,
		)
	}
	if perProject < 1 || perProject > ports.MaxSchedulingCandidatesProject {
		return nil, fmt.Errorf(
			"per-project candidates must be between 1 and %d",
			ports.MaxSchedulingCandidatesProject,
		)
	}
	now := time.Now().UTC()
	nowText := now.Format(time.RFC3339Nano)
	rows, err := s.db.QueryContext(ctx, s.bind(
		`SELECT p.installation_id,p.id,p.scheduling_weight
		 FROM projects p
		 WHERE EXISTS (
		   SELECT 1 FROM jobs j
		   JOIN quota_reservations qr
		     ON qr.job_id=j.id
		    AND qr.installation_id=p.installation_id
		    AND qr.state IN ('INTENT','RESERVED')
		   LEFT JOIN scheduler_claims c ON c.job_id=j.id AND c.expires_at>?
		   WHERE j.project_id=p.id AND j.desired_state='QUEUED'
		     AND j.observed_state IN ('CREATED','PAUSED')
		     AND j.management_mode='MANAGED'
		     AND j.sync_status NOT IN ('MISSING','OUT_OF_SCOPE','CONFLICTED')
		     AND j.archived_at IS NULL
		     AND (j.next_reconcile_at IS NULL OR j.next_reconcile_at<=?)
		     AND (j.scheduled_for IS NULL OR j.scheduled_for='' OR j.scheduled_for<=?)
		     AND c.job_id IS NULL
		 )
		 ORDER BY p.installation_id,p.id LIMIT ?`,
	), nowText, nowText, nowText, maxProjects)
	if err != nil {
		return nil, fmt.Errorf("list scheduling projects: %w", err)
	}
	defer func() { _ = rows.Close() }()
	projects := make([]ports.SchedulingProject, 0, maxProjects)
	for rows.Next() {
		var project ports.SchedulingProject
		if err := rows.Scan(
			&project.InstallationID, &project.ProjectID, &project.Weight,
		); err != nil {
			return nil, fmt.Errorf("scan scheduling project: %w", err)
		}
		projects = append(projects, project)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read scheduling projects: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close scheduling projects: %w", err)
	}

	active := projects[:0]
	for index := range projects {
		project := projects[index]
		candidates, err := s.projectSchedulingCandidates(
			ctx, now, project, perProject,
		)
		if err != nil {
			return nil, err
		}
		if len(candidates) == 0 {
			continue
		}
		project.Candidates = candidates
		active = append(active, project)
	}
	return active, nil
}

func (s *Store) projectSchedulingCandidates(
	ctx context.Context,
	now time.Time,
	project ports.SchedulingProject,
	limit int,
) ([]ports.SchedulingCandidate, error) {
	priorityLimit := (limit + 1) / 2
	oldestLimit := limit - priorityLimit
	jobs, err := s.readProjectCandidateJobs(
		ctx, now, project.ProjectID,
		"j.priority DESC,j.position,j.created_at,j.id", priorityLimit,
	)
	if err != nil {
		return nil, err
	}
	if oldestLimit > 0 {
		oldest, err := s.readProjectCandidateJobs(
			ctx, now, project.ProjectID,
			"j.created_at,j.position,j.priority DESC,j.id", oldestLimit,
		)
		if err != nil {
			return nil, err
		}
		seen := make(map[string]struct{}, len(jobs))
		for _, job := range jobs {
			seen[job.ID] = struct{}{}
		}
		for _, job := range oldest {
			if _, duplicate := seen[job.ID]; duplicate {
				continue
			}
			jobs = append(jobs, job)
		}
	}
	candidates := make([]ports.SchedulingCandidate, 0, len(jobs))
	for _, job := range jobs {
		candidate, err := s.schedulingCandidate(ctx, now, project, job)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	return candidates, nil
}

func (s *Store) readProjectCandidateJobs(
	ctx context.Context,
	now time.Time,
	projectID domain.ProjectID,
	order string,
	limit int,
) ([]domain.Job, error) {
	nowText := now.Format(time.RFC3339Nano)
	query := s.bind(
		`SELECT ` + prefixedJobColumns("j") + `
		 FROM jobs j
		 JOIN quota_reservations qr
		   ON qr.job_id=j.id AND qr.state IN ('INTENT','RESERVED')
		 LEFT JOIN scheduler_claims c ON c.job_id=j.id AND c.expires_at>?
		 WHERE j.project_id=? AND j.desired_state='QUEUED'
		   AND j.observed_state IN ('CREATED','PAUSED')
		   AND j.management_mode='MANAGED'
		   AND j.sync_status NOT IN ('MISSING','OUT_OF_SCOPE','CONFLICTED')
		   AND j.archived_at IS NULL
		   AND (j.next_reconcile_at IS NULL OR j.next_reconcile_at<=?)
		   AND (j.scheduled_for IS NULL OR j.scheduled_for='' OR j.scheduled_for<=?)
		   AND c.job_id IS NULL
		 ORDER BY ` + order + ` LIMIT ?`)
	rows, err := s.db.QueryContext(
		ctx, query, nowText, projectID, nowText, nowText, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list project scheduling candidates: %w", err)
	}
	defer func() { _ = rows.Close() }()
	jobs := make([]domain.Job, 0, limit)
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("scan scheduling candidate: %w", err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read scheduling candidates: %w", err)
	}
	return jobs, nil
}

func (s *Store) schedulingCandidate(
	ctx context.Context,
	now time.Time,
	project ports.SchedulingProject,
	job domain.Job,
) (ports.SchedulingCandidate, error) {
	candidate := ports.SchedulingCandidate{
		Job: job, Lane: scheduler.LaneStandard,
	}
	if now.After(job.CreatedAt) {
		candidate.Age = uint64(now.Sub(job.CreatedAt) / time.Minute)
	}
	candidate.EmergencyRequested = requestsEmergencyLane(job.Template)
	if candidate.EmergencyRequested && job.CreatorPrincipalID != "" {
		actor := domain.Actor{
			PrincipalID: job.CreatorPrincipalID, InstallationID: project.InstallationID,
		}
		err := s.Authorize(
			ctx, actor, domain.PermissionQueueGlobalReorder,
			domain.AuthorizationScope{InstallationID: project.InstallationID},
		)
		if err == nil {
			candidate.Lane = scheduler.LaneEmergency
			candidate.EmergencyAuthorized = true
			candidate.EmergencyAuthorization = string(domain.PermissionQueueGlobalReorder)
		} else if !errors.Is(err, domain.ErrAccessDenied) {
			return ports.SchedulingCandidate{}, fmt.Errorf(
				"resolve emergency scheduling authorization: %w", err,
			)
		}
	}
	return candidate, nil
}

func requestsEmergencyLane(template json.RawMessage) bool {
	var manifest struct {
		Metadata struct {
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
	}
	if json.Unmarshal(template, &manifest) != nil {
		return false
	}
	return strings.EqualFold(
		strings.TrimSpace(manifest.Metadata.Annotations[emergencyLaneAnnotation]), "true",
	)
}

func (s *Store) ProjectScheduling(
	ctx context.Context,
	installationID domain.InstallationID,
	projectIDs []domain.ProjectID,
) ([]ports.ProjectScheduling, error) {
	if err := validateProjectBatch(installationID, projectIDs, false); err != nil {
		return nil, err
	}
	query, arguments := projectBatchQuery(
		`SELECT id,scheduling_weight,scheduling_version
		 FROM projects WHERE installation_id=? AND id IN (%s) ORDER BY id`,
		installationID, projectIDs,
	)
	rows, err := s.db.QueryContext(ctx, s.bind(query), arguments...)
	if err != nil {
		return nil, fmt.Errorf("read project scheduling: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make([]ports.ProjectScheduling, 0, len(projectIDs))
	for rows.Next() {
		var project ports.ProjectScheduling
		if err := rows.Scan(&project.ProjectID, &project.Weight, &project.Version); err != nil {
			return nil, fmt.Errorf("scan project scheduling: %w", err)
		}
		result = append(result, project)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read project scheduling rows: %w", err)
	}
	if len(result) != len(projectIDs) {
		return nil, ports.ErrNotFound
	}
	return result, nil
}

func (s *Store) CompareAndSetProjectWeight(
	ctx context.Context,
	installationID domain.InstallationID,
	projectID domain.ProjectID,
	expectedVersion uint64,
	weight uint64,
) (ports.ProjectScheduling, error) {
	if installationID == "" || projectID == "" {
		return ports.ProjectScheduling{}, errors.New("installation ID and project ID are required")
	}
	if weight == 0 || weight > 1_000_000 {
		return ports.ProjectScheduling{}, errors.New("project weight must be between 1 and 1000000")
	}
	if expectedVersion >= math.MaxInt64 {
		return ports.ProjectScheduling{}, errors.New("project scheduling version exceeds durable range")
	}
	result, err := s.db.ExecContext(ctx, s.bind(
		`UPDATE projects
		 SET scheduling_weight=?,scheduling_version=scheduling_version+1
		 WHERE installation_id=? AND id=? AND scheduling_version=?`,
	), weight, installationID, projectID, expectedVersion)
	if err != nil {
		return ports.ProjectScheduling{}, fmt.Errorf("update project weight: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return ports.ProjectScheduling{}, err
	}
	if changed != 1 {
		return ports.ProjectScheduling{}, ports.ErrConflict
	}
	return ports.ProjectScheduling{
		ProjectID: projectID, Weight: weight, Version: expectedVersion + 1,
	}, nil
}

func (s *Store) FairnessState(
	ctx context.Context,
	installationID domain.InstallationID,
	projectIDs []domain.ProjectID,
) (ports.FairnessState, error) {
	if err := validateProjectBatch(installationID, projectIDs, true); err != nil {
		return ports.FairnessState{}, err
	}
	result := ports.FairnessState{State: scheduler.State{
		Deficits: make(map[string]uint64, len(projectIDs)),
	}}
	if len(projectIDs) == 0 {
		var next sql.NullString
		err := s.db.QueryRowContext(ctx, s.bind(
			`SELECT version,next_project_id
			 FROM scheduler_fairness_state WHERE installation_id=?`,
		), installationID).Scan(&result.Version, &next)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return ports.FairnessState{}, fmt.Errorf("read fairness cursor: %w", err)
		}
		if next.Valid {
			result.State.NextProjectID = next.String
		}
		return result, nil
	}

	query, arguments := projectBatchQuery(
		`SELECT COALESCE(fs.version,0),COALESCE(fs.next_project_id,''),
		        p.id,COALESCE(d.deficit,0)
		 FROM projects p
		 LEFT JOIN scheduler_fairness_state fs
		   ON fs.installation_id=p.installation_id
		 LEFT JOIN scheduler_project_deficits d
		   ON d.installation_id=p.installation_id AND d.project_id=p.id
		 WHERE p.installation_id=? AND p.id IN (%s) ORDER BY p.id`,
		installationID, projectIDs,
	)
	rows, err := s.db.QueryContext(ctx, s.bind(query), arguments...)
	if err != nil {
		return ports.FairnessState{}, fmt.Errorf("read fairness deficits: %w", err)
	}
	defer func() { _ = rows.Close() }()
	found := 0
	for rows.Next() {
		var projectID string
		var deficit uint64
		if err := rows.Scan(
			&result.Version, &result.State.NextProjectID, &projectID, &deficit,
		); err != nil {
			return ports.FairnessState{}, fmt.Errorf("scan fairness deficit: %w", err)
		}
		result.State.Deficits[projectID] = deficit
		found++
	}
	if err := rows.Err(); err != nil {
		return ports.FairnessState{}, fmt.Errorf("read fairness deficit rows: %w", err)
	}
	if found != len(projectIDs) {
		return ports.FairnessState{}, ports.ErrNotFound
	}
	return result, nil
}

func (s *Store) CommitSchedulingDecision(
	ctx context.Context,
	installationID domain.InstallationID,
	expectedVersion uint64,
	next scheduler.State,
	decision ports.AdmissionDecision,
) (ports.FairnessState, error) {
	if err := validateSchedulingCommit(installationID, next, decision); err != nil {
		return ports.FairnessState{}, err
	}
	if expectedVersion >= math.MaxInt64 {
		return ports.FairnessState{}, errors.New("fairness version exceeds durable range")
	}
	now := decision.CreatedAt.UTC()
	if decision.CreatedAt.IsZero() {
		now = time.Now().UTC()
	}
	nowText := now.Format(time.RFC3339Nano)
	nextVersion := expectedVersion + 1

	err := s.transaction(ctx, func(tx *sql.Tx) error {
		if err := s.commitFairnessStateTx(
			ctx, tx, installationID, expectedVersion, next, nowText,
		); err != nil {
			return err
		}
		if err := s.insertAdmissionDecision(ctx, tx, installationID, decision, nowText); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return ports.FairnessState{}, err
	}
	return ports.FairnessState{Version: nextVersion, State: next}, nil
}

func (s *Store) CommitRuntimeAdmission(
	ctx context.Context,
	request ports.RuntimeAdmissionRequest,
) (ports.RuntimeAdmissionResult, error) {
	if request.ClaimTTL <= 0 || request.RejectionRetry <= 0 {
		return ports.RuntimeAdmissionResult{}, errors.New(
			"claim TTL and rejection retry must be positive",
		)
	}
	if err := validateSchedulingCommit(
		request.InstallationID, request.NextFairnessState, request.Decision,
	); err != nil {
		return ports.RuntimeAdmissionResult{}, err
	}
	if len(request.Policy.Applied) == 0 ||
		request.Decision.Policy != request.Policy.Applied[len(request.Policy.Applied)-1] {
		return ports.RuntimeAdmissionResult{}, errors.New(
			"admission decision does not identify the effective policy",
		)
	}
	now := time.Now().UTC()
	nowText := now.Format(time.RFC3339Nano)
	var result ports.RuntimeAdmissionResult
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		if err := s.authorizeMutationTx(ctx, tx, request.Authority); err != nil {
			return err
		}
		if err := s.validateAppliedPolicyTx(
			ctx, tx, request.InstallationID, request.Policy,
		); err != nil {
			return err
		}
		eligible, err := s.runtimeCandidateEligibleTx(
			ctx, tx, request.InstallationID, request.Decision.Scheduling.JobID, nowText,
		)
		if err != nil {
			return err
		}
		if !eligible {
			return ports.ErrConflict
		}
		if err := s.commitFairnessStateTx(
			ctx, tx, request.InstallationID, request.ExpectedFairnessVersion,
			request.NextFairnessState, nowText,
		); err != nil {
			return err
		}
		result.Quota, err = s.admitJobQuotaTx(
			ctx, tx, request.InstallationID,
			request.Decision.Scheduling.JobID, request.Policy,
		)
		if err != nil {
			return err
		}
		result.Fairness = ports.FairnessState{
			Version: request.ExpectedFairnessVersion + 1,
			State:   request.NextFairnessState,
		}
		if result.Quota.Rejection != nil {
			return s.recordQuotaRejectionTx(
				ctx, tx, request.Decision.Scheduling.JobID,
				*result.Quota.Rejection, now.Add(request.RejectionRetry),
			)
		}
		claim, err := tx.ExecContext(ctx, s.bind(
			`INSERT INTO scheduler_claims(job_id,holder,expires_at)
			 VALUES(?,?,?) ON CONFLICT(job_id) DO UPDATE SET
			 holder=excluded.holder,expires_at=excluded.expires_at
			 WHERE scheduler_claims.expires_at<=? OR scheduler_claims.holder=excluded.holder`,
		), request.Decision.Scheduling.JobID, request.Authority.Holder,
			now.Add(request.ClaimTTL).Format(time.RFC3339Nano), nowText)
		if err != nil {
			return fmt.Errorf("claim admitted Job: %w", err)
		}
		claimed, err := claim.RowsAffected()
		if err != nil {
			return err
		}
		if claimed != 1 {
			return ports.ErrConflict
		}
		request.Decision.QuotaReservationKey = result.Quota.Reservation.IdempotencyKey
		if err := s.insertAdmissionDecision(
			ctx, tx, request.InstallationID, request.Decision, nowText,
		); err != nil {
			return err
		}
		return nil
	})
	return result, err
}

func (s *Store) AbandonRuntimeAdmission(
	ctx context.Context,
	authority leadership.Authority,
	installationID domain.InstallationID,
	jobID string,
	reason string,
) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if err := s.authorizeMutationTx(ctx, tx, authority); err != nil {
			return err
		}
		if err := s.requeueJobQuotaTx(ctx, tx, installationID, jobID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, s.bind(
			`DELETE FROM scheduler_claims WHERE job_id=? AND holder=?`,
		), jobID, authority.Holder); err != nil {
			return err
		}
		now := time.Now().UTC()
		_, err := tx.ExecContext(ctx, s.bind(
			`UPDATE jobs SET last_error=?,last_error_code='admission.failed',
			 last_error_remediation='RETRY_AUTOMATICALLY',next_reconcile_at=?,
			 updated_at=? WHERE id=? AND desired_state='QUEUED'`,
		), sanitizeDiagnostic(reason), now.Add(2*time.Second).Format(time.RFC3339Nano),
			now.Format(time.RFC3339Nano), jobID)
		return err
	})
}

func (s *Store) validateAppliedPolicyTx(
	ctx context.Context,
	tx *sql.Tx,
	installationID domain.InstallationID,
	effective policyquota.EffectivePolicy,
) error {
	for _, expected := range effective.Applied {
		current, found, err := s.currentPolicyTx(ctx, tx, installationID, expected.Scope)
		if err != nil {
			return err
		}
		if !found || current.Ref != expected {
			return ports.ErrConflict
		}
	}
	return nil
}

func (s *Store) runtimeCandidateEligibleTx(
	ctx context.Context,
	tx *sql.Tx,
	installationID domain.InstallationID,
	jobID string,
	nowText string,
) (bool, error) {
	var count int
	err := tx.QueryRowContext(ctx, s.bind(
		`SELECT COUNT(*) FROM jobs j
		 JOIN projects p ON p.id=j.project_id AND p.installation_id=?
		 LEFT JOIN scheduler_claims c ON c.job_id=j.id AND c.expires_at>?
		 WHERE j.id=? AND j.desired_state='QUEUED'
		   AND j.observed_state IN ('CREATED','PAUSED')
		   AND j.management_mode='MANAGED'
		   AND j.sync_status NOT IN ('MISSING','OUT_OF_SCOPE','CONFLICTED')
		   AND j.archived_at IS NULL
		   AND (j.next_reconcile_at IS NULL OR j.next_reconcile_at<=?)
		   AND (j.scheduled_for IS NULL OR j.scheduled_for='' OR j.scheduled_for<=?)
		   AND c.job_id IS NULL`,
	), installationID, nowText, jobID, nowText, nowText).Scan(&count)
	return count == 1, err
}

func (s *Store) recordQuotaRejectionTx(
	ctx context.Context,
	tx *sql.Tx,
	jobID string,
	rejection policyquota.Rejection,
	retryAt time.Time,
) error {
	message := fmt.Sprintf(
		"%s: current=%d limit=%d", rejection.Metric, rejection.Current, rejection.Limit,
	)
	_, err := tx.ExecContext(ctx, s.bind(
		`UPDATE jobs SET last_error=?,last_error_code=?,last_error_remediation=?,
		 next_reconcile_at=?,updated_at=?
		 WHERE id=? AND desired_state='QUEUED'`,
	), sanitizeDiagnostic(message), rejection.Reason, rejection.Remediation,
		retryAt.UTC().Format(time.RFC3339Nano),
		time.Now().UTC().Format(time.RFC3339Nano), jobID)
	return err
}

func (s *Store) commitFairnessStateTx(
	ctx context.Context,
	tx *sql.Tx,
	installationID domain.InstallationID,
	expectedVersion uint64,
	next scheduler.State,
	nowText string,
) error {
	if err := s.ensureFairnessProjects(ctx, tx, installationID, next); err != nil {
		return err
	}
	nextVersion := expectedVersion + 1
	if expectedVersion == 0 {
		result, err := tx.ExecContext(ctx, s.bind(
			`INSERT INTO scheduler_fairness_state(
			 installation_id,version,next_project_id,updated_at
			 ) VALUES(?,?,?,?) ON CONFLICT(installation_id) DO NOTHING`,
		), installationID, nextVersion, nullableString(next.NextProjectID), nowText)
		if err != nil {
			return fmt.Errorf("create fairness state: %w", err)
		}
		created, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if created != 1 {
			return ports.ErrConflict
		}
	} else {
		result, err := tx.ExecContext(ctx, s.bind(
			`UPDATE scheduler_fairness_state
			 SET version=?,next_project_id=?,updated_at=?
			 WHERE installation_id=? AND version=?`,
		), nextVersion, nullableString(next.NextProjectID), nowText,
			installationID, expectedVersion)
		if err != nil {
			return fmt.Errorf("advance fairness state: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return ports.ErrConflict
		}
	}
	for projectID, deficit := range next.Deficits {
		if _, err := tx.ExecContext(ctx, s.bind(
			`INSERT INTO scheduler_project_deficits(
			 installation_id,project_id,deficit,updated_at
			 ) VALUES(?,?,?,?) ON CONFLICT(installation_id,project_id) DO UPDATE SET
			 deficit=excluded.deficit,updated_at=excluded.updated_at`,
		), installationID, projectID, deficit, nowText); err != nil {
			return fmt.Errorf("write fairness deficit: %w", err)
		}
	}
	return nil
}

func (s *Store) AdmissionDecision(
	ctx context.Context,
	installationID domain.InstallationID,
	id string,
) (ports.AdmissionDecision, error) {
	var result ports.AdmissionDecision
	var createdAt string
	var emergencyRequested, emergencyAuthorized int
	result.InstallationID = installationID
	err := s.db.QueryRowContext(ctx, s.bind(
		`SELECT id,project_id,job_id,policy_id,policy_version,policy_scope_type,
		 COALESCE(policy_scope_project_id,''),COALESCE(policy_scope_namespace,''),
		 scheduling_policy_version,lane,project_weight,deficit_before,deficit_after,
		 base_priority,age,aging_step,effective_priority,
		 emergency_requested,emergency_authorized,COALESCE(emergency_authorization,''),
		 COALESCE(quota_reservation_key,''),decided_by,created_at
		 FROM admission_decisions WHERE installation_id=? AND id=?`,
	), installationID, id).Scan(
		&result.ID, &result.Scheduling.ProjectID, &result.Scheduling.JobID,
		&result.Policy.ID, &result.Policy.Version, &result.Policy.Scope.Kind,
		&result.Policy.Scope.Project, &result.Policy.Scope.Namespace,
		&result.Scheduling.AppliedPolicyVersion, &result.Scheduling.Basis.Lane,
		&result.Scheduling.Basis.ProjectWeight, &result.Scheduling.Basis.DeficitBefore,
		&result.Scheduling.Basis.DeficitAfter, &result.Scheduling.Basis.BasePriority,
		&result.Scheduling.Basis.Age, &result.Scheduling.Basis.AgingStep,
		&result.Scheduling.Basis.EffectivePriority,
		&emergencyRequested, &emergencyAuthorized,
		&result.Scheduling.Basis.EmergencyAuthorization, &result.QuotaReservationKey,
		&result.DecidedBy, &createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ports.AdmissionDecision{}, ports.ErrNotFound
	}
	if err != nil {
		return ports.AdmissionDecision{}, fmt.Errorf("read admission decision: %w", err)
	}
	result.Scheduling.Basis.EmergencyRequested = emergencyRequested == 1
	result.Scheduling.Basis.EmergencyAuthorized = emergencyAuthorized == 1
	result.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return ports.AdmissionDecision{}, fmt.Errorf("parse admission decision time: %w", err)
	}
	return result, nil
}

func (s *Store) ensureFairnessProjects(
	ctx context.Context,
	tx *sql.Tx,
	installationID domain.InstallationID,
	state scheduler.State,
) error {
	if len(state.Deficits) == 0 {
		return nil
	}
	ids := make([]domain.ProjectID, 0, len(state.Deficits))
	for id := range state.Deficits {
		ids = append(ids, domain.ProjectID(id))
	}
	query, arguments := projectBatchQuery(
		`SELECT COUNT(*) FROM projects WHERE installation_id=? AND id IN (%s)`,
		installationID, ids,
	)
	var count int
	if err := tx.QueryRowContext(ctx, s.bind(query), arguments...).Scan(&count); err != nil {
		return fmt.Errorf("validate fairness projects: %w", err)
	}
	if count != len(ids) {
		return ports.ErrNotFound
	}
	return nil
}

func (s *Store) insertAdmissionDecision(
	ctx context.Context,
	tx *sql.Tx,
	installationID domain.InstallationID,
	decision ports.AdmissionDecision,
	createdAt string,
) error {
	scheduled := decision.Scheduling
	basis := scheduled.Basis
	if _, err := tx.ExecContext(ctx, s.bind(
		`INSERT INTO admission_decisions(
		 id,installation_id,project_id,job_id,policy_id,policy_version,
		 policy_scope_type,policy_scope_project_id,policy_scope_namespace,
		 scheduling_policy_version,lane,project_weight,deficit_before,deficit_after,
		 base_priority,age,aging_step,effective_priority,
		 emergency_requested,emergency_authorized,emergency_authorization,
		 quota_reservation_key,
		 decided_by,created_at
		 ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
	), decision.ID, installationID, scheduled.ProjectID, scheduled.JobID,
		decision.Policy.ID, decision.Policy.Version, decision.Policy.Scope.Kind,
		nullableString(decision.Policy.Scope.Project), nullableString(decision.Policy.Scope.Namespace),
		scheduled.AppliedPolicyVersion, basis.Lane, basis.ProjectWeight,
		basis.DeficitBefore, basis.DeficitAfter, basis.BasePriority, basis.Age,
		basis.AgingStep, basis.EffectivePriority,
		boolInt(basis.EmergencyRequested), boolInt(basis.EmergencyAuthorized),
		nullableString(basis.EmergencyAuthorization),
		nullableString(decision.QuotaReservationKey),
		decision.DecidedBy, createdAt); err != nil {
		return fmt.Errorf("write admission decision: %w", err)
	}
	return nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func validateProjectBatch(
	installationID domain.InstallationID,
	projectIDs []domain.ProjectID,
	allowEmpty bool,
) error {
	if installationID == "" {
		return errors.New("installation ID is required")
	}
	if (!allowEmpty && len(projectIDs) == 0) || len(projectIDs) > ports.MaxSchedulingProjects {
		return fmt.Errorf("project batch must contain between 1 and %d IDs", ports.MaxSchedulingProjects)
	}
	seen := make(map[domain.ProjectID]struct{}, len(projectIDs))
	for _, id := range projectIDs {
		if id == "" {
			return errors.New("project ID is required")
		}
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("duplicate project ID %q", id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

func validateSchedulingCommit(
	installationID domain.InstallationID,
	state scheduler.State,
	decision ports.AdmissionDecision,
) error {
	if installationID == "" || decision.ID == "" || decision.DecidedBy == "" {
		return errors.New("installation, decision, and decider IDs are required")
	}
	if decision.Policy.Version > math.MaxInt64 {
		return errors.New("admission policy version exceeds durable range")
	}
	if decision.InstallationID != "" && decision.InstallationID != installationID {
		return errors.New("admission decision installation does not match")
	}
	if len(state.Deficits) == 0 || len(state.Deficits) > ports.MaxSchedulingProjects {
		return fmt.Errorf("fairness state must contain between 1 and %d projects", ports.MaxSchedulingProjects)
	}
	if state.NextProjectID != "" {
		if _, exists := state.Deficits[state.NextProjectID]; !exists {
			return errors.New("fairness cursor must name a persisted project")
		}
	}
	for projectID, deficit := range state.Deficits {
		if projectID == "" || deficit > math.MaxInt64 {
			return errors.New("fairness deficit is outside the durable range")
		}
	}
	if decision.Scheduling.ProjectID == "" || decision.Scheduling.JobID == "" ||
		decision.Scheduling.AppliedPolicyVersion == "" ||
		decision.Policy.ID == "" || decision.Policy.Version == 0 {
		return errors.New("admission decision policy and scheduling identity are required")
	}
	if _, exists := state.Deficits[decision.Scheduling.ProjectID]; !exists {
		return errors.New("admitted project is absent from fairness state")
	}
	if err := validatePolicyScope(decision.Policy.Scope); err != nil {
		return err
	}
	basis := decision.Scheduling.Basis
	if (basis.Lane != scheduler.LaneStandard && basis.Lane != scheduler.LaneEmergency) ||
		basis.ProjectWeight == 0 || basis.ProjectWeight > math.MaxInt64 ||
		basis.DeficitBefore > math.MaxInt64 || basis.DeficitAfter > math.MaxInt64 ||
		basis.Age > math.MaxInt64 || basis.AgingStep <= 0 {
		return errors.New("admission decision basis is outside the durable range")
	}
	if basis.Lane == scheduler.LaneEmergency &&
		(!basis.EmergencyRequested || !basis.EmergencyAuthorized ||
			strings.TrimSpace(basis.EmergencyAuthorization) == "") {
		return errors.New("emergency admission lacks explicit authorization metadata")
	}
	return nil
}

func projectBatchQuery(
	template string,
	installationID domain.InstallationID,
	projectIDs []domain.ProjectID,
) (string, []any) {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(projectIDs)), ",")
	arguments := make([]any, 0, len(projectIDs)+1)
	arguments = append(arguments, installationID)
	for _, projectID := range projectIDs {
		arguments = append(arguments, projectID)
	}
	return fmt.Sprintf(template, placeholders), arguments
}
