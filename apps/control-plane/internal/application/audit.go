package application

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync/atomic"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain/audit"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/ports"
)

var (
	ErrInvalidAuditSearch       = errors.New("invalid audit search")
	ErrAuditWriterAlreadyActive = errors.New("audit writer is already active")
	errAuditScopeNotVisible     = errors.New("audit scope is not visible")
)

type AuditFilter = ports.AuditFilter

type AuditSearchRequest struct {
	InstallationID audit.InstallationID
	Filter         AuditFilter
	Limit          int
	After          *ports.AuditCursor
}

type AuditSearchPage struct {
	Events []audit.Event
	Next   *ports.AuditCursor
}

// AuditEventReader is the bounded persistence surface consumed by audit reads.
type AuditEventReader interface {
	ReadAuditEvents(context.Context, ports.AuditPageRequest) (ports.AuditPage, error)
	GetAuditEvent(context.Context, audit.InstallationID, audit.EventID) (audit.Event, error)
}

type AuditService struct {
	store      AuditEventReader
	authorizer Authorizer
}

func NewAuditService(store AuditEventReader, authorizer Authorizer) *AuditService {
	return &AuditService{store: store, authorizer: authorizer}
}

func (s *AuditService) Search(
	ctx context.Context,
	request AuditSearchRequest,
) (AuditSearchPage, error) {
	filter, err := s.authorizedFilter(ctx, request, domain.PermissionAuditRead)
	if errors.Is(err, errAuditScopeNotVisible) {
		return AuditSearchPage{Events: []audit.Event{}}, nil
	}
	if err != nil {
		return AuditSearchPage{}, err
	}
	return s.searchPage(ctx, request, filter)
}

func (s *AuditService) ExportPage(
	ctx context.Context,
	request AuditSearchRequest,
) (AuditSearchPage, error) {
	filter, err := s.authorizedFilter(ctx, request, domain.PermissionAuditExport)
	if errors.Is(err, errAuditScopeNotVisible) {
		return AuditSearchPage{Events: []audit.Event{}}, nil
	}
	if err != nil {
		return AuditSearchPage{}, err
	}
	return s.searchPage(ctx, request, filter)
}

func (s *AuditService) Get(
	ctx context.Context,
	installationID audit.InstallationID,
	eventID audit.EventID,
) (audit.Event, error) {
	if s == nil || s.store == nil || s.authorizer == nil {
		return audit.Event{}, errors.New("audit service is not configured")
	}
	actor, err := ActorFromContext(ctx)
	if err != nil {
		return audit.Event{}, err
	}
	if installationID.String() == "" ||
		installationID.String() != string(actor.InstallationID) {
		return audit.Event{}, ports.ErrAuditEventNotFound
	}
	access, err := s.authorizer.AccessibleScope(ctx, actor, domain.PermissionAuditRead)
	if err != nil {
		return audit.Event{}, err
	}
	event, err := s.store.GetAuditEvent(ctx, installationID, eventID)
	if err != nil {
		return audit.Event{}, err
	}
	if access.InstallationID != actor.InstallationID {
		return audit.Event{}, ports.ErrAuditEventNotFound
	}
	if access.InstallationWide {
		return event, nil
	}
	for _, projectID := range access.ProjectIDs {
		if string(projectID) == event.Scope().ProjectID().String() {
			return event, nil
		}
	}
	return audit.Event{}, ports.ErrAuditEventNotFound
}

func (s *AuditService) NewExportIterator(
	request AuditSearchRequest,
) *AuditExportIterator {
	return &AuditExportIterator{service: s, request: cloneAuditSearchRequest(request)}
}

func (s *AuditService) authorizedFilter(
	ctx context.Context,
	request AuditSearchRequest,
	permission domain.Permission,
) (AuditFilter, error) {
	if s == nil || s.store == nil || s.authorizer == nil {
		return AuditFilter{}, errors.New("audit service is not configured")
	}
	actor, err := ActorFromContext(ctx)
	if err != nil {
		return AuditFilter{}, err
	}
	if request.InstallationID.String() == "" ||
		request.InstallationID.String() != string(actor.InstallationID) {
		return AuditFilter{}, domain.ErrAccessDenied
	}
	access, err := s.authorizer.AccessibleScope(ctx, actor, permission)
	if err != nil {
		return AuditFilter{}, err
	}
	if access.InstallationID != actor.InstallationID {
		return AuditFilter{}, domain.ErrAccessDenied
	}

	filter, err := normalizeAuditFilter(request.Filter)
	if err != nil {
		return AuditFilter{}, err
	}
	if access.InstallationWide {
		return filter, nil
	}
	if len(access.ProjectIDs) == 0 {
		return AuditFilter{}, domain.ErrAccessDenied
	}
	allowed := make(map[string]struct{}, len(access.ProjectIDs))
	for _, projectID := range access.ProjectIDs {
		allowed[string(projectID)] = struct{}{}
	}
	if len(filter.ProjectIDs) == 0 {
		filter.ProjectIDs = make([]audit.ProjectID, 0, len(access.ProjectIDs))
		for _, projectID := range access.ProjectIDs {
			value, err := audit.NewProjectID(string(projectID))
			if err != nil {
				return AuditFilter{}, fmt.Errorf("translate authorized audit project: %w", err)
			}
			filter.ProjectIDs = append(filter.ProjectIDs, value)
		}
		sortAuditProjectIDs(filter.ProjectIDs)
		return filter, nil
	}
	for _, projectID := range filter.ProjectIDs {
		if _, ok := allowed[projectID.String()]; !ok {
			return AuditFilter{}, errAuditScopeNotVisible
		}
	}
	return filter, nil
}

