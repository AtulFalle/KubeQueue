package leadership

import (
	"errors"
	"time"
)

var (
	ErrMutationNotReady       = errors.New("leadership: mutation is not ready")
	ErrObservationRequired    = errors.New("leadership: observation required before retry")
	ErrInvalidMutationOutcome = errors.New("leadership: invalid mutation outcome")
	ErrInvalidObservation     = errors.New("leadership: invalid mutation observation")
)

// MutationState represents only the coordination state around one Kubernetes
// command. It does not claim that command execution is exactly once.
type MutationState uint8

const (
	MutationReady MutationState = iota + 1
	MutationInFlight
	MutationObservationRequired
	MutationSucceeded
)

type MutationOutcome uint8

const (
	OutcomeSucceeded MutationOutcome = iota + 1
	OutcomeFailed
	// OutcomeUncertain means the command may have reached Kubernetes. Neither
	// this leader nor a successor may retry until Kubernetes is observed.
	OutcomeUncertain
)

type MutationObservation uint8

const (
	ObservationEffectPresent MutationObservation = iota + 1
	ObservationEffectAbsent
)

// Mutation tracks the fencing generation which began the latest attempt.
// The zero value is ready for its first attempt.
type Mutation struct {
	State      MutationState
	Generation uint64
}

// MutationRequest identifies one idempotent intended Kubernetes effect. It
// contains identifiers only; manifests, credentials, and request payloads must
// never be persisted with coordination state.
type MutationRequest struct {
	Operation       string
	JobID           string
	AttemptIdentity string
	RequestIdentity string
}

// MutationRecord is the durable coordination envelope around Mutation.
type MutationRecord struct {
	Mutation
	MutationRequest
	AttemptID  string
	ErrorClass string
	StartedAt  time.Time
	UpdatedAt  time.Time
	ObservedAt *time.Time
}

// Begin starts an attempt under current authority.
func (m Mutation) Begin(authority Authority) (Mutation, error) {
	if authority.Holder == "" || authority.Generation == 0 || authority.ValidUntil.IsZero() {
		return m, ErrInvalidAuthority
	}
	switch m.State {
	case 0, MutationReady:
		if authority.Generation < m.Generation {
			return m, ErrStaleGeneration
		}
		return Mutation{State: MutationInFlight, Generation: authority.Generation}, nil
	case MutationObservationRequired:
		return m, ErrObservationRequired
	case MutationInFlight, MutationSucceeded:
		return m, ErrMutationNotReady
	default:
		return m, ErrMutationNotReady
	}
}

// Complete records the known result of an attempt. An uncertain result is
// sticky across generation handoff until Observe resolves it.
func (m Mutation) Complete(generation uint64, outcome MutationOutcome) (Mutation, error) {
	if generation != m.Generation {
		return m, ErrStaleGeneration
	}
	if m.State != MutationInFlight {
		return m, ErrMutationNotReady
	}
	switch outcome {
	case OutcomeSucceeded:
		return Mutation{State: MutationSucceeded, Generation: generation}, nil
	case OutcomeFailed:
		return Mutation{State: MutationReady, Generation: generation}, nil
	case OutcomeUncertain:
		return Mutation{State: MutationObservationRequired, Generation: generation}, nil
	default:
		return m, ErrInvalidMutationOutcome
	}
}

// Observe resolves an uncertain command under a current generation. Presence
// completes it; confirmed absence permits a later retry under that generation.
func (m Mutation) Observe(authority Authority, observation MutationObservation) (Mutation, error) {
	if authority.Holder == "" || authority.Generation == 0 || authority.ValidUntil.IsZero() {
		return m, ErrInvalidAuthority
	}
	if authority.Generation < m.Generation {
		return m, ErrStaleGeneration
	}
	if m.State != MutationObservationRequired {
		return m, ErrObservationRequired
	}
	switch observation {
	case ObservationEffectPresent:
		return Mutation{State: MutationSucceeded, Generation: authority.Generation}, nil
	case ObservationEffectAbsent:
		return Mutation{State: MutationReady, Generation: authority.Generation}, nil
	default:
		return m, ErrInvalidObservation
	}
}
