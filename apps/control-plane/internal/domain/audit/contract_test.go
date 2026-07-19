package audit_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain/audit"
)

func TestValidationBounds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		run  func() error
		code audit.ErrorCode
	}{
		{
			name: "event ID is required",
			run: func() error {
				_, err := audit.NewEventID("")
				return err
			},
			code: audit.ErrorRequired,
		},
		{
			name: "event ID is bounded",
			run: func() error {
				_, err := audit.NewEventID(strings.Repeat("a", 129))
				return err
			},
			code: audit.ErrorTooLong,
		},
		{
			name: "action grammar is bounded",
			run: func() error {
				_, err := audit.NewAction("Jobs Pause")
				return err
			},
			code: audit.ErrorInvalid,
		},
		{
			name: "namespace is a DNS label",
			run: func() error {
				_, err := audit.NewNamespace("Not_A_Namespace")
				return err
			},
			code: audit.ErrorInvalid,
		},
		{
			name: "effective groups are bounded",
			run: func() error {
				principal := mustValue(audit.NewPrincipalID("principal-1"))
				credential := mustValue(audit.NewCredentialID("session-1"))
				groups := make([]audit.Group, 65)
				for index := range groups {
					groups[index] = mustValue(audit.NewGroup(fmt.Sprintf("group-%02d", index)))
				}
				_, err := audit.NewActor(
					principal,
					audit.AuthenticationOIDCSession,
					credential,
					groups,
				)
				return err
			},
			code: audit.ErrorTooMany,
		},
		{
			name: "user agent is bounded",
			run: func() error {
				_, err := audit.NewTrustworthySource(
					netip.MustParseAddr("192.0.2.10"),
					audit.SourceDirectPeer,
					strings.Repeat("a", 513),
				)
				return err
			},
			code: audit.ErrorTooLong,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := test.run()
			if !audit.IsValidationError(err, test.code) {
				t.Fatalf("error = %v, want validation code %q", err, test.code)
			}
		})
	}
}

func TestSummaryRejectsForbiddenSensitiveFields(t *testing.T) {
	t.Parallel()

	for _, field := range []string{
		"manifest",
		"job.template",
		"environment",
		"access_token",
		"session.cookie",
		"client_secret",
		"database_url",
	} {
		field := field
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			_, err := audit.NewSummaryField(field)
			if !audit.IsValidationError(err, audit.ErrorSensitiveContent) {
				t.Fatalf("NewSummaryField() error = %v", err)
			}
		})
	}
}

func TestEventIsImmutableAndOrderingKeyIsStable(t *testing.T) {
	t.Parallel()

	firstTime := time.Date(2026, time.July, 19, 12, 0, 0, 123, time.UTC)
	secondTime := firstTime.Add(time.Nanosecond)
	first := newEvent(t, "evt-001", firstTime)
	tied := newEvent(t, "evt-002", firstTime)
	second := newEvent(t, "evt-000", secondTime)

	if first.OrderingKey() >= tied.OrderingKey() || tied.OrderingKey() >= second.OrderingKey() {
		t.Fatalf(
			"ordering keys are not timestamp/event-ID ordered: %q, %q, %q",
			first.OrderingKey(),
			tied.OrderingKey(),
			second.OrderingKey(),
		)
	}

	localTime := firstTime.In(time.FixedZone("customer", 5*60*60+30*60))
	sameInstant := newEvent(t, "evt-001", localTime)
	if first.OrderingKey() != sameInstant.OrderingKey() {
		t.Fatalf("same instant produced different keys: %q != %q", first.OrderingKey(), sameInstant.OrderingKey())
	}

	groups := first.Actor().EffectiveGroups()
	groups[0] = mustValue(audit.NewGroup("changed"))
	if first.Actor().EffectiveGroups()[0].String() == "changed" {
		t.Fatal("actor groups were mutable through a getter")
	}

	before, ok := first.Before()
	if !ok {
		t.Fatal("event has no before summary")
	}
	fields := before.ChangedFields()
	fields[0] = mustValue(audit.NewSummaryField("changed.field"))
	beforeAgain, _ := first.Before()
	if beforeAgain.ChangedFields()[0].String() == "changed.field" {
		t.Fatal("summary fields were mutable through a getter")
	}
}