func (s *AuditService) searchPage(
	ctx context.Context,
	request AuditSearchRequest,
	filter AuditFilter,
) (AuditSearchPage, error) {
	limit, err := boundedApplicationAuditLimit(request.Limit)
	if err != nil {
		return AuditSearchPage{}, err
	}
	raw, err := s.store.ReadAuditEvents(ctx, ports.AuditPageRequest{
		InstallationID: request.InstallationID,
		Filter:         filter,
		Limit:          limit,
		After:          request.After,
	})
	if err != nil {
		return AuditSearchPage{}, fmt.Errorf("read audit search page: %w", err)
	}
	return AuditSearchPage{Events: raw.Events, Next: cloneAuditCursor(raw.Next)}, nil
}

type AuditExportIterator struct {
	service *AuditService
	request AuditSearchRequest
	started bool
	done    bool
}

// Next returns one ascending, stable export page. More is false after the last page.
func (i *AuditExportIterator) Next(
	ctx context.Context,
) (page AuditSearchPage, more bool, err error) {
	if i == nil || i.service == nil || i.done {
		return AuditSearchPage{}, false, nil
	}
	if i.started && i.request.After == nil {
		i.done = true
		return AuditSearchPage{}, false, nil
	}
	page, err = i.service.ExportPage(ctx, i.request)
	if err != nil {
		return AuditSearchPage{}, false, err
	}
	i.started = true
	i.request.After = cloneAuditCursor(page.Next)
	i.done = page.Next == nil
	return page, true, nil
}

type AuditRetentionRepository interface {
	SelectAuditRetentionCandidates(
		context.Context,
		audit.InstallationID,
		time.Time,
		int,
	) ([]ports.AuditRetentionCandidate, error)
	ports.AuditRetentionDeleter
}

type AuditRetentionResult struct {
	Selected int
	Deleted  int
}

// ApplyAuditRetention selects a bounded batch and delegates deletion to a
// repository operation that must recheck expiry and legal hold atomically.
func ApplyAuditRetention(
	ctx context.Context,
	repository AuditRetentionRepository,
	installationID audit.InstallationID,
	evaluatedAt time.Time,
	limit int,
) (AuditRetentionResult, error) {
	if repository == nil || installationID.String() == "" || evaluatedAt.IsZero() {
		return AuditRetentionResult{}, errors.New("audit retention is not configured")
	}
	limit, err := boundedApplicationAuditLimit(limit)
	if err != nil {
		return AuditRetentionResult{}, err
	}
	candidates, err := repository.SelectAuditRetentionCandidates(
		ctx, installationID, evaluatedAt.Round(0).UTC(), limit,
	)
	if err != nil {
		return AuditRetentionResult{}, fmt.Errorf("select audit retention batch: %w", err)
	}
	result := AuditRetentionResult{Selected: len(candidates)}
	if len(candidates) == 0 {
		return result, nil
	}
	ids := make([]audit.EventID, len(candidates))
	for index, candidate := range candidates {
		ids[index] = candidate.EventID
	}
	result.Deleted, err = repository.DeleteAuditRetentionCandidates(
		ctx, installationID, evaluatedAt.Round(0).UTC(), ids,
	)
	if err != nil {
		return result, fmt.Errorf("delete audit retention batch: %w", err)
	}
	if result.Deleted < 0 || result.Deleted > result.Selected {
		return result, errors.New("delete audit retention batch returned an invalid count")
	}
	return result, nil
}

type AuditWrite struct {
	Event  audit.Event
	Policy audit.RetentionPolicy
	Hold   audit.LegalHold
}

type AuditWriterStats struct {
	Accepted   uint64
	Persisted  uint64
	Overloaded uint64
	Failed     uint64
}

