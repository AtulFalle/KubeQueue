package ports

import (
	"context"
	"errors"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
)

var (
	ErrNotFound = errors.New("job not found")
	ErrConflict = errors.New("version conflict")
)

type JobFilter struct {
	Status    domain.State
	Namespace string
	Team      string
	Search    string
	Priority  *int
}

type Repository interface {
	Ping(context.Context) error
	Create(context.Context, domain.CreateJob) (domain.Job, error)
	Adopt(context.Context, domain.Job) (domain.Job, error)
	List(context.Context, JobFilter) ([]domain.Job, error)
	Facets(context.Context) (domain.JobFacets, error)
	Queue(context.Context) ([]domain.Job, int64, error)
	Get(context.Context, string) (domain.Job, error)
	SetDesiredState(context.Context, string, domain.State) (domain.Job, error)
	SetObserved(context.Context, string, domain.Observation) (domain.Job, error)
	MarkMissing(context.Context, string, string, string, time.Time) (domain.Job, error)
	MarkOutOfScope(context.Context, string, string, time.Time) (domain.Job, error)
	RecordReconcileError(context.Context, string, string, string, string, string, time.Time) error
	Archive(context.Context, string, time.Time) error
	UpdateQueue(context.Context, string, int, int64, int64, *time.Time) (domain.Job, error)
	Reorder(context.Context, []string, int64) (int64, error)
	QueueVersion(context.Context) (int64, error)
	Events(context.Context, string) ([]domain.Event, error)
	Eligible(context.Context, int) ([]domain.Job, error)
	AcquireSchedulerLease(context.Context, string, time.Duration) (bool, error)
	ClaimEligible(context.Context, string, int, time.Duration) ([]domain.Job, error)
	ReleaseSchedulerClaim(context.Context, string, string) error
	UpdateWorkerStatus(context.Context, domain.WorkerStatus) error
	WorkerStatus(context.Context) (domain.WorkerStatus, error)
	Close() error
}
