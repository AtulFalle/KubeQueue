package policyquota

import (
	"errors"
	"fmt"
)

type Counters struct {
	Concurrent uint64
	Queued     uint64
	Retained   uint64
}

type Usage struct {
	Global    Counters
	Project   Counters
	Namespace Counters
}

var (
	ErrCounterOverflow         = errors.New("quota counter overflow")
	ErrCounterUnderflow        = errors.New("quota counter underflow")
	ErrInvalidReservation      = errors.New("invalid quota reservation")
	ErrInvalidReservationState = errors.New("invalid quota reservation state transition")
)

func (usage Usage) Add(demand Usage) (Usage, error) {
	global, err := addCounters(usage.Global, demand.Global)
	if err != nil {
		return Usage{}, fmt.Errorf("%w: global: %w", ErrCounterOverflow, err)
	}
	project, err := addCounters(usage.Project, demand.Project)
	if err != nil {
		return Usage{}, fmt.Errorf("%w: project: %w", ErrCounterOverflow, err)
	}
	namespace, err := addCounters(usage.Namespace, demand.Namespace)
	if err != nil {
		return Usage{}, fmt.Errorf("%w: namespace: %w", ErrCounterOverflow, err)
	}
	return Usage{Global: global, Project: project, Namespace: namespace}, nil
}

// Release subtracts held quota and rejects any operation that would underflow.
func (usage Usage) Release(held Usage) (Usage, error) {
	global, err := releaseCounters(usage.Global, held.Global)
	if err != nil {
		return Usage{}, fmt.Errorf("%w: global: %w", ErrCounterUnderflow, err)
	}
	project, err := releaseCounters(usage.Project, held.Project)
	if err != nil {
		return Usage{}, fmt.Errorf("%w: project: %w", ErrCounterUnderflow, err)
	}
	namespace, err := releaseCounters(usage.Namespace, held.Namespace)
	if err != nil {
		return Usage{}, fmt.Errorf("%w: namespace: %w", ErrCounterUnderflow, err)
	}
	return Usage{Global: global, Project: project, Namespace: namespace}, nil
}

func addCounters(current, demand Counters) (Counters, error) {
	concurrent, ok := checkedAdd(current.Concurrent, demand.Concurrent)
	if !ok {
		return Counters{}, errors.New("concurrency")
	}
	queued, ok := checkedAdd(current.Queued, demand.Queued)
	if !ok {
		return Counters{}, errors.New("queued jobs")
	}
	retained, ok := checkedAdd(current.Retained, demand.Retained)
	if !ok {
		return Counters{}, errors.New("retained jobs")
	}
	return Counters{Concurrent: concurrent, Queued: queued, Retained: retained}, nil
}

func releaseCounters(current, held Counters) (Counters, error) {
	if held.Concurrent > current.Concurrent {
		return Counters{}, errors.New("concurrency")
	}
	if held.Queued > current.Queued {
		return Counters{}, errors.New("queued jobs")
	}
	if held.Retained > current.Retained {
		return Counters{}, errors.New("retained jobs")
	}
	return Counters{
		Concurrent: current.Concurrent - held.Concurrent,
		Queued:     current.Queued - held.Queued,
		Retained:   current.Retained - held.Retained,
	}, nil
}

func checkedAdd(left, right uint64) (uint64, bool) {
	sum := left + right
	return sum, sum >= left
}

type RejectionReason string

const (
	ReasonGlobalConcurrency    RejectionReason = "quota.global_concurrency_exceeded"
	ReasonGlobalQueued         RejectionReason = "quota.global_queued_jobs_exceeded"
	ReasonGlobalRetained       RejectionReason = "quota.global_retained_jobs_exceeded"
	ReasonProjectConcurrency   RejectionReason = "quota.project_concurrency_exceeded"
	ReasonProjectQueued        RejectionReason = "quota.project_queued_jobs_exceeded"
	ReasonProjectRetained      RejectionReason = "quota.project_retained_jobs_exceeded"
	ReasonNamespaceConcurrency RejectionReason = "quota.namespace_concurrency_exceeded"
	ReasonNamespaceQueued      RejectionReason = "quota.namespace_queued_jobs_exceeded"
	ReasonNamespaceRetained    RejectionReason = "quota.namespace_retained_jobs_exceeded"
	ReasonIdempotencyConflict  RejectionReason = "quota.idempotency_key_conflict"
)

