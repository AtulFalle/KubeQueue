package persistence

import (
	"errors"
	"fmt"
	"net/netip"
	"testing"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain/audit"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/ports"
)

func TestAuditStoreAppendIsIdempotentlyRejectedWithoutReplacement(t *testing.T) {
	t.Parallel()
	store := openAuditStore(t, "audit-append-only")
	occurredAt := time.Date(2026, time.July, 19, 10, 0, 0, 0, time.UTC)
	original := newPersistenceAuditEvent(t, "event-001", "jobs.pause", occurredAt)
	replacement := newPersistenceAuditEvent(t, "event-001", "jobs.cancel", occurredAt)
	policy := mustAuditTestConstruct(t, audit.NewRetentionPolicy, 30*24*time.Hour)

	if err := store.AppendAuditEvent(
		t.Context(), original, policy, audit.NoLegalHold(),
	); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendAuditEvent(
		t.Context(), replacement, policy, audit.NoLegalHold(),
	); !errors.Is(err, ports.ErrAuditEventExists) {
		t.Fatalf("duplicate append error = %v, want %v", err, ports.ErrAuditEventExists)
	}

	page, err := store.ReadAuditEvents(t.Context(), ports.AuditPageRequest{
		InstallationID: mustAuditTestConstruct(t, audit.NewInstallationID, "default"),
		Limit:          10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 || page.Events[0].Action().String() != "jobs.pause" {
		t.Fatalf("events = %#v, want unchanged original event", page.Events)
	}
	got, err := store.GetAuditEvent(
		t.Context(),
		mustAuditTestConstruct(t, audit.NewInstallationID, "default"),
		original.ID(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID() != original.ID() || got.Action() != original.Action() {
		t.Fatalf("detail = %#v, want original event", got)
	}
	_, err = store.GetAuditEvent(
		t.Context(),
		mustAuditTestConstruct(t, audit.NewInstallationID, "other"),
		original.ID(),
	)
	if !errors.Is(err, ports.ErrAuditEventNotFound) {
		t.Fatalf("cross-installation detail error = %v, want not found", err)
	}
}

func TestAuditStoreCursorReadsAreBoundedAndStablyOrdered(t *testing.T) {
	t.Parallel()
	store := openAuditStore(t, "audit-cursor")
	base := time.Date(2026, time.July, 19, 11, 0, 0, 0, time.UTC)
	policy := mustAuditTestConstruct(t, audit.NewRetentionPolicy, 24*time.Hour)

	for index := ports.MaxAuditPageSize; index >= 0; index-- {
		id := fmt.Sprintf("event-%03d", index)
		event := newPersistenceAuditEvent(t, id, "jobs.read", base)
		if err := store.AppendAuditEvent(
			t.Context(), event, policy, audit.NoLegalHold(),
		); err != nil {
			t.Fatal(err)
		}
	}

	first, err := store.ReadAuditEvents(t.Context(), ports.AuditPageRequest{
		InstallationID: mustAuditTestConstruct(t, audit.NewInstallationID, "default"),
		Limit:          ports.MaxAuditPageSize + 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Events) != ports.MaxAuditPageSize || first.Next == nil {
		t.Fatalf("first page length/next = %d/%v, want %d/non-nil",
			len(first.Events), first.Next, ports.MaxAuditPageSize)
	}
	for index, event := range first.Events {
		want := fmt.Sprintf("event-%03d", index)
		if event.ID().String() != want {
			t.Fatalf("event %d ID = %q, want %q", index, event.ID().String(), want)
		}
	}

	second, err := store.ReadAuditEvents(t.Context(), ports.AuditPageRequest{
		InstallationID: mustAuditTestConstruct(t, audit.NewInstallationID, "default"),
		Limit:          10,
		After:          first.Next,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Events) != 1 || second.Events[0].ID().String() != "event-200" ||
		second.Next != nil {
		t.Fatalf("second page = %#v, next %v", second.Events, second.Next)
	}
}

func TestAuditStoreAppliesSupportedFiltersBeforeCursorLimit(t *testing.T) {
	t.Parallel()
	store := openAuditStore(t, "audit-filters")
	base := time.Date(2026, time.July, 19, 11, 30, 0, 0, time.UTC)
	policy := mustAuditTestConstruct(t, audit.NewRetentionPolicy, 24*time.Hour)
	fixtures := []persistenceAuditEventFixture{
		{
			id: "event-001", projectID: "project-one", principalID: "principal-one",
			action: "jobs.read", targetType: "job", targetID: "job-one",
			decision: audit.DecisionAllow, result: audit.ResultSuccess, occurredAt: base,
		},
		{
			id: "event-002", projectID: "project-two", principalID: "principal-one",
			action: "jobs.read", targetType: "job", targetID: "job-two",
			decision: audit.DecisionAllow, result: audit.ResultSuccess, occurredAt: base,
		},
		{
			id: "event-003", projectID: "project-one", principalID: "principal-two",
			action: "jobs.pause", targetType: "project", targetID: "project-one",
			decision: audit.DecisionDeny, result: audit.ResultFailure,
			occurredAt: base.Add(time.Second),
		},
	}
	for _, fixture := range fixtures {
		if err := store.AppendAuditEvent(
			t.Context(), newPersistenceAuditEventFixture(t, fixture),
			policy, audit.NoLegalHold(),
		); err != nil {
			t.Fatal(err)
		}
	}
	projectOne := mustAuditTestConstruct(t, audit.NewProjectID, "project-one")
	projectTwo := mustAuditTestConstruct(t, audit.NewProjectID, "project-two")
	tests := []struct {
		name   string
		filter ports.AuditFilter
		want   []string
	}{
		{
			name: "project set",
			filter: ports.AuditFilter{
				ProjectIDs: []audit.ProjectID{projectOne, projectTwo},
			},
			want: []string{"event-001", "event-002", "event-003"},
		},
		{
			name: "principal",
			filter: ports.AuditFilter{
				PrincipalID: mustAuditTestConstruct(t, audit.NewPrincipalID, "principal-two"),
			},
			want: []string{"event-003"},
		},
		{
			name: "action",
			filter: ports.AuditFilter{
				Action: mustAuditTestConstruct(t, audit.NewAction, "jobs.pause"),
			},
			want: []string{"event-003"},
		},
		{
			name: "target",
			filter: ports.AuditFilter{
				TargetType: mustAuditTestConstruct(t, audit.NewTargetType, "job"),
				TargetID:   mustAuditTestConstruct(t, audit.NewTargetID, "job-two"),
			},
			want: []string{"event-002"},
		},
		{
			name: "decision and result",
			filter: ports.AuditFilter{
				Decision: audit.DecisionDeny,
				Result:   audit.ResultFailure,
			},
			want: []string{"event-003"},
		},
		{
			name: "half open occurrence range",
			filter: ports.AuditFilter{
				OccurredFrom: base.Add(time.Second),
				OccurredTo:   base.Add(2 * time.Second),
			},
			want: []string{"event-003"},
		},
		{
			name: "combined project and action",
			filter: ports.AuditFilter{
				ProjectIDs: []audit.ProjectID{projectOne},
				Action:     mustAuditTestConstruct(t, audit.NewAction, "jobs.read"),
			},
			want: []string{"event-001"},
		},
	}
	installationID := mustAuditTestConstruct(t, audit.NewInstallationID, "default")
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			page, err := store.ReadAuditEvents(t.Context(), ports.AuditPageRequest{
				InstallationID: installationID, Filter: test.filter, Limit: 10,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Events) != len(test.want) {
				t.Fatalf("events length = %d, want %d", len(page.Events), len(test.want))
			}
			for index, event := range page.Events {
				if event.ID().String() != test.want[index] {
					t.Fatalf("event %d = %q, want %q", index, event.ID().String(), test.want[index])
				}
			}
		})
	}

	first, err := store.ReadAuditEvents(t.Context(), ports.AuditPageRequest{
		InstallationID: installationID,
		Filter: ports.AuditFilter{
			ProjectIDs: []audit.ProjectID{projectOne},
		},
		Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Events) != 1 || first.Events[0].ID().String() != "event-001" ||
		first.Next == nil {
		t.Fatalf("first filtered cursor page = %#v", first)
	}
	second, err := store.ReadAuditEvents(t.Context(), ports.AuditPageRequest{
		InstallationID: installationID,
		Filter: ports.AuditFilter{
			ProjectIDs: []audit.ProjectID{projectOne},
		},
		Limit: 1, After: first.Next,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Events) != 1 || second.Events[0].ID().String() != "event-003" ||
		second.Next != nil {
		t.Fatalf("second filtered cursor page = %#v", second)
	}
}

func TestAuditStoreRetentionSelectionExcludesActiveLegalHolds(t *testing.T) {
	t.Parallel()
	store := openAuditStore(t, "audit-retention")
	evaluatedAt := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	occurredAt := evaluatedAt.Add(-48 * time.Hour)
	policy := mustAuditTestConstruct(t, audit.NewRetentionPolicy, 24*time.Hour)
	activeUntil := mustAuditTestConstruct(t, audit.NewLegalHoldUntil, evaluatedAt.Add(time.Hour))
	expiredUntil := mustAuditTestConstruct(t, audit.NewLegalHoldUntil, evaluatedAt.Add(-time.Hour))

	fixtures := []struct {
		id   string
		hold audit.LegalHold
	}{
		{id: "eligible-no-hold", hold: audit.NoLegalHold()},
		{id: "held-indefinitely", hold: audit.NewIndefiniteLegalHold()},
		{id: "held-until-future", hold: activeUntil},
		{id: "eligible-expired-hold", hold: expiredUntil},
	}
	for _, fixture := range fixtures {
		event := newPersistenceAuditEvent(t, fixture.id, "audit.retention", occurredAt)
		if err := store.AppendAuditEvent(t.Context(), event, policy, fixture.hold); err != nil {
			t.Fatal(err)
		}
	}

	candidates, err := store.SelectAuditRetentionCandidates(
		t.Context(),
		mustAuditTestConstruct(t, audit.NewInstallationID, "default"),
		evaluatedAt,
		10,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 ||
		candidates[0].EventID.String() != "eligible-expired-hold" ||
		candidates[1].EventID.String() != "eligible-no-hold" {
		t.Fatalf("retention candidates = %#v", candidates)
	}
}

func TestAuditStoreRetentionDeleteRechecksLegalHoldInTransaction(t *testing.T) {
	t.Parallel()
	store := openAuditStore(t, "audit-retention-delete")
	evaluatedAt := time.Date(2026, time.July, 19, 13, 0, 0, 0, time.UTC)
	occurredAt := evaluatedAt.Add(-48 * time.Hour)
	policy := mustAuditTestConstruct(t, audit.NewRetentionPolicy, 24*time.Hour)
	for _, id := range []string{"delete-eligible", "hold-added"} {
		if err := store.AppendAuditEvent(
			t.Context(),
			newPersistenceAuditEvent(t, id, "audit.retention", occurredAt),
			policy,
			audit.NoLegalHold(),
		); err != nil {
			t.Fatal(err)
		}
	}
	candidates, err := store.SelectAuditRetentionCandidates(
		t.Context(),
		mustAuditTestConstruct(t, audit.NewInstallationID, "default"),
		evaluatedAt,
		10,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 {
		t.Fatalf("candidates = %#v, want two", candidates)
	}
	if _, err := store.db.ExecContext(
		t.Context(),
		`UPDATE audit_events SET legal_hold_indefinite=TRUE WHERE id='hold-added'`,
	); err != nil {
		t.Fatal(err)
	}
	ids := []audit.EventID{candidates[0].EventID, candidates[1].EventID}
	deleted, err := store.DeleteAuditRetentionCandidates(
		t.Context(),
		mustAuditTestConstruct(t, audit.NewInstallationID, "default"),
		evaluatedAt,
		ids,
	)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want one", deleted)
	}
	page, err := store.ReadAuditEvents(t.Context(), ports.AuditPageRequest{
		InstallationID: mustAuditTestConstruct(t, audit.NewInstallationID, "default"),
		Limit:          10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 || page.Events[0].ID().String() != "hold-added" {
		t.Fatalf("remaining events = %#v, want held event", page.Events)
	}
}

func openAuditStore(t *testing.T, name string) *Store {
	t.Helper()
	store, err := Open(t.Context(), "file:"+name+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func newPersistenceAuditEvent(
	t *testing.T,
	id string,
	actionValue string,
	occurredAt time.Time,
) audit.Event {
	t.Helper()
	return newPersistenceAuditEventFixture(t, persistenceAuditEventFixture{
		id: id, projectID: "default", principalID: "legacy_admin",
		action: actionValue, targetType: "job", targetID: "job-001",
		decision: audit.DecisionAllow, result: audit.ResultSuccess, occurredAt: occurredAt,
	})
}

type persistenceAuditEventFixture struct {
	id, projectID, principalID string
	action, targetType         string
	targetID                   string
	decision                   audit.AuthorizationDecision
	result                     audit.Result
	occurredAt                 time.Time
}

func newPersistenceAuditEventFixture(
	t *testing.T,
	fixture persistenceAuditEventFixture,
) audit.Event {
	t.Helper()
	actorValue, actorErr := audit.NewActor(
		mustAuditTestConstruct(t, audit.NewPrincipalID, fixture.principalID),
		audit.AuthenticationLegacyToken,
		mustAuditTestConstruct(t, audit.NewCredentialID, "legacy-token"),
		[]audit.Group{mustAuditTestConstruct(t, audit.NewGroup, "installation-owner")},
	)
	actor := mustAuditTestValue(t, actorValue, actorErr)
	targetValue, targetErr := audit.NewTarget(
		mustAuditTestConstruct(t, audit.NewTargetType, fixture.targetType),
		mustAuditTestConstruct(t, audit.NewTargetID, fixture.targetID),
	)
	target := mustAuditTestValue(t, targetValue, targetErr)
	scopeValue, scopeErr := audit.NewScope(
		mustAuditTestConstruct(t, audit.NewInstallationID, "default"),
		mustAuditTestConstruct(t, audit.NewProjectID, fixture.projectID),
		audit.TeamID{},
		mustAuditTestConstruct(t, audit.NewNamespace, "default"),
	)
	scope := mustAuditTestValue(t, scopeValue, scopeErr)
	sourceValue, sourceErr := audit.NewTrustworthySource(
		netip.MustParseAddr("192.0.2.10"),
		audit.SourceDirectPeer,
		"KubeQueue persistence test",
	)
	source := mustAuditTestValue(t, sourceValue, sourceErr)
	beforeValue, beforeErr := audit.NewSummary(
		mustAuditTestConstruct(t, audit.NewSummaryState, "QUEUED"),
		[]audit.SummaryField{mustAuditTestConstruct(t, audit.NewSummaryField, "desired.state")},
		0,
		false,
	)
	before := mustAuditTestValue(t, beforeValue, beforeErr)
	afterValue, afterErr := audit.NewSummary(
		mustAuditTestConstruct(t, audit.NewSummaryState, "PAUSED"),
		[]audit.SummaryField{mustAuditTestConstruct(t, audit.NewSummaryField, "desired.state")},
		0,
		false,
	)
	after := mustAuditTestValue(t, afterValue, afterErr)
	eventValue, eventErr := audit.NewEvent(audit.EventInput{
		ID:         mustAuditTestConstruct(t, audit.NewEventID, fixture.id),
		OccurredAt: fixture.occurredAt,
		RequestID:  mustAuditTestConstruct(t, audit.NewRequestID, "request-001"),
		TraceID:    mustAuditTestConstruct(t, audit.NewTraceID, "trace-001"),
		Actor:      actor,
		Action:     mustAuditTestConstruct(t, audit.NewAction, fixture.action),
		Target:     target,
		Scope:      scope,
		Decision:   fixture.decision,
		Result:     fixture.result,
		Reason:     mustAuditTestConstruct(t, audit.NewReasonCode, "request.accepted"),
		Source:     source,
		Before:     &before,
		After:      &after,
	})
	return mustAuditTestValue(t, eventValue, eventErr)
}

func mustAuditTestConstruct[T, A any](
	t *testing.T,
	constructor func(A) (T, error),
	input A,
) T {
	t.Helper()
	value, err := constructor(input)
	return mustAuditTestValue(t, value, err)
}

func mustAuditTestValue[T any](t *testing.T, value T, err error) T {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	return value
}