// AuditEventAppender is the persistence surface consumed by asynchronous writes.
type AuditEventAppender interface {
	AppendAuditEvent(
		context.Context,
		audit.Event,
		audit.RetentionPolicy,
		audit.LegalHold,
	) error
}

// BoundedAuditWriter accepts authentication-failure and denial events without
// blocking request handling. Run owns persistence and permits one active runner.
type BoundedAuditWriter struct {
	store      AuditEventAppender
	queue      chan AuditWrite
	active     atomic.Bool
	accepted   atomic.Uint64
	persisted  atomic.Uint64
	overloaded atomic.Uint64
	failed     atomic.Uint64
}

func NewBoundedAuditWriter(
	store AuditEventAppender,
	capacity int,
) (*BoundedAuditWriter, error) {
	if store == nil || capacity <= 0 || capacity > 10000 {
		return nil, errors.New("audit writer requires a store and bounded positive capacity")
	}
	return &BoundedAuditWriter{store: store, queue: make(chan AuditWrite, capacity)}, nil
}

func (w *BoundedAuditWriter) TryAppend(write AuditWrite) bool {
	if w == nil {
		return false
	}
	select {
	case w.queue <- write:
		w.accepted.Add(1)
		return true
	default:
		w.overloaded.Add(1)
		return false
	}
}

func (w *BoundedAuditWriter) Run(ctx context.Context) error {
	if w == nil || w.store == nil {
		return errors.New("audit writer is not configured")
	}
	if !w.active.CompareAndSwap(false, true) {
		return ErrAuditWriterAlreadyActive
	}
	defer w.active.Store(false)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case write := <-w.queue:
			if err := w.store.AppendAuditEvent(
				ctx, write.Event, write.Policy, write.Hold,
			); err != nil {
				w.failed.Add(1)
				continue
			}
			w.persisted.Add(1)
		}
	}
}

func (w *BoundedAuditWriter) Stats() AuditWriterStats {
	if w == nil {
		return AuditWriterStats{}
	}
	return AuditWriterStats{
		Accepted: w.accepted.Load(), Persisted: w.persisted.Load(),
		Overloaded: w.overloaded.Load(), Failed: w.failed.Load(),
	}
}

func normalizeAuditFilter(filter AuditFilter) (AuditFilter, error) {
	filter.ProjectIDs = append([]audit.ProjectID(nil), filter.ProjectIDs...)
	sortAuditProjectIDs(filter.ProjectIDs)
	for index := 1; index < len(filter.ProjectIDs); index++ {
		if filter.ProjectIDs[index-1].String() == filter.ProjectIDs[index].String() {
			return AuditFilter{}, fmt.Errorf("%w: duplicate project", ErrInvalidAuditSearch)
		}
	}
	if !filter.OccurredFrom.IsZero() {
		filter.OccurredFrom = filter.OccurredFrom.Round(0).UTC()
	}
	if !filter.OccurredTo.IsZero() {
		filter.OccurredTo = filter.OccurredTo.Round(0).UTC()
	}
	if !filter.OccurredFrom.IsZero() && !filter.OccurredTo.IsZero() &&
		!filter.OccurredFrom.Before(filter.OccurredTo) {
		return AuditFilter{}, fmt.Errorf("%w: occurrence range", ErrInvalidAuditSearch)
	}
	if filter.Decision != "" &&
		filter.Decision != audit.DecisionAllow &&
		filter.Decision != audit.DecisionDeny {
		return AuditFilter{}, fmt.Errorf("%w: decision", ErrInvalidAuditSearch)
	}
	if filter.Result != "" &&
		filter.Result != audit.ResultSuccess &&
		filter.Result != audit.ResultFailure {
		return AuditFilter{}, fmt.Errorf("%w: result", ErrInvalidAuditSearch)
	}
	return filter, nil
}

func sortAuditProjectIDs(projectIDs []audit.ProjectID) {
	sort.Slice(projectIDs, func(first, second int) bool {
		return projectIDs[first].String() < projectIDs[second].String()
	})
}

func boundedApplicationAuditLimit(limit int) (int, error) {
	if limit <= 0 {
		return 0, fmt.Errorf("%w: limit must be positive", ErrInvalidAuditSearch)
	}
	if limit > ports.MaxAuditPageSize {
		return ports.MaxAuditPageSize, nil
	}
	return limit, nil
}

func cloneAuditCursor(cursor *ports.AuditCursor) *ports.AuditCursor {
	if cursor == nil {
		return nil
	}
	cloned := *cursor
	return &cloned
}

func cloneAuditSearchRequest(request AuditSearchRequest) AuditSearchRequest {
	request.Filter.ProjectIDs = append([]audit.ProjectID(nil), request.Filter.ProjectIDs...)
	request.After = cloneAuditCursor(request.After)
	return request
}
