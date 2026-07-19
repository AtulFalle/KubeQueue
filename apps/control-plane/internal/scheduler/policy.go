package scheduler

import "errors"

var (
	ErrInvalidPolicy = errors.New("invalid scheduling policy")
	ErrInvalidInput  = errors.New("invalid scheduling input")
	ErrInvalidState  = errors.New("invalid scheduler state")
)

// Lane identifies the scheduling lane already authorized by the application
// layer. The scheduler deliberately does not decide who may use LaneEmergency.
type Lane string

const (
	LaneStandard  Lane = "STANDARD"
	LaneEmergency Lane = "EMERGENCY"
)

// Policy is the versioned configuration applied to one scheduling decision.
// AgingStep is added to a job's priority for each Age unit supplied by the
// caller.
type Policy struct {
	Version   string
	AgingStep int64
}

// Job is the policy-only view of a queued job. Age is an explicit,
// caller-calculated count of elapsed aging intervals so the algorithm does not
// read a clock.
type Job struct {
	ID                     string
	Priority               int64
	Age                    uint64
	Eligible               bool
	Lane                   Lane
	EmergencyRequested     bool
	EmergencyAuthorized    bool
	EmergencyAuthorization string
}

// Project is one weighted scheduling share. Every admitted job has unit cost;
// resource-specific costs are intentionally outside this policy core.
type Project struct {
	ID     string
	Weight uint64
	Jobs   []Job
}

// State is the complete durable state required by deficit round robin.
// NextProjectID names the project whose remaining deficit is considered next.
type State struct {
	NextProjectID string
	Deficits      map[string]uint64
}

// Basis records the inputs and arithmetic that selected a job.
type Basis struct {
	Lane                   Lane
	ProjectWeight          uint64
	DeficitBefore          uint64
	DeficitAfter           uint64
	BasePriority           int64
	Age                    uint64
	AgingStep              int64
	EffectivePriority      int64
	EmergencyRequested     bool
	EmergencyAuthorized    bool
	EmergencyAuthorization string
}

// Decision is an auditable scheduling choice.
type Decision struct {
	ProjectID            string
	JobID                string
	AppliedPolicyVersion string
	Basis                Basis
}

// Outcome returns both the optional decision and the state for the next call.
// Decision is nil when no job is eligible.
type Outcome struct {
	Decision *Decision
	State    State
}
