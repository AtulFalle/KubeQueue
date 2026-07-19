package persistence

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain/policyquota"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/ports"
)

type quotaUsageRow struct {
	scope    policyquota.Scope
	counters policyquota.Counters
	version  uint64
}

func (s *Store) CreateJobWithQuota(
	ctx context.Context,
	submission ports.QuotaSubmission,
) (ports.QuotaSubmissionResult, error) {
	if err := submission.Job.Validate(); err != nil {
		return ports.QuotaSubmissionResult{}, err
	}
	if err := validateQuotaTarget(submission.InstallationID, submission.Target); err != nil {
		return ports.QuotaSubmissionResult{}, err
	}
	if submission.IdempotencyKey == "" {
		return ports.QuotaSubmissionResult{}, errors.New("idempotency key is required")
	}
	if submission.Job.IdempotencyKey == "" {
		submission.Job.IdempotencyKey = submission.IdempotencyKey
	}
	if err := validateEffectiveQuotaPolicy(submission.Policy, submission.Target); err != nil {
		return ports.QuotaSubmissionResult{}, err
	}
	if err := validateUsageForDatabase(submission.Demand); err != nil {
		return ports.QuotaSubmissionResult{}, err
	}

	var result ports.QuotaSubmissionResult
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		usageRows, err := s.lockQuotaUsage(
			ctx, tx, submission.InstallationID, submission.Target,
		)
		if err != nil {
			return err
		}
		usage := quotaUsageFromRows(usageRows)
		existing, _, existingTarget, found, err := s.quotaReservation(
			ctx, tx, submission.InstallationID, submission.IdempotencyKey, true,
		)
		if err != nil {
			return err
		}
		if found {
			result.Decision, err = policyquota.DecideReservation(
				submission.Policy, usage, policyquota.ReservationRequest{
					IdempotencyKey: submission.IdempotencyKey,
					JobID:          existing.JobID,
					Demand:         submission.Demand,
				}, &existing,
			)
			if err != nil || !result.Decision.Replay {
				return err
			}
			result.Job, err = scanJob(tx.QueryRowContext(ctx, s.bind(
				`SELECT `+jobColumns+` FROM jobs WHERE id=?`,
			), existing.JobID))
			if err != nil {
				return err
			}
			if existingTarget != submission.Target ||
				!sameJobIntent(result.Job, submission.Job) {
				result.Decision = idempotencyConflictDecision(submission.Policy, usage)
				result.Job = domain.Job{}
			}
			return nil
		}

		result.Job = newJobIntent(submission.Job)
		if err := s.resolveJobOwnershipTx(ctx, tx, submission.Job, &result.Job); err != nil {
			return err
		}
		if result.Job.ProjectID != domain.ProjectID(submission.Target.Project) ||
			result.Job.Namespace != submission.Target.Namespace {
			return ports.ErrNotFound
		}
		result.Decision, err = policyquota.DecideReservation(
			submission.Policy, usage, policyquota.ReservationRequest{
				IdempotencyKey: submission.IdempotencyKey,
				JobID:          result.Job.ID,
				Demand:         submission.Demand,
			}, nil,
		)
		if err != nil || !result.Decision.Accepted {
			result.Job = domain.Job{}
			return err
		}
		if err := s.insertJobIntentTx(ctx, tx, &result.Job); err != nil {
			return err
		}
		if err := s.updateQuotaUsage(
			ctx, tx, submission.InstallationID, usageRows, result.Decision.Usage,
		); err != nil {
			return err
		}
		if err := s.insertQuotaReservationTx(
			ctx, tx, submission.InstallationID, submission.Target,
			result.Decision.Reservation,
		); err != nil {
			return err
		}
		return s.appendTransactionalAudit(ctx, tx)
	})
	return result, err
}

