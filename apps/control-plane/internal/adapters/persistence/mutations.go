package persistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/leadership"
	"github.com/google/uuid"
)

func (s *Store) BeginMutation(
	ctx context.Context,
	request leadership.MutationRequest,
	authority leadership.Authority,
) (leadership.MutationRecord, error) {
	if err := validateMutationRequest(request); err != nil {
		return leadership.MutationRecord{}, err
	}
	var record leadership.MutationRecord
	var resultErr error
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		if err := s.authorizeMutationTx(ctx, tx, authority); err != nil {
			return err
		}
		current, err := s.readMutationTx(ctx, tx, request)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if errors.Is(err, sql.ErrNoRows) {
			current = leadership.MutationRecord{MutationRequest: request}
		}
		if current.State == leadership.MutationInFlight {
			current.Mutation, err = current.Complete(
				current.Generation, leadership.OutcomeUncertain,
			)
			if err != nil {
				return err
			}
			current.UpdatedAt = time.Now().UTC()
			if err := s.writeMutationTx(ctx, tx, current); err != nil {
				return err
			}
			record = current
			resultErr = leadership.ErrObservationRequired
			return nil
		}
		if current.State == leadership.MutationObservationRequired {
			record = current
			resultErr = leadership.ErrObservationRequired
			return nil
		}
		if current.State == leadership.MutationSucceeded {
			record = current
			resultErr = leadership.ErrMutationNotReady
			return nil
		}
		next, err := current.Begin(authority)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		record = leadership.MutationRecord{
			Mutation: next, MutationRequest: request, AttemptID: uuid.NewString(),
			StartedAt: now, UpdatedAt: now,
		}
		return s.writeMutationTx(ctx, tx, record)
	})
	if err != nil {
		return leadership.MutationRecord{}, fmt.Errorf("begin Kubernetes mutation: %w", err)
	}
	return record, resultErr
}

func (s *Store) CompleteMutation(
	ctx context.Context,
	request leadership.MutationRequest,
	generation uint64,
	outcome leadership.MutationOutcome,
	errorClass string,
) (leadership.MutationRecord, error) {
	var record leadership.MutationRecord
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		current, err := s.readMutationTx(ctx, tx, request)
		if err != nil {
			return err
		}
		current.Mutation, err = current.Complete(generation, outcome)
		if err != nil {
			return err
		}
		current.ErrorClass = boundedMutationValue(errorClass, 64)
		current.UpdatedAt = time.Now().UTC()
		record = current
		return s.writeMutationTx(ctx, tx, current)
	})
	if err != nil {
		return leadership.MutationRecord{}, fmt.Errorf("complete Kubernetes mutation: %w", err)
	}
	return record, nil
}

func (s *Store) ObserveMutation(
	ctx context.Context,
	request leadership.MutationRequest,
	authority leadership.Authority,
	observation leadership.MutationObservation,
) (leadership.MutationRecord, error) {
	var record leadership.MutationRecord
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		if err := s.authorizeMutationTx(ctx, tx, authority); err != nil {
			return err
		}
		current, err := s.readMutationTx(ctx, tx, request)
		if err != nil {
			return err
		}
		current.Mutation, err = current.Observe(authority, observation)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		current.ObservedAt = &now
		current.UpdatedAt = now
		current.ErrorClass = ""
		record = current
		return s.writeMutationTx(ctx, tx, current)
	})
	if err != nil {
		return leadership.MutationRecord{}, fmt.Errorf("observe Kubernetes mutation: %w", err)
	}
	return record, nil
}

func (s *Store) Mutation(
	ctx context.Context, request leadership.MutationRequest,
) (leadership.MutationRecord, error) {
	record, err := s.readMutationRow(s.db.QueryRowContext(
		ctx, s.bind(`SELECT operation,job_id,attempt_identity,request_identity,
			attempt_id,generation,state,error_class,started_at,updated_at,observed_at
			FROM reconciliation_mutations
			WHERE job_id=? AND operation=? AND request_identity=?`),
		request.JobID, request.Operation, request.RequestIdentity,
	))
	if err != nil {
		return leadership.MutationRecord{}, fmt.Errorf("read Kubernetes mutation: %w", err)
	}
	return record, nil
}