func TestRetentionDecisionHonorsLegalHold(t *testing.T) {
	t.Parallel()

	policy := mustValue(audit.NewRetentionPolicy(30 * 24 * time.Hour))
	occurredAt := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name        string
		evaluatedAt time.Time
		hold        audit.LegalHold
		want        audit.RetentionDecision
	}{
		{
			name:        "policy retains young event",
			evaluatedAt: occurredAt.Add(29 * 24 * time.Hour),
			hold:        audit.NoLegalHold(),
			want:        audit.RetainForPolicy,
		},
		{
			name:        "expired event is deletable",
			evaluatedAt: occurredAt.Add(30 * 24 * time.Hour),
			hold:        audit.NoLegalHold(),
			want:        audit.EligibleForDelete,
		},
		{
			name:        "indefinite hold wins",
			evaluatedAt: occurredAt.Add(365 * 24 * time.Hour),
			hold:        audit.NewIndefiniteLegalHold(),
			want:        audit.RetainForLegalHold,
		},
		{
			name:        "active bounded hold wins",
			evaluatedAt: occurredAt.Add(60 * 24 * time.Hour),
			hold: mustValue(
				audit.NewLegalHoldUntil(occurredAt.Add(61 * 24 * time.Hour)),
			),
			want: audit.RetainForLegalHold,
		},
		{
			name:        "expired bounded hold does not extend retention",
			evaluatedAt: occurredAt.Add(61 * 24 * time.Hour),
			hold: mustValue(
				audit.NewLegalHoldUntil(occurredAt.Add(60 * 24 * time.Hour)),
			),
			want: audit.EligibleForDelete,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := audit.DecideRetention(policy, occurredAt, test.evaluatedAt, test.hold)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("DecideRetention() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestContractProducesNoSecretBearingOutput(t *testing.T) {
	t.Parallel()

	secrets := []string{
		"Bearer abcdefghijklmnop",
		"Cookie: session=very-secret",
		"refresh_token=very-secret",
		"eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ1c2VyLTEifQ.abcdefghijklmnop",
	}
	for _, secret := range secrets {
		_, err := audit.NewTrustworthySource(
			netip.MustParseAddr("2001:db8::10"),
			audit.SourceTrustedProxy,
			secret,
		)
		if !audit.IsValidationError(err, audit.ErrorSensitiveContent) {
			t.Fatalf("secret-bearing user agent accepted: error = %v", err)
		}
		if strings.Contains(fmt.Sprint(err), secret) {
			t.Fatal("validation error disclosed rejected secret")
		}
	}

	event := newEvent(t, "evt-safe", time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC))
	encoded, err := marshalOpaqueJSON(event)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"manifest", "token", "cookie", "secret", "password"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Fatalf("serialized event contains forbidden term %q: %s", forbidden, encoded)
		}
	}
}

func marshalOpaqueJSON(value any) ([]byte, error) {
	return json.Marshal(value)
}

func TestDeniedEventCannotReportSuccess(t *testing.T) {
	t.Parallel()

	input := eventInput(t, "evt-denied", time.Now())
	input.Decision = audit.DecisionDeny
	input.Result = audit.ResultSuccess
	_, err := audit.NewEvent(input)
	if !audit.IsValidationError(err, audit.ErrorInconsistentEvent) {
		t.Fatalf("NewEvent() error = %v", err)
	}
}

func newEvent(t *testing.T, id string, occurredAt time.Time) audit.Event {
	t.Helper()
	event, err := audit.NewEvent(eventInput(t, id, occurredAt))
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func eventInput(t *testing.T, id string, occurredAt time.Time) audit.EventInput {
	t.Helper()

	principal := mustValue(audit.NewPrincipalID("principal-1"))
	credential := mustValue(audit.NewCredentialID("session-1"))
	groups := []audit.Group{
		mustValue(audit.NewGroup("operators")),
		mustValue(audit.NewGroup("project-alpha")),
	}
	actor := mustValue(
		audit.NewActor(principal, audit.AuthenticationOIDCSession, credential, groups),
	)
	target := mustValue(
		audit.NewTarget(
			mustValue(audit.NewTargetType("job")),
			mustValue(audit.NewTargetID("job-1")),
		),
	)
	scope := mustValue(
		audit.NewScope(
			mustValue(audit.NewInstallationID("installation-1")),
			mustValue(audit.NewProjectID("project-1")),
			mustValue(audit.NewTeamID("team-1")),
			mustValue(audit.NewNamespace("batch-jobs")),
		),
	)
	summary := mustValue(
		audit.NewSummary(
			mustValue(audit.NewSummaryState("QUEUED")),
			[]audit.SummaryField{
				mustValue(audit.NewSummaryField("lifecycle.state")),
			},
			1,
			false,
		),
	)

	return audit.EventInput{
		ID:         mustValue(audit.NewEventID(id)),
		OccurredAt: occurredAt,
		RequestID:  mustValue(audit.NewRequestID("request-1")),
		TraceID:    mustValue(audit.NewTraceID("0123456789abcdef0123456789abcdef")),
		Actor:      actor,
		Action:     mustValue(audit.NewAction("jobs.pause")),
		Target:     target,
		Scope:      scope,
		Decision:   audit.DecisionAllow,
		Result:     audit.ResultSuccess,
		Reason:     mustValue(audit.NewReasonCode("authorization.allowed")),
		Source: mustValue(
			audit.NewTrustworthySource(
				netip.MustParseAddr("192.0.2.10"),
				audit.SourceDirectPeer,
				"KubeQueue-Test/1.0",
			),
		),
		Before: &summary,
		After:  &summary,
	}
}

func mustValue[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func TestValidationErrorSupportsErrorsAs(t *testing.T) {
	t.Parallel()

	_, err := audit.NewEventID("")
	var validation *audit.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("error = %v, want *ValidationError", err)
	}
}
