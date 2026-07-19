package ports

import (
	"context"
	"errors"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
)

var (
	ErrNotFound               = errors.New("job not found")
	ErrConflict               = errors.New("version conflict")
	ErrServiceAccountNotFound = errors.New("service account not found")
	ErrCredentialNotFound     = errors.New("service-account credential not found")
	ErrCredentialConflict     = errors.New("service-account credential conflict")
)

type JobFilter struct {
	Status        domain.State
	Namespace     string
	Team          string
	Search        string
	Priority      *int
	ProjectID     domain.ProjectID
	ProjectIDs    []domain.ProjectID
	SyncStatus    domain.SyncStatus
	CreatedAfter  *time.Time
	CreatedBefore *time.Time
	UpdatedAfter  *time.Time
	UpdatedBefore *time.Time
}

type JobSort string

const (
	JobSortQueue         JobSort = "queue"
	JobSortCreatedAt     JobSort = "createdAt"
	JobSortCreatedAtDesc JobSort = "-createdAt"
	JobSortUpdatedAt     JobSort = "updatedAt"
	JobSortUpdatedAtDesc JobSort = "-updatedAt"
	JobSortName          JobSort = "name"
	JobSortNameDesc      JobSort = "-name"
)

func (s JobSort) Valid() bool {
	switch s {
	case JobSortQueue, JobSortCreatedAt, JobSortCreatedAtDesc, JobSortUpdatedAt,
		JobSortUpdatedAtDesc, JobSortName, JobSortNameDesc:
		return true
	default:
		return false
	}
}

type JobPageCursor struct {
	Sort      JobSort
	Priority  int
	Position  int64
	Value     string
	Secondary string
	ID        string
}

type JobPageRequest struct {
	Filter JobFilter
	Sort   JobSort
	Limit  int
	After  *JobPageCursor
}

type JobPage struct {
	Items        []domain.Job
	QueueVersion int64
	Next         *JobPageCursor
}

type EventPageRequest struct {
	Limit  int
	Before int64
}

type EventPage struct {
	Items []domain.Event
	Next  *int64
}

type JobChange struct {
	Cursor int64
	JobID  string
}

type JobChangePage struct {
	Changes []JobChange
	Cursor  int64
	More    bool
}

type Repository interface {
	Ping(context.Context) error
	Create(context.Context, domain.CreateJob) (domain.Job, error)
	Adopt(context.Context, domain.Job) (domain.Job, error)
	List(context.Context, JobFilter) ([]domain.Job, error)
	ListPage(context.Context, JobPageRequest) (JobPage, error)
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
	ReorderProject(context.Context, domain.ProjectID, []string, int64) (int64, error)
	QueueVersion(context.Context) (int64, error)
	EventsPage(context.Context, string, EventPageRequest) (EventPage, error)
	LatestJobChangeCursor(context.Context, []domain.ProjectID) (int64, error)
	JobChanges(context.Context, []domain.ProjectID, int64, int) (JobChangePage, error)
	Eligible(context.Context, int) ([]domain.Job, error)
	AcquireSchedulerLease(context.Context, string, time.Duration) (bool, error)
	ClaimEligible(context.Context, string, int, time.Duration) ([]domain.Job, error)
	ReleaseSchedulerClaim(context.Context, string, string) error
	UpdateWorkerStatus(context.Context, domain.WorkerStatus) error
	WorkerStatus(context.Context) (domain.WorkerStatus, error)
	Close() error
}

// ScopedJobRepository is the defense-in-depth persistence surface consumed by
// authenticated application use cases.
type ScopedJobRepository interface {
	Repository
	NamespaceBinding(context.Context, string) (domain.NamespaceBinding, domain.InstallationID, error)
	GetInProjects(context.Context, string, []domain.ProjectID) (domain.Job, error)
	FacetsInProjects(context.Context, []domain.ProjectID) (domain.JobFacets, error)
	QueueInProjects(context.Context, []domain.ProjectID) ([]domain.Job, int64, error)
}
