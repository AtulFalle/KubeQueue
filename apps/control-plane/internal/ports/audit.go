package ports

import (
	"context"
	"errors"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain/audit"
)

const MaxAuditPageSize = 200

var (
	ErrAuditEventExists   = errors.New("audit event already exists")
	ErrAuditEventNotFound = errors.New("audit event not found")
)

// AuditCursor identifies the last event returned by an ascending stable cursor read.
type AuditCursor struct {
	OccurredAt time.Time
	EventID    audit.EventID
}

type AuditFilter struct {
	ProjectIDs   []audit.ProjectID
	PrincipalID  audit.PrincipalID
	Action       audit.Action
	TargetType   audit.TargetType
	TargetID     audit.TargetID
	Decision     audit.AuthorizationDecision
	Result       audit.Result
	OccurredFrom time.Time
	OccurredTo   time.Time
}

type AuditPageRequest struct {
	InstallationID audit.InstallationID
	Filter         AuditFilter
	Limit          int
	After          *AuditCursor
}

type AuditPage struct {
	Events []audit.Event
	Next   *AuditCursor
}

type AuditRetentionCandidate struct {
	EventID          audit.EventID
	OccurredAt       time.Time
	RetentionExpires time.Time
}

type TransactionalAudit struct {
	Event  audit.Event
	Policy audit.RetentionPolicy
	Hold   audit.LegalHold
}

type transactionalAuditContextKey struct{}

func WithTransactionalAudit(ctx context.Context, record TransactionalAudit) context.Context {
	return context.WithValue(ctx, transactionalAuditContextKey{}, record)
}

func TransactionalAuditFromContext(ctx context.Context) (TransactionalAudit, bool) {
	record, ok := ctx.Value(transactionalAuditContextKey{}).(TransactionalAudit)
	return record, ok
}

// AuditEventStore is the narrow durable boundary for the immutable local audit record.
type AuditEventStore interface {
	AppendAuditEvent(
		context.Context,
		audit.Event,
		audit.RetentionPolicy,
		audit.LegalHold,
	) error
	ReadAuditEvents(context.Context, AuditPageRequest) (AuditPage, error)
	GetAuditEvent(context.Context, audit.InstallationID, audit.EventID) (audit.Event, error)
	SelectAuditRetentionCandidates(
		context.Context,
		audit.InstallationID,
		time.Time,
		int,
	) ([]AuditRetentionCandidate, error)
}

// AuditRetentionDeleter removes only the supplied retention candidates after
// atomically rechecking expiry and legal-hold metadata at evaluatedAt.
type AuditRetentionDeleter interface {
	DeleteAuditRetentionCandidates(
		context.Context,
		audit.InstallationID,
		time.Time,
		[]audit.EventID,
	) (int, error)
}
