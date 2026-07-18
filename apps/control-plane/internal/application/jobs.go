package application

import (
	"context"
	"fmt"
	"strings"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/ports"
)

type Jobs struct {
	repository ports.Repository
}

func NewJobs(repository ports.Repository) *Jobs {
	return &Jobs{repository: repository}
}

func (j *Jobs) Create(ctx context.Context, input domain.CreateJob) (domain.Job, error) {
	return j.repository.Create(ctx, input)
}

func (j *Jobs) List(ctx context.Context, filter ports.JobFilter) ([]domain.Job, error) {
	return j.repository.List(ctx, filter)
}

func (j *Jobs) Get(ctx context.Context, id string) (domain.Job, error) {
	return j.repository.Get(ctx, id)
}

func (j *Jobs) Events(ctx context.Context, id string) ([]domain.Event, error) {
	if _, err := j.repository.Get(ctx, id); err != nil {
		return nil, err
	}
	return j.repository.Events(ctx, id)
}

func (j *Jobs) Command(ctx context.Context, id, command string) (domain.Job, error) {
	current, err := j.repository.Get(ctx, id)
	if err != nil {
		return domain.Job{}, err
	}
	switch strings.ToLower(command) {
	case "start", "resume":
		if current.DesiredState == domain.StateQueued && !current.Terminal() {
			return current, nil
		}
		if current.Terminal() {
			return domain.Job{}, terminalCommandError(command)
		}
		return j.repository.SetDesiredState(ctx, id, domain.StateQueued)
	case "pause":
		if current.DesiredState == domain.StatePaused && !current.Terminal() {
			return current, nil
		}
		if current.Terminal() {
			return domain.Job{}, terminalCommandError(command)
		}
		return j.repository.SetDesiredState(ctx, id, domain.StatePaused)
	case "terminate":
		if current.DesiredState == domain.StateCancelled {
			return current, nil
		}
		if current.Terminal() {
			return domain.Job{}, terminalCommandError(command)
		}
		return j.repository.SetDesiredState(ctx, id, domain.StateCancelled)
	case "retry":
		if current.ObservedState != domain.StateFailed &&
			current.DesiredState != domain.StateCancelled {
			return domain.Job{}, fmt.Errorf("%w: only failed or cancelled jobs can be retried",
				domain.ErrInvalidTransition)
		}
		return j.repository.Create(ctx, domain.CreateJob{
			Name:      retryName(current),
			Namespace: current.Namespace, Team: current.Team, Priority: current.Priority,
			Template: current.Template, ParentID: current.ID, Attempt: current.Attempt + 1,
		})
	default:
		return domain.Job{}, fmt.Errorf("unknown command %q", command)
	}
}

func terminalCommandError(command string) error {
	return fmt.Errorf("%w: cannot %s a terminal job", domain.ErrInvalidTransition, command)
}

func retryName(current domain.Job) string {
	base := current.Name
	if current.Attempt > 1 {
		base = strings.TrimSuffix(base, fmt.Sprintf("-retry-%d", current.Attempt))
	}
	suffix := fmt.Sprintf("-retry-%d", current.Attempt+1)
	if len(base)+len(suffix) > 63 {
		base = strings.TrimRight(base[:63-len(suffix)], "-")
	}
	return base + suffix
}
