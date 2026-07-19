package ports

import (
	"context"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain/policyquota"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/leadership"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/scheduler"
)

const (
	MaxSchedulingProjects          = 200
	MaxSchedulingCandidatesProject = 32
)

type ProjectScheduling struct {
	ProjectID domain.ProjectID
	Weight    uint64
	Version   uint64
}

type FairnessState struct {
	Version uint64
	State   scheduler.State
}

type SchedulingCandidate struct {
	Job                    domain.Job
	Age                    uint64
	Lane                   scheduler.Lane
	EmergencyRequested     bool
	EmergencyAuthorized    bool
	EmergencyAuthorization string
}

type SchedulingProject struct {
	InstallationID domain.InstallationID
	ProjectID      domain.ProjectID
	Weight         uint64
	Candidates     []SchedulingCandidate
}

type AdmissionDecision struct {
	ID                  string
	InstallationID      domain.InstallationID
	Policy              policyquota.PolicyRef
	Scheduling          scheduler.Decision
	QuotaReservationKey string
	DecidedBy           string
	CreatedAt           time.Time
}

type RuntimeAdmissionRequest struct {
	Authority               leadership.Authority
	InstallationID          domain.InstallationID
	ExpectedFairnessVersion uint64
	NextFairnessState       scheduler.State
	Decision                AdmissionDecision
	Policy                  policyquota.EffectivePolicy
	ClaimTTL                time.Duration
	RejectionRetry          time.Duration
}

type RuntimeAdmissionResult struct {
	Fairness FairnessState
	Quota    policyquota.ReservationDecision
}

// SchedulingRepository persists bounded project configuration and the
// compare-and-set state transition associated with an attributable decision.
type SchedulingRepository interface {
	ProjectScheduling(
		context.Context,
		domain.InstallationID,
		[]domain.ProjectID,
	) ([]ProjectScheduling, error)
	CompareAndSetProjectWeight(
		context.Context,
		domain.InstallationID,
		domain.ProjectID,
		uint64,
		uint64,
	) (ProjectScheduling, error)
	FairnessState(
		context.Context,
		domain.InstallationID,
		[]domain.ProjectID,
	) (FairnessState, error)
	CommitSchedulingDecision(
		context.Context,
		domain.InstallationID,
		uint64,
		scheduler.State,
		AdmissionDecision,
	) (FairnessState, error)
	AdmissionDecision(
		context.Context,
		domain.InstallationID,
		string,
	) (AdmissionDecision, error)
}

// RuntimeSchedulingRepository is the concrete bounded scheduling surface used
// by the reconciler. CommitRuntimeAdmission fences and commits fairness, quota,
// claim, and attribution in one transaction.
type RuntimeSchedulingRepository interface {
	SchedulingCandidates(
		context.Context,
		int,
		int,
	) ([]SchedulingProject, error)
	FairnessState(
		context.Context,
		domain.InstallationID,
		[]domain.ProjectID,
	) (FairnessState, error)
	PolicyHierarchy(
		context.Context,
		domain.InstallationID,
		policyquota.Scope,
	) ([]policyquota.Policy, error)
	CommitRuntimeAdmission(
		context.Context,
		RuntimeAdmissionRequest,
	) (RuntimeAdmissionResult, error)
	AbandonRuntimeAdmission(
		context.Context,
		leadership.Authority,
		domain.InstallationID,
		string,
		string,
	) error
}
