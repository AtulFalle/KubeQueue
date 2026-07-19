package ports

import (
	"context"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain/policyquota"
)

const MaxAdmissionDecisionPageSize = 200

type ProjectAdmissionConfiguration struct {
	Policy     *policyquota.Policy
	Scheduling ProjectScheduling
}

type AdmissionDecisionCursor struct {
	DecidedAt time.Time
	ID        string
}

type AdmissionDecisionRecord struct {
	ID               string
	ProjectID        domain.ProjectID
	JobID            string
	Accepted         bool
	Reason           string
	PolicyVersion    uint64
	SchedulingWeight uint64
	DecidedAt        time.Time
}

type AdmissionAdministrationRepository interface {
	PolicyRepository
	ProjectScheduling(context.Context, domain.InstallationID, []domain.ProjectID) ([]ProjectScheduling, error)
	ProjectQuotaUsage(context.Context, domain.InstallationID, domain.ProjectID) (policyquota.Counters, error)
	CompareAndSetProjectAdmission(
		context.Context,
		domain.InstallationID,
		domain.ProjectID,
		uint64,
		uint64,
		policyquota.Policy,
		uint64,
	) (ProjectAdmissionConfiguration, error)
	ListAdmissionDecisions(
		context.Context,
		domain.InstallationID,
		domain.ProjectID,
		*AdmissionDecisionCursor,
		int,
	) ([]AdmissionDecisionRecord, error)
}
