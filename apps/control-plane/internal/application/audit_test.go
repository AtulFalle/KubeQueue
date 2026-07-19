package application

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain/audit"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/ports"
)

func TestAuditServiceAuthorizesAndAppliesStableFilters(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, time.July, 19, 10, 0, 0, 0, time.UTC)
	store := &applicationAuditStore{events: []audit.Event{
		newApplicationAuditEvent(t, "event-001", "project-one", "jobs.read", base),
		newApplicationAuditEvent(t, "event-002", "project-two", "jobs.read", base.Add(time.Second)),
		newApplicationAuditEvent(t, "event-003", "project-one", "jobs.pause", base.Add(2*time.Second)),
	}}
	authorizer := &applicationAuditAuthorizer{access: domain.AccessScope{
		InstallationID: "default", ProjectIDs: []domain.ProjectID{"project-one"},
	}}
	service := NewAuditService(store, authorizer)
	ctx := WithActor(t.Context(), domain.Actor{
		PrincipalID: "principal-one", InstallationID: "default",
	})

	page, err := service.Search(ctx, AuditSearchRequest{
		InstallationID: mustApplicationAuditConstruct(t, audit.NewInstallationID, "default"),
		Filter: AuditFilter{
			Action: mustApplicationAuditConstruct(t, audit.NewAction, "jobs.read"),
		},
		Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 || page.Events[0].ID().String() != "event-001" {
		t.Fatalf("filtered events = %#v", page.Events)
	}
	if authorizer.permission != domain.PermissionAuditRead {
		t.Fatalf("permission = %q, want %q", authorizer.permission, domain.PermissionAuditRead)
	}
	if len(store.lastRequest.Filter.ProjectIDs) != 1 ||
		store.lastRequest.Filter.ProjectIDs[0].String() != "project-one" ||
		store.lastRequest.Filter.Action.String() != "jobs.read" {
		t.Fatalf("persistence filter = %#v", store.lastRequest.Filter)
	}
}

func TestAuditServiceReturnsEmptyForUnauthorizedProjectFilter(t *testing.T) {
	t.Parallel()
	store := &applicationAuditStore{}
	authorizer := &applicationAuditAuthorizer{access: domain.AccessScope{
		InstallationID: "default", ProjectIDs: []domain.ProjectID{"project-one"},
	}}
	service := NewAuditService(store, authorizer)
	ctx := WithActor(t.Context(), domain.Actor{
		PrincipalID: "principal-one", InstallationID: "default",
	})

	page, err := service.Search(ctx, AuditSearchRequest{
		InstallationID: mustApplicationAuditConstruct(t, audit.NewInstallationID, "default"),
		Filter: AuditFilter{ProjectIDs: []audit.ProjectID{
			mustApplicationAuditConstruct(t, audit.NewProjectID, "project-two"),
		}},
		Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 0 || page.Next != nil {
		t.Fatalf("page = %#v, want non-enumerating empty page", page)
	}
	if store.reads != 0 {
		t.Fatalf("persistence reads = %d, want zero", store.reads)
	}
}

func TestAuditServiceDetailDoesNotEnumerateProjectScope(t *testing.T) {
	t.Parallel()
	event := newApplicationAuditEvent(
		t,
		"event-001",
		"project-two",
		"jobs.read",
		time.Date(2026, time.July, 19, 10, 30, 0, 0, time.UTC),
	)
	store := &applicationAuditStore{events: []audit.Event{event}}
	authorizer := &applicationAuditAuthorizer{access: domain.AccessScope{
		InstallationID: "default", ProjectIDs: []domain.ProjectID{"project-one"},
	}}
	service := NewAuditService(store, authorizer)
	ctx := WithActor(t.Context(), domain.Actor{
		PrincipalID: "principal-one", InstallationID: "default",
	})

	_, err := service.Get(
		ctx,
		mustApplicationAuditConstruct(t, audit.NewInstallationID, "default"),
		event.ID(),
	)
	if !errors.Is(err, ports.ErrAuditEventNotFound) {
		t.Fatalf("detail error = %v, want non-enumerating not found", err)
	}
	if authorizer.permission != domain.PermissionAuditRead {
		t.Fatalf("permission = %q, want %q", authorizer.permission, domain.PermissionAuditRead)
	}
}

func TestAuditExportIteratorUsesOrderedBoundedPages(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, time.July, 19, 11, 0, 0, 0, time.UTC)
	store := &applicationAuditStore{events: []audit.Event{
		newApplicationAuditEvent(t, "event-001", "project-one", "jobs.read", base),
		newApplicationAuditEvent(t, "event-002", "project-one", "jobs.read", base.Add(time.Second)),
		newApplicationAuditEvent(t, "event-003", "project-one", "jobs.read", base.Add(2*time.Second)),
	}}
	authorizer := &applicationAuditAuthorizer{access: domain.AccessScope{
		InstallationID: "default", InstallationWide: true,
	}}
	service := NewAuditService(store, authorizer)
	iterator := service.NewExportIterator(AuditSearchRequest{
		InstallationID: mustApplicationAuditConstruct(t, audit.NewInstallationID, "default"),
		Limit:          1,
	})
	ctx := WithActor(t.Context(), domain.Actor{
		PrincipalID: "principal-one", InstallationID: "default",
	})

	for index, want := range []string{"event-001", "event-002", "event-003"} {
		page, more, err := iterator.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !more || len(page.Events) != 1 || page.Events[0].ID().String() != want {
			t.Fatalf("page %d = %#v, more %v", index, page, more)
		}
	}
	if _, more, err := iterator.Next(ctx); err != nil || more {
		t.Fatalf("terminal Next() more/error = %v/%v", more, err)
	}
	if authorizer.permission != domain.PermissionAuditExport {
		t.Fatalf("permission = %q, want %q", authorizer.permission, domain.PermissionAuditExport)
	}
	for _, request := range store.requests {
		if request.Limit != 1 {
			t.Fatalf("persistence limit = %d, want 1", request.Limit)
		}
	}
}

func TestApplyAuditRetentionDelegatesAtomicLegalHoldRecheck(t *testing.T) {
	t.Parallel()
	evaluatedAt := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	repository := &applicationAuditRetentionRepository{
		candidates: []ports.AuditRetentionCandidate{
			{EventID: mustApplicationAuditConstruct(t, audit.NewEventID, "event-001")},
			{EventID: mustApplicationAuditConstruct(t, audit.NewEventID, "event-002")},
		},
		deleteCount: 1,
	}
	installationID := mustApplicationAuditConstruct(t, audit.NewInstallationID, "default")

	result, err := ApplyAuditRetention(
		t.Context(), repository, installationID, evaluatedAt, ports.MaxAuditPageSize+50,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Selected != 2 || result.Deleted != 1 {
		t.Fatalf("retention result = %#v", result)
	}
	if repository.selectionLimit != ports.MaxAuditPageSize ||
		!repository.deleteEvaluatedAt.Equal(evaluatedAt) ||
		len(repository.deletedIDs) != 2 {
		t.Fatalf("retention delegation = %#v", repository)
	}
}

func TestBoundedAuditWriterDoesNotBlockAndAccountsForOverload(t *testing.T) {
	t.Parallel()
	appendStarted := make(chan struct{}, 2)
	releaseFirst := make(chan struct{})
	persisted := make(chan struct{}, 2)
	store := &applicationAuditStore{
		appendStarted: appendStarted,
		releaseFirst:  releaseFirst,
		persisted:     persisted,
	}
	writer, err := NewBoundedAuditWriter(store, 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	runResult := make(chan error, 1)
	go func() { runResult <- writer.Run(ctx) }()

	if !writer.TryAppend(AuditWrite{}) {
		t.Fatal("first write was rejected")
	}
	<-appendStarted
	if !writer.TryAppend(AuditWrite{}) {
		t.Fatal("second write was rejected")
	}
	if writer.TryAppend(AuditWrite{}) {
		t.Fatal("over-capacity write was accepted")
	}
	close(releaseFirst)
	<-persisted
	<-persisted
	cancel()
	if err := <-runResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context canceled", err)
	}

	stats := writer.Stats()
	if stats.Accepted != 2 || stats.Persisted != 2 ||
		stats.Overloaded != 1 || stats.Failed != 0 {
		t.Fatalf("writer stats = %#v", stats)
	}
}

type applicationAuditAuthorizer struct {
	access     domain.AccessScope
	err        error
	permission domain.Permission
}

func (a *applicationAuditAuthorizer) Authorize(
	context.Context,
	domain.Actor,
	domain.Permission,
	domain.AuthorizationScope,
) error {
	return errors.New("unexpected Authorize call")
}

func (a *applicationAuditAuthorizer) AccessibleScope(
	_ context.Context,
	_ domain.Actor,
	permission domain.Permission,
) (domain.AccessScope, error) {
	a.permission = permission
	return a.access, a.err
}

type applicationAuditStore struct {
	mu            sync.Mutex
	events        []audit.Event
	reads         int
	lastRequest   ports.AuditPageRequest
	requests      []ports.AuditPageRequest
	appendStarted chan struct{}
	releaseFirst  chan struct{}
	persisted     chan struct{}
	appendCalls   int
}

func (s *applicationAuditStore) AppendAuditEvent(
	_ context.Context,
	_ audit.Event,
	_ audit.RetentionPolicy,
	_ audit.LegalHold,
) error {
	s.mu.Lock()
	s.appendCalls++
	call := s.appendCalls
	s.mu.Unlock()
	if s.appendStarted != nil {
		s.appendStarted <- struct{}{}
	}
	if call == 1 && s.releaseFirst != nil {
		<-s.releaseFirst
	}
	if s.persisted != nil {
		s.persisted <- struct{}{}
	}
	return nil
}

func (s *applicationAuditStore) ReadAuditEvents(
	_ context.Context,
	request ports.AuditPageRequest,
) (ports.AuditPage, error) {
	s.reads++
	s.lastRequest = request
	s.requests = append(s.requests, request)
	start := 0
	if request.After != nil {
		for index, event := range s.events {
			if event.OccurredAt().After(request.After.OccurredAt) ||
				(event.OccurredAt().Equal(request.After.OccurredAt) &&
					event.ID().String() > request.After.EventID.String()) {
				start = index
				break
			}
			start = len(s.events)
		}
	}
	filtered := make([]audit.Event, 0, len(s.events)-start)
	for _, event := range s.events[start:] {
		if applicationAuditEventMatchesRequest(event, request.Filter) {
			filtered = append(filtered, event)
		}
	}
	end := request.Limit
	if end > len(filtered) {
		end = len(filtered)
	}
	page := ports.AuditPage{Events: append([]audit.Event(nil), filtered[:end]...)}
	if end < len(filtered) && len(page.Events) > 0 {
		last := page.Events[len(page.Events)-1]
		page.Next = &ports.AuditCursor{OccurredAt: last.OccurredAt(), EventID: last.ID()}
	}
	return page, nil
}

func (s *applicationAuditStore) GetAuditEvent(
	_ context.Context,
	_ audit.InstallationID,
	eventID audit.EventID,
) (audit.Event, error) {
	for _, event := range s.events {
		if event.ID() == eventID {
			return event, nil
		}
	}
	return audit.Event{}, ports.ErrAuditEventNotFound
}

func applicationAuditEventMatchesRequest(event audit.Event, filter ports.AuditFilter) bool {
	if len(filter.ProjectIDs) > 0 {
		matches := false
		for _, projectID := range filter.ProjectIDs {
			if event.Scope().ProjectID() == projectID {
				matches = true
				break
			}
		}
		if !matches {
			return false
		}
	}
	if filter.Action.String() != "" && event.Action() != filter.Action {
		return false
	}
	return true
}

func (s *applicationAuditStore) SelectAuditRetentionCandidates(
	context.Context,
	audit.InstallationID,
	time.Time,
	int,
) ([]ports.AuditRetentionCandidate, error) {
	return nil, errors.New("unexpected retention selection")
}

type applicationAuditRetentionRepository struct {
	candidates        []ports.AuditRetentionCandidate
	selectionLimit    int
	deleteEvaluatedAt time.Time
	deletedIDs        []audit.EventID
	deleteCount       int
}

func (r *applicationAuditRetentionRepository) SelectAuditRetentionCandidates(
	_ context.Context,
	_ audit.InstallationID,
	_ time.Time,
	limit int,
) ([]ports.AuditRetentionCandidate, error) {
	r.selectionLimit = limit
	return append([]ports.AuditRetentionCandidate(nil), r.candidates...), nil
}

func (r *applicationAuditRetentionRepository) DeleteAuditRetentionCandidates(
	_ context.Context,
	_ audit.InstallationID,
	evaluatedAt time.Time,
	ids []audit.EventID,
) (int, error) {
	r.deleteEvaluatedAt = evaluatedAt
	r.deletedIDs = append([]audit.EventID(nil), ids...)
	return r.deleteCount, nil
}

func newApplicationAuditEvent(
	t *testing.T,
	id string,
	projectIDValue string,
	actionValue string,
	occurredAt time.Time,
) audit.Event {
	t.Helper()
	actorValue, actorErr := audit.NewActor(
		mustApplicationAuditConstruct(t, audit.NewPrincipalID, "principal-one"),
		audit.AuthenticationOIDCSession,
		mustApplicationAuditConstruct(t, audit.NewCredentialID, "session-one"),
		nil,
	)
	actor := mustApplicationAuditValue(t, actorValue, actorErr)
	targetValue, targetErr := audit.NewTarget(
		mustApplicationAuditConstruct(t, audit.NewTargetType, "job"),
		mustApplicationAuditConstruct(t, audit.NewTargetID, "job-one"),
	)
	target := mustApplicationAuditValue(t, targetValue, targetErr)
	scopeValue, scopeErr := audit.NewScope(
		mustApplicationAuditConstruct(t, audit.NewInstallationID, "default"),
		mustApplicationAuditConstruct(t, audit.NewProjectID, projectIDValue),
		audit.TeamID{},
		mustApplicationAuditConstruct(t, audit.NewNamespace, "default"),
	)
	scope := mustApplicationAuditValue(t, scopeValue, scopeErr)
	sourceValue, sourceErr := audit.NewTrustworthySource(
		netip.MustParseAddr("192.0.2.10"), audit.SourceDirectPeer, "test",
	)
	source := mustApplicationAuditValue(t, sourceValue, sourceErr)
	eventValue, eventErr := audit.NewEvent(audit.EventInput{
		ID:         mustApplicationAuditConstruct(t, audit.NewEventID, id),
		OccurredAt: occurredAt,
		RequestID:  mustApplicationAuditConstruct(t, audit.NewRequestID, "request-"+id),
		TraceID:    mustApplicationAuditConstruct(t, audit.NewTraceID, "trace-"+id),
		Actor:      actor,
		Action:     mustApplicationAuditConstruct(t, audit.NewAction, actionValue),
		Target:     target,
		Scope:      scope,
		Decision:   audit.DecisionAllow,
		Result:     audit.ResultSuccess,
		Reason:     mustApplicationAuditConstruct(t, audit.NewReasonCode, "request.accepted"),
		Source:     source,
	})
	return mustApplicationAuditValue(t, eventValue, eventErr)
}

func mustApplicationAuditConstruct[T, A any](
	t *testing.T,
	constructor func(A) (T, error),
	input A,
) T {
	t.Helper()
	value, err := constructor(input)
	return mustApplicationAuditValue(t, value, err)
}

func mustApplicationAuditValue[T any](t *testing.T, value T, err error) T {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	return value
}
