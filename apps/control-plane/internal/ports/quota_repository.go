package ports

import (
	"context"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain/policyquota"
)

// QuotaRepository owns the transaction that compares usage, writes a
// reservation, and updates all hierarchical counters.
type QuotaRepository interface {
	QuotaUsage(
		context.Context,
		domain.InstallationID,
		policyquota.Scope,
	) (policyquota.Usage, error)
	ReserveQuota(
		context.Context,
		domain.InstallationID,
		policyquota.Scope,
		policyquota.EffectivePolicy,
		policyquota.ReservationRequest,
	) (policyquota.ReservationDecision, error)
	MarkQuotaReserved(
		context.Context,
		domain.InstallationID,
		string,
	) (policyquota.Reservation, error)
	ReleaseQuota(
		context.Context,
		domain.InstallationID,
		string,
		policyquota.ReleaseCause,
	) (policyquota.Reservation, policyquota.Usage, error)
}

type QuotaSubmission struct {
	InstallationID domain.InstallationID
	Target         policyquota.Scope
	Policy         policyquota.EffectivePolicy
	IdempotencyKey string
	Job            domain.CreateJob
	Demand         policyquota.Usage
}

type QuotaSubmissionResult struct {
	Job      domain.Job
	Decision policyquota.ReservationDecision
}

// PolicyQuotaJobRepository exposes only operations whose Job and quota writes
// must share one concrete persistence transaction.
type PolicyQuotaJobRepository interface {
	PolicyRepository
	CreateJobWithQuota(context.Context, QuotaSubmission) (QuotaSubmissionResult, error)
	AdmitJobQuota(
		context.Context,
		domain.InstallationID,
		string,
		policyquota.EffectivePolicy,
	) (policyquota.ReservationDecision, error)
	ReleaseJobQuota(
		context.Context,
		domain.InstallationID,
		string,
		policyquota.ReleaseCause,
	) (policyquota.Reservation, policyquota.Usage, error)
}
