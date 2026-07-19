package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
)

type SupportDiagnosticsRepository interface {
	WorkerStatus(context.Context) (domain.WorkerStatus, error)
	SchemaDiagnostics(context.Context) (domain.SchemaDiagnostics, error)
	SupportLeadershipDiagnostics(context.Context) (domain.LeadershipDiagnostics, error)
	RecentSupportErrorClasses(context.Context, int) ([]domain.SupportErrorClass, error)
}

type AuditWriterStatsReader interface {
	Stats() AuditWriterStats
}

type SupportDiagnostics struct {
	repository  SupportDiagnosticsRepository
	authorizer  Authorizer
	auditWriter AuditWriterStatsReader
	apiVersion  string
}

func NewSupportDiagnostics(
	repository SupportDiagnosticsRepository,
	authorizer Authorizer,
	auditWriter AuditWriterStatsReader,
	apiVersion string,
) *SupportDiagnostics {
	return &SupportDiagnostics{
		repository: repository, authorizer: authorizer,
		auditWriter: auditWriter, apiVersion: boundedVersion(apiVersion),
	}
}

func (s *SupportDiagnostics) Snapshot(ctx context.Context) (domain.SupportDiagnostics, error) {
	if s == nil || s.repository == nil || s.authorizer == nil {
		return domain.SupportDiagnostics{}, errors.New("support diagnostics are not configured")
	}
	actor, err := ActorFromContext(ctx)
	if err != nil {
		return domain.SupportDiagnostics{}, err
	}
	if err := s.authorizer.Authorize(ctx, actor, domain.PermissionSupportDiagnosticsRead,
		domain.AuthorizationScope{InstallationID: actor.InstallationID}); err != nil {
		return domain.SupportDiagnostics{}, err
	}
	worker, err := s.repository.WorkerStatus(ctx)
	if err != nil {
		return domain.SupportDiagnostics{}, fmt.Errorf("read diagnostic worker status: %w", err)
	}
	schema, err := s.repository.SchemaDiagnostics(ctx)
	if err != nil {
		return domain.SupportDiagnostics{}, fmt.Errorf("read diagnostic schema status: %w", err)
	}
	leader, err := s.repository.SupportLeadershipDiagnostics(ctx)
	if err != nil {
		return domain.SupportDiagnostics{}, fmt.Errorf("read diagnostic leadership: %w", err)
	}
	errorClasses, err := s.repository.RecentSupportErrorClasses(ctx, domain.MaxSupportErrorClasses)
	if err != nil {
		return domain.SupportDiagnostics{}, fmt.Errorf("read diagnostic error classes: %w", err)
	}
	result := domain.SupportDiagnostics{
		GeneratedAt: time.Now().UTC(), Schema: schema, Leadership: leader,
		Worker: worker, RecentErrorClasses: errorClasses,
	}
	result.Versions.API = s.apiVersion
	result.Versions.Worker = boundedVersion(worker.ReleaseVersion)
	result.Watch.Mode = worker.WatchMode
	result.Watch.EffectiveNamespaces = nonNilStrings(worker.EffectiveNamespaces)
	result.Watch.ExcludedNamespaces = nonNilStrings(worker.ExcludedNamespaces)
	result.Watch.Namespaces = supportNamespaceStatuses(worker.Namespaces)
	if s.auditWriter != nil {
		result.AuditWriterOverloadCount = s.auditWriter.Stats().Overloaded
	}
	return result, nil
}

func boundedVersion(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if value == "" {
		return "unknown"
	}
	if len(value) > 128 {
		return value[:128]
	}
	return value
}

func supportNamespaceStatuses(
	values []domain.NamespaceAuthorityStatus,
) []domain.NamespaceAuthorityStatus {
	result := make([]domain.NamespaceAuthorityStatus, 0, len(values))
	for _, value := range values {
		value.Message = ""
		result = append(result, value)
	}
	return result
}
