package persistence

import (
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

func (s *Store) CompareAndSetProjectAdmission(
	ctx context.Context,
	installationID domain.InstallationID,
	projectID domain.ProjectID,
	expectedPolicyVersion uint64,
	expectedSchedulingVersion uint64,
	next policyquota.Policy,
	weight uint64,
) (ports.ProjectAdmissionConfiguration, error) {
	if installationID == "" || projectID == "" || next.Ref.Scope != (policyquota.Scope{
		Kind: policyquota.ScopeProject, Project: string(projectID),
	}) {
		return ports.ProjectAdmissionConfiguration{}, errors.New("valid project admission scope is required")
	}
	if next.Ref.Version != expectedPolicyVersion+1 ||
		next.Ref.Version > math.MaxInt64 || expectedSchedulingVersion >= math.MaxInt64 {
		return ports.ProjectAdmissionConfiguration{}, ports.ErrConflict
	}
	if weight == 0 || weight > 1_000_000 {
		return ports.ProjectAdmissionConfiguration{}, errors.New("project weight must be between 1 and 1000000")
	}
	encoded, err := json.Marshal(next.Rules)
	if err != nil {
		return ports.ProjectAdmissionConfiguration{}, fmt.Errorf("encode project policy: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	err = s.auditTransaction(ctx, func(tx *sql.Tx) error {
		if err := s.validatePolicyAgainstParents(ctx, tx, installationID, next); err != nil {
			return err
		}
		if expectedPolicyVersion == 0 {
			result, err := tx.ExecContext(ctx, s.bind(
				`INSERT INTO policy_scopes(
				 id,installation_id,scope_key,scope_type,project_id,namespace,
				 current_version,created_at,updated_at
				 ) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`,
			), next.Ref.ID, installationID, policyScopeKey(next.Ref.Scope), next.Ref.Scope.Kind,
				projectID, nil, next.Ref.Version, now, now)
			if err != nil {
				return fmt.Errorf("create project policy scope: %w", err)
			}
			created, err := result.RowsAffected()
			if err != nil || created != 1 {
				if err != nil {
					return err
				}
				return ports.ErrConflict
			}
		} else {
			result, err := tx.ExecContext(ctx, s.bind(
				`UPDATE policy_scopes SET current_version=?,updated_at=?
				 WHERE id=? AND installation_id=? AND scope_key=? AND current_version=?`,
			), next.Ref.Version, now, next.Ref.ID, installationID,
				policyScopeKey(next.Ref.Scope), expectedPolicyVersion)
			if err != nil {
				return fmt.Errorf("advance project policy: %w", err)
			}
			updated, err := result.RowsAffected()
			if err != nil || updated != 1 {
				if err != nil {
					return err
				}
				return ports.ErrConflict
			}
		}
		if _, err := tx.ExecContext(ctx, s.bind(
			`INSERT INTO policy_versions(policy_id,version,rules,created_at) VALUES(?,?,?,?)`,
		), next.Ref.ID, next.Ref.Version, string(encoded), now); err != nil {
			return fmt.Errorf("write project policy version: %w", err)
		}
		result, err := tx.ExecContext(ctx, s.bind(
			`UPDATE projects SET scheduling_weight=?,scheduling_version=scheduling_version+1
			 WHERE installation_id=? AND id=? AND scheduling_version=?`,
		), weight, installationID, projectID, expectedSchedulingVersion)
		if err != nil {
			return fmt.Errorf("update project scheduling weight: %w", err)
		}
		updated, err := result.RowsAffected()
		if err != nil || updated != 1 {
			if err != nil {
				return err
			}
			return ports.ErrConflict
		}
		return nil
	})
	if err != nil {
		return ports.ProjectAdmissionConfiguration{}, err
	}
	policy := next
	return ports.ProjectAdmissionConfiguration{
		Policy: &policy,
		Scheduling: ports.ProjectScheduling{
			ProjectID: projectID, Weight: weight, Version: expectedSchedulingVersion + 1,
		},
	}, nil
}

func (s *Store) ListAdmissionDecisions(
	ctx context.Context,
	installationID domain.InstallationID,
	projectID domain.ProjectID,
	after *ports.AdmissionDecisionCursor,
	limit int,
) ([]ports.AdmissionDecisionRecord, error) {
	if installationID == "" || projectID == "" || limit < 1 ||
		limit > ports.MaxAdmissionDecisionPageSize {
		return nil, errors.New("valid bounded admission decision request is required")
	}
	query := `SELECT id,project_id,job_id,accepted,reason,policy_version,
		scheduling_weight,decided_at FROM (
		  SELECT id,project_id,job_id,1 AS accepted,'admission.accepted' AS reason,
		         policy_version,project_weight AS scheduling_weight,created_at AS decided_at
		  FROM admission_decisions WHERE installation_id=? AND project_id=?
		  UNION ALL
		  SELECT ('rejection:' || id),project_id,id,0,last_error_code,NULL,NULL,updated_at
		  FROM jobs WHERE project_id=? AND
		    (last_error_code LIKE 'quota.%' OR last_error_code LIKE 'policy.%')
		) decisions`
	args := []any{installationID, projectID, projectID}
	if after != nil {
		if after.DecidedAt.IsZero() || after.ID == "" {
			return nil, errors.New("invalid admission decision cursor")
		}
		cursorTime := after.DecidedAt.UTC().Format(time.RFC3339Nano)
		query += ` WHERE (decided_at<? OR (decided_at=? AND id<?))`
		args = append(args, cursorTime, cursorTime, after.ID)
	}
	query += ` ORDER BY decided_at DESC,id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return nil, fmt.Errorf("list admission decisions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]ports.AdmissionDecisionRecord, 0, limit+1)
	for rows.Next() {
		var record ports.AdmissionDecisionRecord
		var accepted int
		var policyVersion, schedulingWeight sql.NullInt64
		var decidedAt string
		if err := rows.Scan(
			&record.ID, &record.ProjectID, &record.JobID, &accepted, &record.Reason,
			&policyVersion, &schedulingWeight, &decidedAt,
		); err != nil {
			return nil, fmt.Errorf("scan admission decision: %w", err)
		}
		record.Accepted = accepted == 1
		if policyVersion.Valid {
			record.PolicyVersion = uint64(policyVersion.Int64)
		}
		if schedulingWeight.Valid {
			record.SchedulingWeight = uint64(schedulingWeight.Int64)
		}
		record.DecidedAt, err = time.Parse(time.RFC3339Nano, decidedAt)
		if err != nil {
			return nil, fmt.Errorf("parse admission decision time: %w", err)
		}
		result = append(result, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read admission decisions: %w", err)
	}
	return result, nil
}