func (s *Store) PendingMutations(
	ctx context.Context, jobID string,
) ([]leadership.MutationRecord, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT operation,job_id,attempt_identity,
		request_identity,attempt_id,generation,state,error_class,started_at,updated_at,observed_at
		FROM reconciliation_mutations
		WHERE job_id=? AND state IN ('IN_FLIGHT','OBSERVATION_REQUIRED')
		ORDER BY started_at,operation,request_identity`), jobID)
	if err != nil {
		return nil, fmt.Errorf("list pending Kubernetes mutations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	records := make([]leadership.MutationRecord, 0)
	for rows.Next() {
		record, err := s.readMutationRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scan pending Kubernetes mutation: %w", err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list pending Kubernetes mutations: %w", err)
	}
	return records, nil
}

func (s *Store) authorizeMutationTx(
	ctx context.Context, tx *sql.Tx, authority leadership.Authority,
) error {
	lease, err := scanLeadershipLease(tx.QueryRowContext(
		ctx,
		s.bind(`SELECT holder,generation,expires_at FROM leadership_leases WHERE name=?`),
		"reconciler",
	))
	if err != nil {
		return err
	}
	return leadership.Authorize(authority, lease, time.Now().UTC())
}

func (s *Store) readMutationTx(
	ctx context.Context, tx *sql.Tx, request leadership.MutationRequest,
) (leadership.MutationRecord, error) {
	return s.readMutationRow(tx.QueryRowContext(
		ctx, s.bind(`SELECT operation,job_id,attempt_identity,request_identity,
			attempt_id,generation,state,error_class,started_at,updated_at,observed_at
			FROM reconciliation_mutations
			WHERE job_id=? AND operation=? AND request_identity=?`),
		request.JobID, request.Operation, request.RequestIdentity,
	))
}

func (s *Store) writeMutationTx(
	ctx context.Context, tx *sql.Tx, record leadership.MutationRecord,
) error {
	_, err := tx.ExecContext(ctx, s.bind(`
		INSERT INTO reconciliation_mutations(
			job_id,operation,request_identity,attempt_identity,attempt_id,
			generation,state,error_class,started_at,updated_at,observed_at
		) VALUES(?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(job_id,operation,request_identity) DO UPDATE SET
			attempt_identity=excluded.attempt_identity,
			attempt_id=excluded.attempt_id,
			generation=excluded.generation,
			state=excluded.state,
			error_class=excluded.error_class,
			started_at=excluded.started_at,
			updated_at=excluded.updated_at,
			observed_at=excluded.observed_at
	`), record.JobID, record.Operation, record.RequestIdentity, record.AttemptIdentity,
		record.AttemptID, int64(record.Generation), mutationStateText(record.State),
		boundedMutationValue(record.ErrorClass, 64),
		record.StartedAt.UTC().Format(time.RFC3339Nano),
		record.UpdatedAt.UTC().Format(time.RFC3339Nano),
		formatTime(record.ObservedAt))
	return err
}

type mutationRowScanner interface {
	Scan(...any) error
}

func (s *Store) readMutationRow(row mutationRowScanner) (leadership.MutationRecord, error) {
	var record leadership.MutationRecord
	var generation int64
	var state, startedAt, updatedAt string
	var observedAt sql.NullString
	err := row.Scan(
		&record.Operation, &record.JobID, &record.AttemptIdentity, &record.RequestIdentity,
		&record.AttemptID, &generation, &state, &record.ErrorClass, &startedAt, &updatedAt,
		&observedAt,
	)
	if err != nil {
		return leadership.MutationRecord{}, err
	}
	record.Generation = uint64(generation)
	record.State, err = parseMutationState(state)
	if err != nil {
		return leadership.MutationRecord{}, err
	}
	record.StartedAt, err = time.Parse(time.RFC3339Nano, startedAt)
	if err != nil {
		return leadership.MutationRecord{}, err
	}
	record.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return leadership.MutationRecord{}, err
	}
	record.ObservedAt = parseTime(observedAt)
	return record, nil
}

func validateMutationRequest(request leadership.MutationRequest) error {
	switch {
	case strings.TrimSpace(request.Operation) == "":
		return errors.New("mutation operation is required")
	case strings.TrimSpace(request.JobID) == "":
		return errors.New("mutation job identity is required")
	case strings.TrimSpace(request.AttemptIdentity) == "":
		return errors.New("mutation attempt identity is required")
	case strings.TrimSpace(request.RequestIdentity) == "":
		return errors.New("mutation request identity is required")
	case len(request.Operation) > 64 || len(request.AttemptIdentity) > 128 ||
		len(request.RequestIdentity) > 256:
		return errors.New("mutation identity exceeds bounded length")
	default:
		return nil
	}
}

func mutationStateText(state leadership.MutationState) string {
	switch state {
	case leadership.MutationReady:
		return "READY"
	case leadership.MutationInFlight:
		return "IN_FLIGHT"
	case leadership.MutationObservationRequired:
		return "OBSERVATION_REQUIRED"
	case leadership.MutationSucceeded:
		return "SUCCEEDED"
	default:
		return ""
	}
}

func parseMutationState(value string) (leadership.MutationState, error) {
	switch value {
	case "READY":
		return leadership.MutationReady, nil
	case "IN_FLIGHT":
		return leadership.MutationInFlight, nil
	case "OBSERVATION_REQUIRED":
		return leadership.MutationObservationRequired, nil
	case "SUCCEEDED":
		return leadership.MutationSucceeded, nil
	default:
		return 0, fmt.Errorf("unknown mutation state %q", value)
	}
}

func boundedMutationValue(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) > limit {
		return value[:limit]
	}
	return value
}
