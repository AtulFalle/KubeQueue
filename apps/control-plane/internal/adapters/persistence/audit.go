package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain/audit"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/ports"
)

const auditTimeFormat = "2006-01-02T15:04:05.000000000Z"

var _ ports.AuditEventStore = (*Store)(nil)
var _ ports.AuditRetentionDeleter = (*Store)(nil)

func (s *Store) AppendAuditEvent(
	ctx context.Context,
	event audit.Event,
	policy audit.RetentionPolicy,
	hold audit.LegalHold,
) error {
	return s.appendAuditEvent(ctx, s.db, event, policy, hold)
}

type auditExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (s *Store) appendAuditEvent(
	ctx context.Context,
	execer auditExecer,
	event audit.Event,
	policy audit.RetentionPolicy,
	hold audit.LegalHold,
) error {
	retentionExpires := event.OccurredAt().Add(policy.Period())
	if policy.Period() == 0 || retentionExpires.Year() > 9999 {
		return errors.New("append audit event: valid retention policy is required")
	}

	groups := event.Actor().EffectiveGroups()
	encodedGroups, err := json.Marshal(stringValues(groups))
	if err != nil {
		return fmt.Errorf("encode audit actor groups: %w", err)
	}
	before := encodeAuditSummary(event.Before())
	after := encodeAuditSummary(event.After())
	holdUntil, hasHoldUntil := hold.Until()

	result, err := execer.ExecContext(ctx, s.bind(
		`INSERT INTO audit_events(
		 id,occurred_at,request_id,trace_id,actor_principal_id,authentication_method,
		 actor_credential_id,effective_groups,action,target_type,target_id,installation_id,
		 project_id,team_id,namespace,authorization_decision,result,reason,source_address,
		 source_provenance,source_user_agent,before_present,before_state,
		 before_changed_fields,before_redaction_count,before_truncated,after_present,
		 after_state,after_changed_fields,after_redaction_count,after_truncated,
		 retention_expires_at,legal_hold_indefinite,legal_hold_until,persisted_at
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO NOTHING`),
		event.ID().String(), formatAuditTime(event.OccurredAt()),
		event.RequestID().String(), event.TraceID().String(),
		event.Actor().PrincipalID().String(), event.Actor().AuthenticationMethod(),
		event.Actor().CredentialID().String(), string(encodedGroups), event.Action().String(),
		event.Target().Type().String(), event.Target().ID().String(),
		event.Scope().InstallationID().String(), nullableAuditText(event.Scope().ProjectID().String()),
		nullableAuditText(event.Scope().TeamID().String()),
		nullableAuditText(event.Scope().Namespace().String()), event.Decision(), event.Result(),
		event.Reason().String(), event.Source().Address().String(), event.Source().Provenance(),
		event.Source().UserAgent(), before.present, before.state, before.fields,
		before.redactionCount, before.truncated, after.present, after.state, after.fields,
		after.redactionCount, after.truncated, formatAuditTime(retentionExpires),
		hold.Indefinite(), nullableAuditTime(holdUntil, hasHoldUntil),
		formatAuditTime(time.Now().UTC()),
	)
	if err != nil {
		return fmt.Errorf("append audit event: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read appended audit event count: %w", err)
	}
	if inserted == 0 {
		return ports.ErrAuditEventExists
	}
	return nil
}

func (s *Store) auditTransaction(
	ctx context.Context,
	mutate func(*sql.Tx) error,
) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if err := mutate(tx); err != nil {
			return err
		}
		return s.appendTransactionalAudit(ctx, tx)
	})
}

func (s *Store) appendTransactionalAudit(ctx context.Context, tx *sql.Tx) error {
	record, audited := ports.TransactionalAuditFromContext(ctx)
	if !audited {
		return nil
	}
	return s.appendAuditEvent(ctx, tx, record.Event, record.Policy, record.Hold)
}