func (s *Store) QuotaUsage(
	ctx context.Context,
	installationID domain.InstallationID,
	target policyquota.Scope,
) (policyquota.Usage, error) {
	if err := validateQuotaTarget(installationID, target); err != nil {
		return policyquota.Usage{}, err
	}
	scopes := quotaScopes(target)
	rowsByKey := make(map[string]quotaUsageRow, len(scopes))
	for _, scope := range scopes {
		rowsByKey[policyScopeKey(scope)] = quotaUsageRow{scope: scope}
	}
	result, err := s.db.QueryContext(ctx, s.bind(
		`SELECT scope_key,concurrent_jobs,queued_jobs,retained_jobs,version
		 FROM quota_usage
		 WHERE installation_id=? AND scope_key IN (?,?,?)`,
	), installationID, policyScopeKey(scopes[0]), policyScopeKey(scopes[1]),
		policyScopeKey(scopes[2]))
	if err != nil {
		return policyquota.Usage{}, fmt.Errorf("read quota usage: %w", err)
	}
	defer func() { _ = result.Close() }()
	for result.Next() {
		var key string
		row := quotaUsageRow{}
		if err := result.Scan(
			&key, &row.counters.Concurrent, &row.counters.Queued,
			&row.counters.Retained, &row.version,
		); err != nil {
			return policyquota.Usage{}, fmt.Errorf("scan quota usage: %w", err)
		}
		row.scope = rowsByKey[key].scope
		rowsByKey[key] = row
	}
	if err := result.Err(); err != nil {
		return policyquota.Usage{}, fmt.Errorf("read quota usage rows: %w", err)
	}
	rows := make([]quotaUsageRow, 0, len(scopes))
	for _, scope := range scopes {
		rows = append(rows, rowsByKey[policyScopeKey(scope)])
	}
	return quotaUsageFromRows(rows), nil
}

func (s *Store) ProjectQuotaUsage(
	ctx context.Context,
	installationID domain.InstallationID,
	projectID domain.ProjectID,
) (policyquota.Counters, error) {
	if installationID == "" || projectID == "" {
		return policyquota.Counters{}, errors.New("installation ID and project ID are required")
	}
	var counters policyquota.Counters
	err := s.db.QueryRowContext(ctx, s.bind(
		`SELECT concurrent_jobs,queued_jobs,retained_jobs
		 FROM quota_usage WHERE installation_id=? AND scope_key=?`,
	), installationID, policyScopeKey(policyquota.Scope{
		Kind: policyquota.ScopeProject, Project: string(projectID),
	})).Scan(&counters.Concurrent, &counters.Queued, &counters.Retained)
	if errors.Is(err, sql.ErrNoRows) {
		var exists int
		if projectErr := s.db.QueryRowContext(ctx, s.bind(
			`SELECT COUNT(*) FROM projects WHERE installation_id=? AND id=?`,
		), installationID, projectID).Scan(&exists); projectErr != nil {
			return policyquota.Counters{}, fmt.Errorf("check project quota scope: %w", projectErr)
		}
		if exists == 0 {
			return policyquota.Counters{}, ports.ErrNotFound
		}
		return policyquota.Counters{}, nil
	}
	if err != nil {
		return policyquota.Counters{}, fmt.Errorf("read project quota usage: %w", err)
	}
	return counters, nil
}

func (s *Store) ReserveQuota(
	ctx context.Context,
	installationID domain.InstallationID,
	target policyquota.Scope,
	policy policyquota.EffectivePolicy,
	request policyquota.ReservationRequest,
) (policyquota.ReservationDecision, error) {
	if err := validateQuotaTarget(installationID, target); err != nil {
		return policyquota.ReservationDecision{}, err
	}
	if err := validateEffectiveQuotaPolicy(policy, target); err != nil {
		return policyquota.ReservationDecision{}, err
	}
	if err := validateUsageForDatabase(request.Demand); err != nil {
		return policyquota.ReservationDecision{}, err
	}

	var decision policyquota.ReservationDecision
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		rows, err := s.lockQuotaUsage(ctx, tx, installationID, target)
		if err != nil {
			return err
		}
		usage := quotaUsageFromRows(rows)
		existing, _, _, found, err := s.quotaReservation(
			ctx, tx, installationID, request.IdempotencyKey, true,
		)
		if err != nil {
			return err
		}
		var existingPointer *policyquota.Reservation
		if found {
			existingPointer = &existing
		}
		decision, err = policyquota.DecideReservation(policy, usage, request, existingPointer)
		if err != nil || !decision.Accepted || decision.Replay {
			return err
		}
		if err := s.updateQuotaUsage(ctx, tx, installationID, rows, decision.Usage); err != nil {
			return err
		}
		return s.insertQuotaReservationTx(ctx, tx, installationID, target, decision.Reservation)
	})
	return decision, err
}

