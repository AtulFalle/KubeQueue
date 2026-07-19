package policyquota

import (
	"errors"
	"math"
	"reflect"
	"testing"
)

func TestConcurrentReservationDecisionsAreDeterministic(t *testing.T) {
	policy := quotaTestPolicy(t, 1)
	demand := Usage{
		Global:    Counters{Concurrent: 1},
		Project:   Counters{Concurrent: 1},
		Namespace: Counters{Concurrent: 1},
	}

	first, err := DecideReservation(policy, Usage{}, ReservationRequest{
		IdempotencyKey: "request-1",
		JobID:          "job-1",
		Demand:         demand,
	}, nil)
	if err != nil {
		t.Fatalf("first DecideReservation() error = %v", err)
	}
	if !first.Accepted {
		t.Fatalf("first decision rejected: %+v", first.Rejection)
	}

	second, err := DecideReservation(policy, first.Usage, ReservationRequest{
		IdempotencyKey: "request-2",
		JobID:          "job-2",
		Demand:         demand,
	}, nil)
	if err != nil {
		t.Fatalf("second DecideReservation() error = %v", err)
	}
	if second.Accepted || second.Rejection == nil {
		t.Fatal("second decision accepted, want deterministic rejection")
	}
	if second.Rejection.Reason != ReasonGlobalConcurrency {
		t.Errorf("reason = %q, want %q", second.Rejection.Reason, ReasonGlobalConcurrency)
	}
	if second.Rejection.Current != 1 || second.Rejection.Limit != 1 {
		t.Errorf("usage details = current %d, limit %d", second.Rejection.Current, second.Rejection.Limit)
	}
	if second.Usage != first.Usage {
		t.Errorf("rejected decision changed usage: got %+v want %+v", second.Usage, first.Usage)
	}
}

func TestReservationReplayAndIdempotencyConflict(t *testing.T) {
	policy := quotaTestPolicy(t, 10)
	request := ReservationRequest{
		IdempotencyKey: "request-1",
		JobID:          "job-1",
		Demand: Usage{
			Global:    Counters{Queued: 1},
			Project:   Counters{Queued: 1},
			Namespace: Counters{Queued: 1},
		},
	}
	first, err := DecideReservation(policy, Usage{}, request, nil)
	if err != nil {
		t.Fatalf("DecideReservation() error = %v", err)
	}

	replay, err := DecideReservation(policy, first.Usage, request, &first.Reservation)
	if err != nil {
		t.Fatalf("replay error = %v", err)
	}
	if !replay.Accepted || !replay.Replay || replay.Usage != first.Usage {
		t.Errorf("replay = %+v", replay)
	}

	conflicting := request
	conflicting.JobID = "job-2"
	conflict, err := DecideReservation(policy, first.Usage, conflicting, &first.Reservation)
	if err != nil {
		t.Fatalf("conflict error = %v", err)
	}
	if conflict.Accepted || conflict.Rejection == nil ||
		conflict.Rejection.Reason != ReasonIdempotencyConflict ||
		conflict.Rejection.Remediation != RemediationUseNewKey {
		t.Errorf("conflict = %+v", conflict)
	}
}

func TestReleaseIsIdempotentForTerminalCauses(t *testing.T) {
	tests := []ReleaseCause{ReleaseCompleted, ReleaseCancelled, ReleaseFailed}
	for _, cause := range tests {
		t.Run(string(cause), func(t *testing.T) {
			policy := quotaTestPolicy(t, 10)
			demand := Usage{
				Global:    Counters{Concurrent: 1, Queued: 1, Retained: 1},
				Project:   Counters{Concurrent: 1, Queued: 1, Retained: 1},
				Namespace: Counters{Concurrent: 1, Queued: 1, Retained: 1},
			}
			decision, err := DecideReservation(policy, Usage{}, ReservationRequest{
				IdempotencyKey: "request-1",
				JobID:          "job-1",
				Demand:         demand,
			}, nil)
			if err != nil {
				t.Fatalf("DecideReservation() error = %v", err)
			}
			reserved, err := decision.Reservation.MarkReserved()
			if err != nil {
				t.Fatalf("MarkReserved() error = %v", err)
			}
			released, usage, err := reserved.Release(decision.Usage, cause)
			if err != nil {
				t.Fatalf("Release() error = %v", err)
			}
			if usage != (Usage{}) || released.State != ReservationReleased || released.ReleaseCause != cause {
				t.Errorf("first release = reservation %+v usage %+v", released, usage)
			}

			replayed, replayUsage, err := released.Release(usage, cause)
			if err != nil {
				t.Fatalf("idempotent Release() error = %v", err)
			}
			if !reflect.DeepEqual(replayed, released) || replayUsage != usage {
				t.Errorf("idempotent release changed state: reservation %+v usage %+v", replayed, replayUsage)
			}
		})
	}
}