func (s *Store) ReadAuditEvents(
	ctx context.Context,
	request ports.AuditPageRequest,
) (ports.AuditPage, error) {
	if request.InstallationID.String() == "" {
		return ports.AuditPage{}, errors.New("read audit events: installation ID is required")
	}
	limit, err := boundedAuditLimit(request.Limit)
	if err != nil {
		return ports.AuditPage{}, fmt.Errorf("read audit events: %w", err)
	}

	query := `SELECT ` + auditEventColumns + `
		FROM audit_events WHERE installation_id=?`
	args := []any{request.InstallationID.String()}
	query, args, err = appendAuditFilters(query, args, request.Filter)
	if err != nil {
		return ports.AuditPage{}, fmt.Errorf("read audit events: %w", err)
	}
	if request.After != nil {
		if request.After.OccurredAt.IsZero() || request.After.EventID.String() == "" {
			return ports.AuditPage{}, errors.New("read audit events: invalid cursor")
		}
		cursorTime := formatAuditTime(request.After.OccurredAt)
		query += ` AND (occurred_at>? OR (occurred_at=? AND id>?))`
		args = append(args, cursorTime, cursorTime, request.After.EventID.String())
	}
	query += ` ORDER BY occurred_at ASC,id ASC LIMIT ?`
	args = append(args, limit+1)

	rows, err := s.db.QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return ports.AuditPage{}, fmt.Errorf("read audit events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	page := ports.AuditPage{Events: make([]audit.Event, 0, limit)}
	for rows.Next() {
		event, err := scanAuditEvent(rows)
		if err != nil {
			return ports.AuditPage{}, err
		}
		page.Events = append(page.Events, event)
	}
	if err := rows.Err(); err != nil {
		return ports.AuditPage{}, fmt.Errorf("iterate audit events: %w", err)
	}
	if len(page.Events) > limit {
		page.Events = page.Events[:limit]
		last := page.Events[len(page.Events)-1]
		page.Next = &ports.AuditCursor{OccurredAt: last.OccurredAt(), EventID: last.ID()}
	}
	return page, nil
}

func (s *Store) GetAuditEvent(
	ctx context.Context,
	installationID audit.InstallationID,
	eventID audit.EventID,
) (audit.Event, error) {
	if installationID.String() == "" || eventID.String() == "" {
		return audit.Event{}, ports.ErrAuditEventNotFound
	}
	event, err := scanAuditEvent(s.db.QueryRowContext(ctx, s.bind(
		`SELECT `+auditEventColumns+`
		 FROM audit_events WHERE installation_id=? AND id=?`,
	), installationID.String(), eventID.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return audit.Event{}, ports.ErrAuditEventNotFound
	}
	if err != nil {
		return audit.Event{}, fmt.Errorf("get audit event: %w", err)
	}
	return event, nil
}

func (s *Store) SelectAuditRetentionCandidates(
	ctx context.Context,
	installationID audit.InstallationID,
	evaluatedAt time.Time,
	requestedLimit int,
) ([]ports.AuditRetentionCandidate, error) {
	if installationID.String() == "" || evaluatedAt.IsZero() {
		return nil, errors.New("select audit retention candidates: installation ID and evaluation time are required")
	}
	limit, err := boundedAuditLimit(requestedLimit)
	if err != nil {
		return nil, fmt.Errorf("select audit retention candidates: %w", err)
	}
	evaluated := formatAuditTime(evaluatedAt)
	rows, err := s.db.QueryContext(ctx, s.bind(
		`SELECT id,occurred_at,retention_expires_at
		 FROM audit_events
		 WHERE installation_id=? AND retention_expires_at<=?
		   AND legal_hold_indefinite=FALSE
		   AND (legal_hold_until IS NULL OR legal_hold_until<=?)
		 ORDER BY retention_expires_at ASC,occurred_at ASC,id ASC
		 LIMIT ?`,
	), installationID.String(), evaluated, evaluated, limit)
	if err != nil {
		return nil, fmt.Errorf("select audit retention candidates: %w", err)
	}
	defer func() { _ = rows.Close() }()

	candidates := make([]ports.AuditRetentionCandidate, 0, limit)
	for rows.Next() {
		var id, occurredAt, retentionExpires string
		if err := rows.Scan(&id, &occurredAt, &retentionExpires); err != nil {
			return nil, fmt.Errorf("scan audit retention candidate: %w", err)
		}
		eventID, err := audit.NewEventID(id)
		if err != nil {
			return nil, fmt.Errorf("decode audit retention event ID: %w", err)
		}
		occurred, err := time.Parse(auditTimeFormat, occurredAt)
		if err != nil {
			return nil, fmt.Errorf("decode audit retention occurrence: %w", err)
		}
		expires, err := time.Parse(auditTimeFormat, retentionExpires)
		if err != nil {
			return nil, fmt.Errorf("decode audit retention expiry: %w", err)
		}
		candidates = append(candidates, ports.AuditRetentionCandidate{
			EventID: eventID, OccurredAt: occurred, RetentionExpires: expires,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate audit retention candidates: %w", err)
	}
	return candidates, nil
}

func (s *Store) DeleteAuditRetentionCandidates(
	ctx context.Context,
	installationID audit.InstallationID,
	evaluatedAt time.Time,
	eventIDs []audit.EventID,
) (int, error) {
	if installationID.String() == "" || evaluatedAt.IsZero() {
		return 0, errors.New("delete audit retention candidates: installation ID and evaluation time are required")
	}
	if len(eventIDs) == 0 {
		return 0, nil
	}
	if len(eventIDs) > ports.MaxAuditPageSize {
		return 0, errors.New("delete audit retention candidates: candidate count exceeds limit")
	}
	seen := make(map[string]struct{}, len(eventIDs))
	placeholders := make([]string, len(eventIDs))
	args := make([]any, 0, len(eventIDs)+3)
	evaluated := formatAuditTime(evaluatedAt)
	args = append(args, installationID.String(), evaluated, evaluated)
	for index, eventID := range eventIDs {
		if eventID.String() == "" {
			return 0, errors.New("delete audit retention candidates: event ID is required")
		}
		if _, exists := seen[eventID.String()]; exists {
			return 0, errors.New("delete audit retention candidates: duplicate event ID")
		}
		seen[eventID.String()] = struct{}{}
		placeholders[index] = "?"
		args = append(args, eventID.String())
	}

	deleted := 0
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, s.bind(
			`DELETE FROM audit_events
			 WHERE installation_id=?
			   AND retention_expires_at<=?
			   AND legal_hold_indefinite=FALSE
			   AND (legal_hold_until IS NULL OR legal_hold_until<=?)
			   AND id IN (`+strings.Join(placeholders, ",")+`)`,
		), args...)
		if err != nil {
			return fmt.Errorf("delete eligible audit events: %w", err)
		}
		deleted64, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("read deleted audit event count: %w", err)
		}
		deleted = int(deleted64)
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("delete audit retention candidates: %w", err)
	}
	return deleted, nil
}

func appendAuditFilters(
	query string,
	args []any,
	filter ports.AuditFilter,
) (string, []any, error) {
	if len(filter.ProjectIDs) > ports.MaxAuditPageSize {
		return "", nil, errors.New("project filter exceeds limit")
	}
	if len(filter.ProjectIDs) > 0 {
		placeholders := make([]string, len(filter.ProjectIDs))
		seen := make(map[string]struct{}, len(filter.ProjectIDs))
		for index, projectID := range filter.ProjectIDs {
			if projectID.String() == "" {
				return "", nil, errors.New("project filter contains an empty ID")
			}
			if _, exists := seen[projectID.String()]; exists {
				return "", nil, errors.New("project filter contains a duplicate ID")
			}
			seen[projectID.String()] = struct{}{}
			placeholders[index] = "?"
			args = append(args, projectID.String())
		}
		query += ` AND project_id IN (` + strings.Join(placeholders, ",") + `)`
	}
	if filter.PrincipalID.String() != "" {
		query += ` AND actor_principal_id=?`
		args = append(args, filter.PrincipalID.String())
	}
	if filter.Action.String() != "" {
		query += ` AND action=?`
		args = append(args, filter.Action.String())
	}
	if filter.TargetType.String() != "" {
		query += ` AND target_type=?`
		args = append(args, filter.TargetType.String())
	}
	if filter.TargetID.String() != "" {
		query += ` AND target_id=?`
		args = append(args, filter.TargetID.String())
	}
	if filter.Decision != "" {
		if filter.Decision != audit.DecisionAllow && filter.Decision != audit.DecisionDeny {
			return "", nil, errors.New("invalid authorization decision filter")
		}
		query += ` AND authorization_decision=?`
		args = append(args, filter.Decision)
	}
	if filter.Result != "" {
		if filter.Result != audit.ResultSuccess && filter.Result != audit.ResultFailure {
			return "", nil, errors.New("invalid result filter")
		}
		query += ` AND result=?`
		args = append(args, filter.Result)
	}
	if !filter.OccurredFrom.IsZero() {
		query += ` AND occurred_at>=?`
		args = append(args, formatAuditTime(filter.OccurredFrom))
	}
	if !filter.OccurredTo.IsZero() {
		query += ` AND occurred_at<?`
		args = append(args, formatAuditTime(filter.OccurredTo))
	}
	if !filter.OccurredFrom.IsZero() && !filter.OccurredTo.IsZero() &&
		!filter.OccurredFrom.Before(filter.OccurredTo) {
		return "", nil, errors.New("invalid occurrence filter range")
	}
	return query, args, nil
}

type auditSummaryRecord struct {
	present        bool
	state          any
	fields         any
	redactionCount any
	truncated      any
}

func encodeAuditSummary(summary audit.Summary, present bool) auditSummaryRecord {
	if !present {
		return auditSummaryRecord{}
	}
	fields, _ := json.Marshal(stringValues(summary.ChangedFields()))
	return auditSummaryRecord{
		present: true, state: nullableAuditText(summary.State().String()),
		fields: string(fields), redactionCount: int64(summary.RedactionCount()),
		truncated: summary.Truncated(),
	}
}

func stringValues[T interface{ String() string }](values []T) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = value.String()
	}
	return result
}