func (s *Store) AdmitJobQuota(
	ctx context.Context,
	installationID domain.InstallationID,
	jobID string,
	policy policyquota.EffectivePolicy,
) (policyquota.ReservationDecision, error) {
	if installationID == "" || jobID == "" {
		return policyquota.ReservationDecision{}, errors.New("installation ID and Job ID are required")
	}
	var decision policyquota.ReservationDecision
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		var err error
		decision, err = s.admitJobQuotaTx(ctx, tx, installationID, jobID, policy)
		return err
	})
	return decision, err
}

func (s *Store) admitJobQuotaTx(
	ctx context.Context,
	tx *sql.Tx,
	installationID domain.InstallationID,
	jobID string,
	policy policyquota.EffectivePolicy,
) (policyquota.ReservationDecision, error) {
	key, target, found, err := s.quotaReservationKeyByJob(ctx, tx, installationID, jobID)
	if err != nil {
		return policyquota.ReservationDecision{}, err
	}
	if !found {
		return policyquota.ReservationDecision{}, ports.ErrNotFound
	}
	if err := validateEffectiveQuotaPolicy(policy, target); err != nil {
		return policyquota.ReservationDecision{}, err
	}
	rows, err := s.lockQuotaUsage(ctx, tx, installationID, target)
	if err != nil {
		return policyquota.ReservationDecision{}, err
	}
	currentUsage := quotaUsageFromRows(rows)
	current, version, _, found, err := s.quotaReservation(
		ctx, tx, installationID, key, true,
	)
	if err != nil {
		return policyquota.ReservationDecision{}, err
	}
	if !found {
		return policyquota.ReservationDecision{}, ports.ErrConflict
	}
	if current.State == policyquota.ReservationReserved {
		return policyquota.ReservationDecision{
			Accepted: true, Usage: currentUsage, Reservation: current, Replay: true,
		}, nil
	}
	if current.State != policyquota.ReservationIntent {
		return policyquota.ReservationDecision{}, policyquota.ErrInvalidReservationState
	}
	base, err := currentUsage.Release(current.Demand)
	if err != nil {
		return policyquota.ReservationDecision{}, err
	}
	admittedDemand := concurrentDemand(current.Demand)
	decision, err := policyquota.DecideReservation(
		policy, base, policyquota.ReservationRequest{
			IdempotencyKey: current.IdempotencyKey,
			JobID:          current.JobID,
			Demand:         admittedDemand,
		}, nil,
	)
	if err != nil || !decision.Accepted {
		return decision, err
	}
	decision.Reservation, err = decision.Reservation.MarkReserved()
	if err != nil {
		return policyquota.ReservationDecision{}, err
	}
	if err := s.updateQuotaUsage(ctx, tx, installationID, rows, decision.Usage); err != nil {
		return policyquota.ReservationDecision{}, err
	}
	encoded, err := json.Marshal(decision.Reservation.Demand)
	if err != nil {
		return policyquota.ReservationDecision{}, err
	}
	ref := decision.Reservation.Policy
	update, err := tx.ExecContext(ctx, s.bind(
		`UPDATE quota_reservations
		 SET demand=?,state=?,policy_id=?,policy_version=?,policy_scope_type=?,
		     policy_scope_project_id=?,policy_scope_namespace=?,
		     version=version+1,updated_at=?
		 WHERE installation_id=? AND idempotency_key=? AND version=? AND state='INTENT'`,
	), string(encoded), decision.Reservation.State, ref.ID, ref.Version, ref.Scope.Kind,
		nullableString(ref.Scope.Project), nullableString(ref.Scope.Namespace),
		time.Now().UTC().Format(time.RFC3339Nano),
		installationID, key, version)
	if err != nil {
		return policyquota.ReservationDecision{}, err
	}
	changed, err := update.RowsAffected()
	if err != nil {
		return policyquota.ReservationDecision{}, err
	}
	if changed != 1 {
		return policyquota.ReservationDecision{}, ports.ErrConflict
	}
	return decision, nil
}

