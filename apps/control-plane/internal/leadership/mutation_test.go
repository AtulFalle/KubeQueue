package leadership

import (
	"errors"
	"testing"
	"time"
)

func TestUncertainMutationHandoffRequiresObservation(t *testing.T) {
	first := testAuthority("worker-a", 12)
	successor := testAuthority("worker-b", 13)

	mutation, err := (Mutation{}).Begin(first)
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	mutation, err = mutation.Complete(first.Generation, OutcomeUncertain)
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}

	if _, err := mutation.Begin(successor); !errors.Is(err, ErrObservationRequired) {
		t.Fatalf("successor Begin() error = %v, want %v", err, ErrObservationRequired)
	}

	tests := []struct {
		name        string
		observation MutationObservation
		wantState   MutationState
		canRetry    bool
	}{
		{
			name:        "effect present completes without retry",
			observation: ObservationEffectPresent,
			wantState:   MutationSucceeded,
		},
		{
			name:        "effect absent permits successor retry",
			observation: ObservationEffectAbsent,
			wantState:   MutationReady,
			canRetry:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			observed, err := mutation.Observe(successor, tt.observation)
			if err != nil {
				t.Fatalf("Observe() error = %v", err)
			}
			if observed.State != tt.wantState || observed.Generation != successor.Generation {
				t.Fatalf("Observe() = %#v, want state %v at generation %d", observed, tt.wantState, successor.Generation)
			}

			retried, retryErr := observed.Begin(successor)
			if tt.canRetry {
				if retryErr != nil {
					t.Fatalf("Begin() after absent observation error = %v", retryErr)
				}
				if retried.State != MutationInFlight {
					t.Fatalf("Begin() state = %v, want %v", retried.State, MutationInFlight)
				}
				return
			}
			if !errors.Is(retryErr, ErrMutationNotReady) {
				t.Fatalf("Begin() after present observation error = %v, want %v", retryErr, ErrMutationNotReady)
			}
		})
	}
}

func TestMutationRejectsStaleGeneration(t *testing.T) {
	current := testAuthority("worker-b", 21)
	stale := testAuthority("worker-a", 20)

	tests := []struct {
		name    string
		run     func() error
		wantErr error
	}{
		{
			name: "stale leader cannot begin a later-generation retry",
			run: func() error {
				_, err := (Mutation{State: MutationReady, Generation: 21}).Begin(stale)
				return err
			},
			wantErr: ErrStaleGeneration,
		},
		{
			name: "stale leader cannot complete successor attempt",
			run: func() error {
				_, err := (Mutation{State: MutationInFlight, Generation: 21}).Complete(20, OutcomeSucceeded)
				return err
			},
			wantErr: ErrStaleGeneration,
		},
		{
			name: "stale leader cannot resolve uncertain successor state",
			run: func() error {
				_, err := (Mutation{State: MutationObservationRequired, Generation: 21}).Observe(stale, ObservationEffectAbsent)
				return err
			},
			wantErr: ErrStaleGeneration,
		},
		{
			name: "current leader may resolve uncertain state",
			run: func() error {
				_, err := (Mutation{State: MutationObservationRequired, Generation: 21}).Observe(current, ObservationEffectAbsent)
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.run(); !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestMutationOutcomeTransitions(t *testing.T) {
	authority := testAuthority("worker-a", 5)

	tests := []struct {
		name      string
		outcome   MutationOutcome
		wantState MutationState
		wantErr   error
	}{
		{name: "success is terminal", outcome: OutcomeSucceeded, wantState: MutationSucceeded},
		{name: "known failure permits retry", outcome: OutcomeFailed, wantState: MutationReady},
		{name: "uncertainty blocks retry", outcome: OutcomeUncertain, wantState: MutationObservationRequired},
		{name: "invalid outcome is deterministic", outcome: MutationOutcome(255), wantState: MutationInFlight, wantErr: ErrInvalidMutationOutcome},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			started, err := (Mutation{}).Begin(authority)
			if err != nil {
				t.Fatalf("Begin() error = %v", err)
			}
			got, err := started.Complete(authority.Generation, tt.outcome)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Complete() error = %v, want %v", err, tt.wantErr)
			}
			if got.State != tt.wantState {
				t.Fatalf("Complete() state = %v, want %v", got.State, tt.wantState)
			}
		})
	}
}

func testAuthority(holder string, generation uint64) Authority {
	return Authority{
		Holder:     holder,
		Generation: generation,
		ValidUntil: time.Date(2026, time.July, 19, 12, 1, 0, 0, time.UTC),
	}
}