func boundedAuditLimit(requested int) (int, error) {
	if requested <= 0 {
		return 0, errors.New("limit must be positive")
	}
	if requested > ports.MaxAuditPageSize {
		return ports.MaxAuditPageSize, nil
	}
	return requested, nil
}

func formatAuditTime(value time.Time) string {
	return value.Round(0).UTC().Format(auditTimeFormat)
}

func nullableAuditTime(value time.Time, valid bool) any {
	if !valid {
		return nil
	}
	return formatAuditTime(value)
}

func nullableAuditText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

const auditEventColumns = `id,occurred_at,request_id,trace_id,actor_principal_id,
	authentication_method,actor_credential_id,effective_groups,action,target_type,target_id,
	installation_id,project_id,team_id,namespace,authorization_decision,result,reason,
	source_address,source_provenance,source_user_agent,before_present,before_state,
	before_changed_fields,before_redaction_count,before_truncated,after_present,after_state,
	after_changed_fields,after_redaction_count,after_truncated`

type auditRowScanner interface {
	Scan(...any) error
}

func scanAuditEvent(row auditRowScanner) (audit.Event, error) {
	var record auditEventRecord
	if err := row.Scan(
		&record.id, &record.occurredAt, &record.requestID, &record.traceID,
		&record.principalID, &record.authenticationMethod, &record.credentialID,
		&record.groups, &record.action, &record.targetType, &record.targetID,
		&record.installationID, &record.projectID, &record.teamID, &record.namespace,
		&record.decision, &record.result, &record.reason, &record.sourceAddress,
		&record.sourceProvenance, &record.userAgent, &record.before.present,
		&record.before.state, &record.before.fields, &record.before.redactionCount,
		&record.before.truncated, &record.after.present, &record.after.state,
		&record.after.fields, &record.after.redactionCount, &record.after.truncated,
	); err != nil {
		return audit.Event{}, fmt.Errorf("scan audit event: %w", err)
	}
	event, err := record.event()
	if err != nil {
		return audit.Event{}, fmt.Errorf("decode audit event: %w", err)
	}
	return event, nil
}