func (s *Store) requeueJobQuotaTx(
	ctx context.Context,
	tx *sql.Tx,
	installationID domain.InstallationID,
	jobID string,
) error {
	key, target, found, err := s.quotaReservationKeyByJob(ctx, tx, installationID, jobID)
	if err != nil || !found {
		return err
	}
	rows, err := s.lockQuotaUsage(ctx, tx, installationID, target)
	if err != nil {
		return err
	}
	current, version, _, found, err := s.quotaReservation(
		ctx, tx, installationID, key, true,
	)
	if err != nil || !found {
		return err
	}
	if current.State == policyquota.ReservationIntent {
		return nil
	}
	if current.State != policyquota.ReservationReserved {
		return policyquota.ErrInvalidReservationState
	}
	base, err := quotaUsageFromRows(rows).Release(current.Demand)
	if err != nil {
		return err
	}
	queued := queuedDemand(current.Demand)
	next, err := base.Add(queued)
	if err != nil {
		return err
	}
	if err := s.updateQuotaUsage(ctx, tx, installationID, rows, next); err != nil {
		return err
	}
	current.Demand = queued
	encoded, err := json.Marshal(current.Demand)
	if err != nil {
		return err
	}
	update, err := tx.ExecContext(ctx, s.bind(
		`UPDATE quota_reservations
		 SET demand=?,state='INTENT',version=version+1,updated_at=?
		 WHERE installation_id=? AND idempotency_key=? AND version=? AND state='RESERVED'`,
	), string(encoded), time.Now().UTC().Format(time.RFC3339Nano),
		installationID, key, version)
	if err != nil {
		return err
	}
	changed, err := update.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ports.ErrConflict
	}
	return nil
}

func (s *Store) ReleaseJobQuota(
	ctx context.Context,
	installationID domain.InstallationID,
	jobID string,
	cause policyquota.ReleaseCause,
) (policyquota.Reservation, policyquota.Usage, error) {
	var reservation policyquota.Reservation
	var usage policyquota.Usage
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		var found bool
		var err error
		reservation, usage, found, err = s.releaseJobQuotaTx(
			ctx, tx, installationID, jobID, cause, true,
		)
		if err != nil {
			return err
		}
		if !found {
			return ports.ErrNotFound
		}
		return nil
	})
	return reservation, usage, err
}

func (s *Store) MarkQuotaReserved(
	ctx context.Context,
	installationID domain.InstallationID,
	idempotencyKey string,
) (policyquota.Reservation, error) {
	var updated policyquota.Reservation
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		current, version, _, found, err := s.quotaReservation(
			ctx, tx, installationID, idempotencyKey, true,
		)
		if err != nil {
			return err
		}
		if !found {
			return ports.ErrNotFound
		}
		updated, err = current.MarkReserved()
		if err != nil {
			return err
		}
		if updated.State == current.State {
			return nil
		}
		result, err := tx.ExecContext(ctx, s.bind(
			`UPDATE quota_reservations SET state=?,version=version+1,updated_at=?
			 WHERE installation_id=? AND idempotency_key=? AND version=? AND state=?`,
		), updated.State, time.Now().UTC().Format(time.RFC3339Nano),
			installationID, idempotencyKey, version, current.State)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return ports.ErrConflict
		}
		return nil
	})
	return updated, err
}