func TestUsageBounds(t *testing.T) {
	tests := []struct {
		name    string
		current Usage
		change  Usage
		release bool
		wantErr error
	}{
		{
			name:    "addition overflow",
			current: Usage{Global: Counters{Queued: math.MaxUint64}},
			change:  Usage{Global: Counters{Queued: 1}},
			wantErr: ErrCounterOverflow,
		},
		{
			name:    "release underflow",
			current: Usage{Namespace: Counters{Retained: 1}},
			change:  Usage{Namespace: Counters{Retained: 2}},
			release: true,
			wantErr: ErrCounterUnderflow,
		},
		{
			name:    "exact release",
			current: Usage{Project: Counters{Concurrent: 2}},
			change:  Usage{Project: Counters{Concurrent: 2}},
			release: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var err error
			if test.release {
				_, err = test.current.Release(test.change)
			} else {
				_, err = test.current.Add(test.change)
			}
			if !errors.Is(err, test.wantErr) {
				t.Errorf("error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestStableQuotaRejectionDetails(t *testing.T) {
	policy := quotaTestPolicy(t, 5)
	tests := []struct {
		name        string
		usage       Usage
		demand      Usage
		wantScope   ScopeKind
		wantMetric  string
		wantReason  RejectionReason
		remediation Remediation
	}{
		{
			name:        "global queued",
			usage:       Usage{Global: Counters{Queued: 5}},
			demand:      Usage{Global: Counters{Queued: 1}},
			wantScope:   ScopeInstallation,
			wantMetric:  "queued_jobs",
			wantReason:  ReasonGlobalQueued,
			remediation: RemediationWaitForCapacity,
		},
		{
			name:        "project retained",
			usage:       Usage{Project: Counters{Retained: 5}},
			demand:      Usage{Project: Counters{Retained: 1}},
			wantScope:   ScopeProject,
			wantMetric:  "retained_jobs",
			wantReason:  ReasonProjectRetained,
			remediation: RemediationDeleteRetained,
		},
		{
			name:        "namespace concurrency",
			usage:       Usage{Namespace: Counters{Concurrent: 4}},
			demand:      Usage{Namespace: Counters{Concurrent: 2}},
			wantScope:   ScopeNamespace,
			wantMetric:  "concurrent_jobs",
			wantReason:  ReasonNamespaceConcurrency,
			remediation: RemediationWaitForCapacity,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision, err := DecideReservation(policy, test.usage, ReservationRequest{
				IdempotencyKey: "request-1",
				JobID:          "job-1",
				Demand:         test.demand,
			}, nil)
			if err != nil {
				t.Fatalf("DecideReservation() error = %v", err)
			}
			if decision.Rejection == nil {
				t.Fatal("rejection is nil")
			}
			got := decision.Rejection
			if got.Policy.ID != "namespace" || got.Policy.Version != 3 ||
				got.Scope.Kind != test.wantScope || got.Metric != test.wantMetric ||
				got.Current == 0 || got.Limit != 5 || got.Reason != test.wantReason ||
				got.Remediation != test.remediation {
				t.Errorf("rejection = %+v", *got)
			}
		})
	}
}

func quotaTestPolicy(t *testing.T, limit uint64) EffectivePolicy {
	t.Helper()
	installation := completePolicy("installation", 1, Scope{Kind: ScopeInstallation}, limit)
	project := Policy{
		Ref: PolicyRef{ID: "project", Version: 2, Scope: Scope{Kind: ScopeProject, Project: "project-a"}},
	}
	namespace := Policy{
		Ref: PolicyRef{
			ID:      "namespace",
			Version: 3,
			Scope:   Scope{Kind: ScopeNamespace, Project: "project-a", Namespace: "builds"},
		},
	}
	effective, err := Compose(installation, project, namespace)
	if err != nil {
		t.Fatalf("Compose() error = %v", err)
	}
	return effective
}