type Remediation string

const (
	RemediationWaitForCapacity Remediation = "WAIT_FOR_CAPACITY"
	RemediationDeleteRetained  Remediation = "DELETE_RETAINED_JOBS"
	RemediationUseNewKey       Remediation = "USE_NEW_IDEMPOTENCY_KEY"
)

// Rejection is deliberately bounded and stable so adapters can expose it
// without leaking persistence or scheduler details.
type Rejection struct {
	Policy      PolicyRef
	Scope       Scope
	Metric      string
	Current     uint64
	Limit       uint64
	Reason      RejectionReason
	Remediation Remediation
}

type ReservationState string

const (
	ReservationIntent   ReservationState = "INTENT"
	ReservationReserved ReservationState = "RESERVED"
	ReservationReleased ReservationState = "RELEASED"
)

type ReleaseCause string

const (
	ReleaseCompleted ReleaseCause = "COMPLETED"
	ReleaseCancelled ReleaseCause = "CANCELLED"
	ReleaseFailed    ReleaseCause = "FAILED"
)

type Reservation struct {
	IdempotencyKey string
	JobID          string
	Policy         PolicyRef
	Demand         Usage
	State          ReservationState
	ReleaseCause   ReleaseCause
}

type ReservationRequest struct {
	IdempotencyKey string
	JobID          string
	Demand         Usage
}

type ReservationDecision struct {
	Accepted    bool
	Usage       Usage
	Reservation Reservation
	Rejection   *Rejection
	Replay      bool
}

// DecideReservation is intended to run inside the caller's transaction.
// Persisting the returned usage and INTENT together gives serial transactions
// deterministic quota behavior. Existing is the durable reservation found by
// idempotency key, if any.
func DecideReservation(policy EffectivePolicy, usage Usage, request ReservationRequest, existing *Reservation) (ReservationDecision, error) {
	if request.IdempotencyKey == "" || request.JobID == "" {
		return ReservationDecision{}, fmt.Errorf("%w: idempotency key and job ID are required", ErrInvalidReservation)
	}
	decisionPolicy, err := effectivePolicyRef(policy)
	if err != nil {
		return ReservationDecision{}, err
	}
	if existing != nil {
		if existing.IdempotencyKey == request.IdempotencyKey &&
			existing.JobID == request.JobID &&
			existing.Demand == request.Demand {
			return ReservationDecision{
				Accepted:    true,
				Usage:       usage,
				Reservation: *existing,
				Replay:      true,
			}, nil
		}
		rejection := Rejection{
			Policy:      decisionPolicy,
			Scope:       decisionPolicy.Scope,
			Metric:      "idempotency_key",
			Reason:      ReasonIdempotencyConflict,
			Remediation: RemediationUseNewKey,
		}
		return ReservationDecision{Usage: usage, Rejection: &rejection}, nil
	}

	if rejection := firstRejection(policy, usage, request.Demand); rejection != nil {
		return ReservationDecision{Usage: usage, Rejection: rejection}, nil
	}
	next, err := usage.Add(request.Demand)
	if err != nil {
		return ReservationDecision{}, err
	}
	reservation := Reservation{
		IdempotencyKey: request.IdempotencyKey,
		JobID:          request.JobID,
		Policy:         decisionPolicy,
		Demand:         request.Demand,
		State:          ReservationIntent,
	}
	return ReservationDecision{Accepted: true, Usage: next, Reservation: reservation}, nil
}

func effectivePolicyRef(policy EffectivePolicy) (PolicyRef, error) {
	if len(policy.Applied) == 0 {
		return PolicyRef{}, fmt.Errorf("%w: effective policy has no applied version", ErrInvalidReservation)
	}
	return policy.Applied[len(policy.Applied)-1], nil
}

type quotaCheck struct {
	scope       Scope
	metric      string
	current     uint64
	demand      uint64
	limit       uint64
	reason      RejectionReason
	remediation Remediation
}

func firstRejection(policy EffectivePolicy, usage, demand Usage) *Rejection {
	ref := policy.Applied[len(policy.Applied)-1]
	checks := quotaChecks(ref.Scope, policy.Rules.Quotas, usage, demand)
	for _, check := range checks {
		if check.current > check.limit || check.demand > check.limit-check.current {
			return &Rejection{
				Policy:      ref,
				Scope:       check.scope,
				Metric:      check.metric,
				Current:     check.current,
				Limit:       check.limit,
				Reason:      check.reason,
				Remediation: check.remediation,
			}
		}
	}
	return nil
}