func (s *Store) ReleaseQuota(
	ctx context.Context,
	installationID domain.InstallationID,
	idempotencyKey string,
	cause policyquota.ReleaseCause,
) (policyquota.Reservation, policyquota.Usage, error) {
	var released policyquota.Reservation
	var next policyquota.Usage
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		current, version, target, found, err := s.quotaReservation(
			ctx, tx, installationID, idempotencyKey, true,
		)
		if err != nil {
			return err
		}
		if !found {
			return ports.ErrNotFound
		}
		rows, err := s.lockQuotaUsage(ctx, tx, installationID, target)
		if err != nil {
			return err
		}
		usage := quotaUsageFromRows(rows)
		released, next, err = current.Release(usage, cause)
		if err != nil || current.State == policyquota.ReservationReleased {
			return err
		}
		if err := s.updateQuotaUsage(ctx, tx, installationID, rows, next); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, s.bind(
			`UPDATE quota_reservations
			 SET state=?,release_cause=?,version=version+1,updated_at=?
			 WHERE installation_id=? AND idempotency_key=? AND version=? AND state=?`,
		), released.State, released.ReleaseCause, time.Now().UTC().Format(time.RFC3339Nano),
			installationID, idempotencyKey, version, current.State)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return ports.ErrConflict
		}
		return nil
	})
	return released, next, err
}

