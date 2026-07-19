package application

import (
	"context"
	"fmt"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/ports"
)

type System struct {
	repository ports.Repository
}

func NewSystem(repository ports.Repository) *System {
	return &System{repository: repository}
}

func (s *System) Status(ctx context.Context) (domain.SystemStatus, error) {
	if err := s.repository.Ping(ctx); err != nil {
		return domain.SystemStatus{}, fmt.Errorf("check database readiness: %w", err)
	}
	worker, err := s.repository.WorkerStatus(ctx)
	if err != nil {
		return domain.SystemStatus{}, fmt.Errorf("read worker status: %w", err)
	}
	result := domain.SystemStatus{Worker: worker, ActiveErrors: make([]domain.StatusError, 0)}
	result.API.Ready = true
	result.Database.Ready = true
	result.Watch.Mode = worker.WatchMode
	result.Watch.EffectiveNamespaces = nonNilStrings(worker.EffectiveNamespaces)
	result.Watch.ExcludedNamespaces = nonNilStrings(worker.ExcludedNamespaces)
	result.Watch.Namespaces = nonNilNamespaceStatuses(worker.Namespaces)
	result.Concurrency.Global = worker.GlobalConcurrency
	result.Concurrency.PerNamespace = worker.NamespaceConcurrency
	result.ReleaseVersion = worker.ReleaseVersion

	if worker.HeartbeatAt == nil || time.Since(worker.HeartbeatAt.UTC()) > workerHeartbeatTimeout {
		result.Worker.State = domain.WorkerStateUnavailable
		result.ActiveErrors = append(result.ActiveErrors, domain.StatusError{
			Scope: "worker", Code: "WORKER_HEARTBEAT_STALE",
			Message: "worker heartbeat is unavailable or stale",
		})
	}
	if worker.ActiveError != "" {
		result.ActiveErrors = append(result.ActiveErrors, domain.StatusError{
			Scope: "worker", Code: "WORKER_DEGRADED", Message: worker.ActiveError,
		})
	}
	for _, namespace := range worker.Namespaces {
		if namespace.Authorized && namespace.InformerSynced {
			continue
		}
		result.ActiveErrors = append(result.ActiveErrors, domain.StatusError{
			Scope: "namespace:" + namespace.Namespace,
			Code:  "NAMESPACE_NOT_READY", Message: namespace.Message,
		})
	}
	return result, nil
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func nonNilNamespaceStatuses(
	values []domain.NamespaceAuthorityStatus,
) []domain.NamespaceAuthorityStatus {
	if values == nil {
		return []domain.NamespaceAuthorityStatus{}
	}
	return values
}