func quotaChecks(target Scope, limits QuotaLimits, usage, demand Usage) []quotaCheck {
	globalScope := Scope{Kind: ScopeInstallation}
	projectScope := Scope{Kind: ScopeProject, Project: target.Project}
	namespaceScope := Scope{Kind: ScopeNamespace, Project: target.Project, Namespace: target.Namespace}
	return []quotaCheck{
		newCheck(globalScope, "concurrent_jobs", usage.Global.Concurrent, demand.Global.Concurrent, limits.Global.MaxConcurrent, ReasonGlobalConcurrency, RemediationWaitForCapacity),
		newCheck(globalScope, "queued_jobs", usage.Global.Queued, demand.Global.Queued, limits.Global.MaxQueued, ReasonGlobalQueued, RemediationWaitForCapacity),
		newCheck(globalScope, "retained_jobs", usage.Global.Retained, demand.Global.Retained, limits.Global.MaxRetained, ReasonGlobalRetained, RemediationDeleteRetained),
		newCheck(projectScope, "concurrent_jobs", usage.Project.Concurrent, demand.Project.Concurrent, limits.Project.MaxConcurrent, ReasonProjectConcurrency, RemediationWaitForCapacity),
		newCheck(projectScope, "queued_jobs", usage.Project.Queued, demand.Project.Queued, limits.Project.MaxQueued, ReasonProjectQueued, RemediationWaitForCapacity),
		newCheck(projectScope, "retained_jobs", usage.Project.Retained, demand.Project.Retained, limits.Project.MaxRetained, ReasonProjectRetained, RemediationDeleteRetained),
		newCheck(namespaceScope, "concurrent_jobs", usage.Namespace.Concurrent, demand.Namespace.Concurrent, limits.Namespace.MaxConcurrent, ReasonNamespaceConcurrency, RemediationWaitForCapacity),
		newCheck(namespaceScope, "queued_jobs", usage.Namespace.Queued, demand.Namespace.Queued, limits.Namespace.MaxQueued, ReasonNamespaceQueued, RemediationWaitForCapacity),
		newCheck(namespaceScope, "retained_jobs", usage.Namespace.Retained, demand.Namespace.Retained, limits.Namespace.MaxRetained, ReasonNamespaceRetained, RemediationDeleteRetained),
	}
}

func newCheck(scope Scope, metric string, current, demand uint64, limit *uint64, reason RejectionReason, remediation Remediation) quotaCheck {
	// Compose guarantees complete limits. Keeping this function total makes
	// manually constructed EffectivePolicy values deny safely at zero.
	var value uint64
	if limit != nil {
		value = *limit
	}
	return quotaCheck{
		scope: scope, metric: metric, current: current, demand: demand,
		limit: value, reason: reason, remediation: remediation,
	}
}

func (reservation Reservation) MarkReserved() (Reservation, error) {
	switch reservation.State {
	case ReservationIntent:
		reservation.State = ReservationReserved
		return reservation, nil
	case ReservationReserved:
		return reservation, nil
	case ReservationReleased:
		return Reservation{}, fmt.Errorf("%w: cannot reserve a released intent", ErrInvalidReservationState)
	default:
		return Reservation{}, fmt.Errorf("%w: unknown state %q", ErrInvalidReservationState, reservation.State)
	}
}

// Release is idempotent. Completion, cancellation, and failure all release
// exactly the counters held by this reservation.
func (reservation Reservation) Release(usage Usage, cause ReleaseCause) (Reservation, Usage, error) {
	if cause != ReleaseCompleted && cause != ReleaseCancelled && cause != ReleaseFailed {
		return Reservation{}, Usage{}, fmt.Errorf("%w: unknown release cause %q", ErrInvalidReservationState, cause)
	}
	if reservation.State == ReservationReleased {
		return reservation, usage, nil
	}
	if reservation.State != ReservationIntent && reservation.State != ReservationReserved {
		return Reservation{}, Usage{}, fmt.Errorf("%w: unknown state %q", ErrInvalidReservationState, reservation.State)
	}
	next, err := usage.Release(reservation.Demand)
	if err != nil {
		return Reservation{}, Usage{}, err
	}
	reservation.State = ReservationReleased
	reservation.ReleaseCause = cause
	return reservation, next, nil
}