func (s *Store) lockQuotaUsage(
	ctx context.Context,
	tx *sql.Tx,
	installationID domain.InstallationID,
	target policyquota.Scope,
) ([]quotaUsageRow, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, scope := range quotaScopes(target) {
		if _, err := tx.ExecContext(ctx, s.bind(
			`INSERT INTO quota_usage(
			 installation_id,scope_key,scope_type,project_id,namespace,updated_at
			 ) VALUES(?,?,?,?,?,?) ON CONFLICT(installation_id,scope_key) DO NOTHING`,
		), installationID, policyScopeKey(scope), scope.Kind,
			nullableString(scope.Project), nullableString(scope.Namespace), now); err != nil {
			return nil, fmt.Errorf("initialize quota usage: %w", err)
		}
	}

	rows := make([]quotaUsageRow, 0, 3)
	for _, scope := range quotaScopes(target) {
		query := `SELECT concurrent_jobs,queued_jobs,retained_jobs,version
			FROM quota_usage WHERE installation_id=? AND scope_key=?`
		if s.postgres {
			query += ` FOR UPDATE`
		}
		row := quotaUsageRow{scope: scope}
		if err := tx.QueryRowContext(ctx, s.bind(query),
			installationID, policyScopeKey(scope)).Scan(
			&row.counters.Concurrent, &row.counters.Queued,
			&row.counters.Retained, &row.version,
		); err != nil {
			return nil, fmt.Errorf("lock quota usage: %w", err)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func (s *Store) updateQuotaUsage(
	ctx context.Context,
	tx *sql.Tx,
	installationID domain.InstallationID,
	current []quotaUsageRow,
	next policyquota.Usage,
) error {
	counters := []policyquota.Counters{next.Global, next.Project, next.Namespace}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for index, row := range current {
		result, err := tx.ExecContext(ctx, s.bind(
			`UPDATE quota_usage
			 SET concurrent_jobs=?,queued_jobs=?,retained_jobs=?,version=version+1,updated_at=?
			 WHERE installation_id=? AND scope_key=? AND version=?`,
		), counters[index].Concurrent, counters[index].Queued, counters[index].Retained,
			now, installationID, policyScopeKey(row.scope), row.version)
		if err != nil {
			return fmt.Errorf("update quota usage: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return ports.ErrConflict
		}
	}
	return nil
}

func (s *Store) quotaReservation(
	ctx context.Context,
	tx *sql.Tx,
	installationID domain.InstallationID,
	idempotencyKey string,
	lock bool,
) (policyquota.Reservation, uint64, policyquota.Scope, bool, error) {
	query := `SELECT job_id,project_id,namespace,policy_id,policy_version,policy_scope_type,
		COALESCE(policy_scope_project_id,''),COALESCE(policy_scope_namespace,''),
		demand,state,COALESCE(release_cause,''),version
		FROM quota_reservations WHERE installation_id=? AND idempotency_key=?`
	if lock && s.postgres {
		query += ` FOR UPDATE`
	}
	var reservation policyquota.Reservation
	target := policyquota.Scope{Kind: policyquota.ScopeNamespace}
	var encoded string
	var version uint64
	reservation.IdempotencyKey = idempotencyKey
	err := tx.QueryRowContext(ctx, s.bind(query), installationID, idempotencyKey).Scan(
		&reservation.JobID, &target.Project, &target.Namespace,
		&reservation.Policy.ID, &reservation.Policy.Version,
		&reservation.Policy.Scope.Kind, &reservation.Policy.Scope.Project,
		&reservation.Policy.Scope.Namespace, &encoded, &reservation.State,
		&reservation.ReleaseCause, &version,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return policyquota.Reservation{}, 0, policyquota.Scope{}, false, nil
	}
	if err != nil {
		return policyquota.Reservation{}, 0, policyquota.Scope{}, false, fmt.Errorf("read quota reservation: %w", err)
	}
	if err := json.Unmarshal([]byte(encoded), &reservation.Demand); err != nil {
		return policyquota.Reservation{}, 0, policyquota.Scope{}, false, fmt.Errorf("decode quota demand: %w", err)
	}
	return reservation, version, target, true, nil
}

func (s *Store) quotaReservationKeyByJob(
	ctx context.Context,
	tx *sql.Tx,
	installationID domain.InstallationID,
	jobID string,
) (string, policyquota.Scope, bool, error) {
	var key string
	target := policyquota.Scope{Kind: policyquota.ScopeNamespace}
	err := tx.QueryRowContext(ctx, s.bind(
		`SELECT idempotency_key,project_id,namespace
		 FROM quota_reservations WHERE installation_id=? AND job_id=?`,
	), installationID, jobID).Scan(&key, &target.Project, &target.Namespace)
	if errors.Is(err, sql.ErrNoRows) {
		return "", policyquota.Scope{}, false, nil
	}
	if err != nil {
		return "", policyquota.Scope{}, false, fmt.Errorf("find Job quota reservation: %w", err)
	}
	return key, target, true, nil
}

func (s *Store) insertQuotaReservationTx(
	ctx context.Context,
	tx *sql.Tx,
	installationID domain.InstallationID,
	target policyquota.Scope,
	reservation policyquota.Reservation,
) error {
	demand, err := json.Marshal(reservation.Demand)
	if err != nil {
		return fmt.Errorf("encode quota demand: %w", err)
	}
	ref := reservation.Policy
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, s.bind(
		`INSERT INTO quota_reservations(
		 installation_id,idempotency_key,job_id,project_id,namespace,
		 policy_id,policy_version,policy_scope_type,policy_scope_project_id,
		 policy_scope_namespace,demand,state,release_cause,version,created_at,updated_at
		 ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,NULL,1,?,?)`,
	), installationID, reservation.IdempotencyKey, reservation.JobID,
		target.Project, target.Namespace, ref.ID, ref.Version, ref.Scope.Kind,
		nullableString(ref.Scope.Project), nullableString(ref.Scope.Namespace),
		string(demand), reservation.State, now, now); err != nil {
		return fmt.Errorf("write quota reservation: %w", err)
	}
	return nil
}

func (s *Store) releaseJobQuotaTx(
	ctx context.Context,
	tx *sql.Tx,
	installationID domain.InstallationID,
	jobID string,
	cause policyquota.ReleaseCause,
	preserveRetained bool,
) (policyquota.Reservation, policyquota.Usage, bool, error) {
	key, target, found, err := s.quotaReservationKeyByJob(
		ctx, tx, installationID, jobID,
	)
	if err != nil || !found {
		return policyquota.Reservation{}, policyquota.Usage{}, found, err
	}
	rows, err := s.lockQuotaUsage(ctx, tx, installationID, target)
	if err != nil {
		return policyquota.Reservation{}, policyquota.Usage{}, true, err
	}
	currentUsage := quotaUsageFromRows(rows)
	current, version, _, found, err := s.quotaReservation(
		ctx, tx, installationID, key, true,
	)
	if err != nil || !found {
		return policyquota.Reservation{}, policyquota.Usage{}, found, err
	}
	if current.State == policyquota.ReservationReleased {
		return current, currentUsage, true, nil
	}
	released, next, err := current.Release(currentUsage, cause)
	if err != nil {
		return policyquota.Reservation{}, policyquota.Usage{}, true, err
	}
	if preserveRetained {
		next, err = next.Add(retainedDemand(current.Demand))
		if err != nil {
			return policyquota.Reservation{}, policyquota.Usage{}, true, err
		}
	}
	if err := s.updateQuotaUsage(ctx, tx, installationID, rows, next); err != nil {
		return policyquota.Reservation{}, policyquota.Usage{}, true, err
	}
	update, err := tx.ExecContext(ctx, s.bind(
		`UPDATE quota_reservations
		 SET state=?,release_cause=?,version=version+1,updated_at=?
		 WHERE installation_id=? AND idempotency_key=? AND version=? AND state=?`,
	), released.State, released.ReleaseCause, time.Now().UTC().Format(time.RFC3339Nano),
		installationID, key, version, current.State)
	if err != nil {
		return policyquota.Reservation{}, policyquota.Usage{}, true, err
	}
	changed, err := update.RowsAffected()
	if err != nil {
		return policyquota.Reservation{}, policyquota.Usage{}, true, err
	}
	if changed != 1 {
		return policyquota.Reservation{}, policyquota.Usage{}, true, ports.ErrConflict
	}
	return released, next, true, nil
}

func (s *Store) releaseRetainedJobQuotaTx(
	ctx context.Context,
	tx *sql.Tx,
	installationID domain.InstallationID,
	jobID string,
) error {
	key, target, found, err := s.quotaReservationKeyByJob(ctx, tx, installationID, jobID)
	if err != nil || !found {
		return err
	}
	rows, err := s.lockQuotaUsage(ctx, tx, installationID, target)
	if err != nil {
		return err
	}
	current, version, _, found, err := s.quotaReservation(
		ctx, tx, installationID, key, true,
	)
	if err != nil || !found {
		return err
	}
	held := retainedDemand(current.Demand)
	if current.State != policyquota.ReservationReleased || held == (policyquota.Usage{}) {
		return nil
	}
	next, err := quotaUsageFromRows(rows).Release(held)
	if err != nil {
		return err
	}
	if err := s.updateQuotaUsage(ctx, tx, installationID, rows, next); err != nil {
		return err
	}
	current.Demand.Global.Retained = 0
	current.Demand.Project.Retained = 0
	current.Demand.Namespace.Retained = 0
	encoded, err := json.Marshal(current.Demand)
	if err != nil {
		return err
	}
	update, err := tx.ExecContext(ctx, s.bind(
		`UPDATE quota_reservations SET demand=?,version=version+1,updated_at=?
		 WHERE installation_id=? AND idempotency_key=? AND version=? AND state='RELEASED'`,
	), string(encoded), time.Now().UTC().Format(time.RFC3339Nano),
		installationID, key, version)
	if err != nil {
		return err
	}
	changed, err := update.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ports.ErrConflict
	}
	return nil
}

func (s *Store) releaseAnyJobQuotaTx(
	ctx context.Context,
	tx *sql.Tx,
	jobID string,
	cause policyquota.ReleaseCause,
) error {
	var installationID domain.InstallationID
	err := tx.QueryRowContext(ctx, s.bind(
		`SELECT installation_id FROM quota_reservations WHERE job_id=?`,
	), jobID).Scan(&installationID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	_, _, _, err = s.releaseJobQuotaTx(
		ctx, tx, installationID, jobID, cause, true,
	)
	return err
}

func (s *Store) releaseAnyRetainedJobQuotaTx(
	ctx context.Context,
	tx *sql.Tx,
	jobID string,
) error {
	var installationID domain.InstallationID
	err := tx.QueryRowContext(ctx, s.bind(
		`SELECT installation_id FROM quota_reservations WHERE job_id=?`,
	), jobID).Scan(&installationID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.releaseRetainedJobQuotaTx(ctx, tx, installationID, jobID)
}

func validateQuotaTarget(installationID domain.InstallationID, target policyquota.Scope) error {
	if installationID == "" {
		return errors.New("installation ID is required")
	}
	if target.Kind != policyquota.ScopeNamespace {
		return errors.New("quota target must be a namespace scope")
	}
	return validatePolicyScope(target)
}

func validateEffectiveQuotaPolicy(
	policy policyquota.EffectivePolicy,
	target policyquota.Scope,
) error {
	if len(policy.Applied) == 0 ||
		!policyScopeAppliesTo(policy.Applied[len(policy.Applied)-1].Scope, target) {
		return errors.New("effective policy does not apply to quota target")
	}
	if policy.Applied[len(policy.Applied)-1].Version > math.MaxInt64 {
		return errors.New("policy version exceeds durable range")
	}
	return nil
}

func quotaScopes(target policyquota.Scope) []policyquota.Scope {
	return []policyquota.Scope{
		{Kind: policyquota.ScopeInstallation},
		{Kind: policyquota.ScopeProject, Project: target.Project},
		target,
	}
}

func quotaUsageFromRows(rows []quotaUsageRow) policyquota.Usage {
	return policyquota.Usage{
		Global: rows[0].counters, Project: rows[1].counters, Namespace: rows[2].counters,
	}
}

func validateUsageForDatabase(usage policyquota.Usage) error {
	for _, counters := range []policyquota.Counters{usage.Global, usage.Project, usage.Namespace} {
		if counters.Concurrent > math.MaxInt64 ||
			counters.Queued > math.MaxInt64 ||
			counters.Retained > math.MaxInt64 {
			return errors.New("quota demand exceeds durable counter range")
		}
	}
	return nil
}

func concurrentDemand(current policyquota.Usage) policyquota.Usage {
	return policyquota.Usage{
		Global: policyquota.Counters{
			Concurrent: 1, Retained: current.Global.Retained,
		},
		Project: policyquota.Counters{
			Concurrent: 1, Retained: current.Project.Retained,
		},
		Namespace: policyquota.Counters{
			Concurrent: 1, Retained: current.Namespace.Retained,
		},
	}
}

func queuedDemand(current policyquota.Usage) policyquota.Usage {
	return policyquota.Usage{
		Global: policyquota.Counters{
			Queued: 1, Retained: current.Global.Retained,
		},
		Project: policyquota.Counters{
			Queued: 1, Retained: current.Project.Retained,
		},
		Namespace: policyquota.Counters{
			Queued: 1, Retained: current.Namespace.Retained,
		},
	}
}

func retainedDemand(current policyquota.Usage) policyquota.Usage {
	return policyquota.Usage{
		Global:    policyquota.Counters{Retained: current.Global.Retained},
		Project:   policyquota.Counters{Retained: current.Project.Retained},
		Namespace: policyquota.Counters{Retained: current.Namespace.Retained},
	}
}

func sameJobIntent(stored domain.Job, input domain.CreateJob) bool {
	sameSchedule := stored.ScheduledFor == nil && input.ScheduledFor == nil
	if stored.ScheduledFor != nil && input.ScheduledFor != nil {
		sameSchedule = stored.ScheduledFor.Equal(input.ScheduledFor.UTC())
	}
	return stored.ParentID == input.ParentID &&
		stored.ProjectID == input.ProjectID &&
		stored.NamespaceBindingID == input.NamespaceBindingID &&
		stored.CreatorPrincipalID == input.CreatorPrincipalID &&
		stored.SubmissionSource == input.SubmissionSource &&
		stored.Name == input.Name &&
		stored.Namespace == input.Namespace &&
		stored.Team == input.Team &&
		stored.Priority == input.Priority &&
		stored.Attempt == max(input.Attempt, 1) &&
		sameSchedule &&
		bytes.Equal(stored.Template, input.Template)
}

func idempotencyConflictDecision(
	policy policyquota.EffectivePolicy,
	usage policyquota.Usage,
) policyquota.ReservationDecision {
	ref := policy.Applied[len(policy.Applied)-1]
	rejection := policyquota.Rejection{
		Policy: ref, Scope: ref.Scope, Metric: "idempotency_key",
		Reason:      policyquota.ReasonIdempotencyConflict,
		Remediation: policyquota.RemediationUseNewKey,
	}
	return policyquota.ReservationDecision{Usage: usage, Rejection: &rejection}
}

func policyScopeAppliesTo(policyScope, target policyquota.Scope) bool {
	switch policyScope.Kind {
	case policyquota.ScopeInstallation:
		return true
	case policyquota.ScopeProject:
		return policyScope.Project == target.Project
	case policyquota.ScopeNamespace:
		return policyScope == target
	default:
		return false
	}
}