type auditEventRecord struct {
	id, occurredAt, requestID, traceID              string
	principalID, authenticationMethod, credentialID string
	groups, action, targetType, targetID            string
	installationID, decision, result, reason        string
	sourceAddress, sourceProvenance, userAgent      string
	projectID, teamID, namespace                    sql.NullString
	before, after                                   auditSummaryScan
}

type auditSummaryScan struct {
	present        bool
	state          sql.NullString
	fields         sql.NullString
	redactionCount sql.NullInt64
	truncated      sql.NullBool
}

func (record auditEventRecord) event() (audit.Event, error) {
	occurredAt, err := time.Parse(auditTimeFormat, record.occurredAt)
	if err != nil {
		return audit.Event{}, err
	}
	eventID, err := audit.NewEventID(record.id)
	if err != nil {
		return audit.Event{}, err
	}
	requestID, err := audit.NewRequestID(record.requestID)
	if err != nil {
		return audit.Event{}, err
	}
	traceID, err := audit.NewTraceID(record.traceID)
	if err != nil {
		return audit.Event{}, err
	}
	principalID, err := audit.NewPrincipalID(record.principalID)
	if err != nil {
		return audit.Event{}, err
	}
	credentialID, err := audit.NewCredentialID(record.credentialID)
	if err != nil {
		return audit.Event{}, err
	}
	var groupValues []string
	if err := json.Unmarshal([]byte(record.groups), &groupValues); err != nil {
		return audit.Event{}, err
	}
	groups := make([]audit.Group, len(groupValues))
	for index, value := range groupValues {
		groups[index], err = audit.NewGroup(value)
		if err != nil {
			return audit.Event{}, err
		}
	}
	actor, err := audit.NewActor(
		principalID,
		audit.AuthenticationMethod(record.authenticationMethod),
		credentialID,
		groups,
	)
	if err != nil {
		return audit.Event{}, err
	}
	targetType, err := audit.NewTargetType(record.targetType)
	if err != nil {
		return audit.Event{}, err
	}
	targetID, err := audit.NewTargetID(record.targetID)
	if err != nil {
		return audit.Event{}, err
	}
	target, err := audit.NewTarget(targetType, targetID)
	if err != nil {
		return audit.Event{}, err
	}
	installationID, err := audit.NewInstallationID(record.installationID)
	if err != nil {
		return audit.Event{}, err
	}
	projectID, err := audit.NewProjectID(record.projectID.String)
	if err != nil {
		return audit.Event{}, err
	}
	teamID, err := audit.NewTeamID(record.teamID.String)
	if err != nil {
		return audit.Event{}, err
	}
	namespace, err := audit.NewNamespace(record.namespace.String)
	if err != nil {
		return audit.Event{}, err
	}
	scope, err := audit.NewScope(installationID, projectID, teamID, namespace)
	if err != nil {
		return audit.Event{}, err
	}
	address, err := netip.ParseAddr(record.sourceAddress)
	if err != nil {
		return audit.Event{}, err
	}
	source, err := audit.NewTrustworthySource(
		address, audit.SourceProvenance(record.sourceProvenance), record.userAgent,
	)
	if err != nil {
		return audit.Event{}, err
	}
	before, err := record.before.summary()
	if err != nil {
		return audit.Event{}, err
	}
	after, err := record.after.summary()
	if err != nil {
		return audit.Event{}, err
	}
	action, err := audit.NewAction(record.action)
	if err != nil {
		return audit.Event{}, err
	}
	reason, err := audit.NewReasonCode(record.reason)
	if err != nil {
		return audit.Event{}, err
	}
	return audit.NewEvent(audit.EventInput{
		ID: eventID, OccurredAt: occurredAt,
		RequestID: requestID, TraceID: traceID,
		Actor: actor, Action: action, Target: target,
		Scope: scope, Decision: audit.AuthorizationDecision(record.decision),
		Result: audit.Result(record.result), Reason: reason,
		Source: source, Before: before, After: after,
	})
}

func (record auditSummaryScan) summary() (*audit.Summary, error) {
	if !record.present {
		return nil, nil
	}
	var values []string
	if err := json.Unmarshal([]byte(record.fields.String), &values); err != nil {
		return nil, err
	}
	fields := make([]audit.SummaryField, len(values))
	for index, value := range values {
		field, err := audit.NewSummaryField(value)
		if err != nil {
			return nil, err
		}
		fields[index] = field
	}
	state, err := audit.NewSummaryState(record.state.String)
	if err != nil {
		return nil, err
	}
	summary, err := audit.NewSummary(
		state, fields, uint16(record.redactionCount.Int64), record.truncated.Bool,
	)
	if err != nil {
		return nil, err
	}
	return &summary, nil
}
