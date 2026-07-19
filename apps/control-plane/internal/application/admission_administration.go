package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain/policyquota"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/ports"
	"github.com/google/uuid"
)

type AdmissionAdministration struct {
	repository ports.AdmissionAdministrationRepository
	authorizer Authorizer
}

type ProjectAdmissionSettings struct {
	ProjectID  domain.ProjectID
	Policy     *policyquota.Policy
	Scheduling ports.ProjectScheduling
}

func NewAdmissionAdministration(
	repository ports.AdmissionAdministrationRepository,
	authorizer Authorizer,
) (*AdmissionAdministration, error) {
	if repository == nil || authorizer == nil {
		return nil, errors.New("admission repository and authorizer are required")
	}
	return &AdmissionAdministration{repository: repository, authorizer: authorizer}, nil
}

func (a *AdmissionAdministration) InstallationPolicy(
	ctx context.Context,
) (policyquota.Policy, error) {
	actor, err := a.authorize(ctx, domain.PermissionPoliciesRead, "")
	if err != nil {
		return policyquota.Policy{}, err
	}
	policies, err := a.repository.PolicyHierarchy(
		ctx, actor.InstallationID, policyquota.Scope{Kind: policyquota.ScopeInstallation},
	)
	if err != nil {
		return policyquota.Policy{}, err
	}
	return policies[0], nil
}

func (a *AdmissionAdministration) UpdateInstallationPolicy(
	ctx context.Context,
	expectedVersion uint64,
	rules policyquota.Rules,
) (policyquota.Policy, error) {
	actor, err := a.authorize(ctx, domain.PermissionPoliciesManage, "")
	if err != nil {
		return policyquota.Policy{}, err
	}
	if err := a.authorizer.Authorize(
		ctx, actor, domain.PermissionQuotasManage,
		domain.AuthorizationScope{InstallationID: actor.InstallationID},
	); err != nil {
		return policyquota.Policy{}, err
	}
	policies, err := a.repository.PolicyHierarchy(
		ctx, actor.InstallationID, policyquota.Scope{Kind: policyquota.ScopeInstallation},
	)
	if err != nil {
		return policyquota.Policy{}, err
	}
	current := policies[0]
	if current.Ref.Version != expectedVersion {
		return policyquota.Policy{}, ports.ErrConflict
	}
	next := policyquota.Policy{
		Ref: policyquota.PolicyRef{
			ID: current.Ref.ID, Version: expectedVersion + 1,
			Scope: policyquota.Scope{Kind: policyquota.ScopeInstallation},
		},
		Rules: rules,
	}
	if _, err := policyquota.Compose(next); err != nil {
		return policyquota.Policy{}, err
	}
	ctx, err = withAdministrativeAudit(
		ctx, actor, "policies.update", "admission-policy", next.Ref.ID, "",
		"UPDATED", "rules", "quotas",
	)
	if err != nil {
		return policyquota.Policy{}, err
	}
	if err := a.repository.CompareAndSetPolicy(
		ctx, actor.InstallationID, expectedVersion, next,
	); err != nil {
		return policyquota.Policy{}, err
	}
	return next, nil
}

func (a *AdmissionAdministration) ProjectSettings(
	ctx context.Context,
	projectID domain.ProjectID,
) (ProjectAdmissionSettings, error) {
	actor, err := a.authorize(ctx, domain.PermissionPoliciesRead, projectID)
	if err != nil {
		return ProjectAdmissionSettings{}, err
	}
	policies, err := a.repository.PolicyHierarchy(ctx, actor.InstallationID, policyquota.Scope{
		Kind: policyquota.ScopeProject, Project: string(projectID),
	})
	if err != nil {
		return ProjectAdmissionSettings{}, err
	}
	scheduling, err := a.repository.ProjectScheduling(
		ctx, actor.InstallationID, []domain.ProjectID{projectID},
	)
	if err != nil {
		return ProjectAdmissionSettings{}, err
	}
	var direct *policyquota.Policy
	if last := policies[len(policies)-1]; last.Ref.Scope.Kind == policyquota.ScopeProject {
		value := last
		direct = &value
	}
	return ProjectAdmissionSettings{
		ProjectID: projectID, Policy: direct, Scheduling: scheduling[0],
	}, nil
}

func (a *AdmissionAdministration) UpdateProjectSettings(
	ctx context.Context,
	projectID domain.ProjectID,
	expectedPolicyVersion uint64,
	expectedSchedulingVersion uint64,
	rules policyquota.Rules,
	weight uint64,
) (ProjectAdmissionSettings, error) {
	actor, err := a.authorize(ctx, domain.PermissionPoliciesManage, projectID)
	if err != nil {
		return ProjectAdmissionSettings{}, err
	}
	scope := domain.AuthorizationScope{
		InstallationID: actor.InstallationID, ProjectID: projectID,
	}
	for _, permission := range []domain.Permission{
		domain.PermissionQuotasManage, domain.PermissionQueueProjectReorder,
	} {
		if err := a.authorizer.Authorize(ctx, actor, permission, scope); err != nil {
			return ProjectAdmissionSettings{}, err
		}
	}
	policies, err := a.repository.PolicyHierarchy(ctx, actor.InstallationID, policyquota.Scope{
		Kind: policyquota.ScopeProject, Project: string(projectID),
	})
	if err != nil {
		return ProjectAdmissionSettings{}, err
	}
	scheduling, err := a.repository.ProjectScheduling(
		ctx, actor.InstallationID, []domain.ProjectID{projectID},
	)
	if err != nil {
		return ProjectAdmissionSettings{}, err
	}
	current := ProjectAdmissionSettings{
		ProjectID: projectID, Scheduling: scheduling[0],
	}
	if last := policies[len(policies)-1]; last.Ref.Scope.Kind == policyquota.ScopeProject {
		value := last
		current.Policy = &value
	}
	currentPolicyVersion := uint64(0)
	policyID := uuid.NewString()
	if current.Policy != nil {
		currentPolicyVersion = current.Policy.Ref.Version
		policyID = current.Policy.Ref.ID
	}
	if currentPolicyVersion != expectedPolicyVersion ||
		current.Scheduling.Version != expectedSchedulingVersion {
		return ProjectAdmissionSettings{}, ports.ErrConflict
	}
	next := policyquota.Policy{
		Ref: policyquota.PolicyRef{
			ID: policyID, Version: expectedPolicyVersion + 1,
			Scope: policyquota.Scope{Kind: policyquota.ScopeProject, Project: string(projectID)},
		},
		Rules: rules,
	}
	hierarchy := policies
	if current.Policy != nil {
		hierarchy = hierarchy[:len(hierarchy)-1]
	}
	if _, err := policyquota.Compose(append(hierarchy, next)...); err != nil {
		return ProjectAdmissionSettings{}, fmt.Errorf("validate project admission policy: %w", err)
	}
	ctx, err = withAdministrativeAudit(
		ctx, actor, "projects.admission.update", "project", string(projectID), projectID,
		"UPDATED", "policy", "quotas", "scheduling_weight",
	)
	if err != nil {
		return ProjectAdmissionSettings{}, err
	}
	updated, err := a.repository.CompareAndSetProjectAdmission(
		ctx, actor.InstallationID, projectID, expectedPolicyVersion,
		expectedSchedulingVersion, next, weight,
	)
	if err != nil {
		return ProjectAdmissionSettings{}, err
	}
	return ProjectAdmissionSettings{
		ProjectID: projectID, Policy: updated.Policy, Scheduling: updated.Scheduling,
	}, nil
}

func (a *AdmissionAdministration) ProjectQuotaUsage(
	ctx context.Context,
	projectID domain.ProjectID,
) (policyquota.Counters, error) {
	actor, err := a.authorize(ctx, domain.PermissionQuotasManage, projectID)
	if err != nil {
		return policyquota.Counters{}, err
	}
	return a.repository.ProjectQuotaUsage(ctx, actor.InstallationID, projectID)
}

func (a *AdmissionAdministration) AdmissionDecisions(
	ctx context.Context,
	projectID domain.ProjectID,
	after *ports.AdmissionDecisionCursor,
	limit int,
) ([]ports.AdmissionDecisionRecord, error) {
	actor, err := a.authorize(ctx, domain.PermissionQueueRead, projectID)
	if err != nil {
		return nil, err
	}
	if limit < 1 || limit > ports.MaxAdmissionDecisionPageSize {
		return nil, errors.New("admission decision limit must be between 1 and 200")
	}
	return a.repository.ListAdmissionDecisions(
		ctx, actor.InstallationID, projectID, after, limit,
	)
}

func (a *AdmissionAdministration) authorize(
	ctx context.Context,
	permission domain.Permission,
	projectID domain.ProjectID,
) (domain.Actor, error) {
	actor, err := ActorFromContext(ctx)
	if err != nil {
		return domain.Actor{}, err
	}
	if err := a.authorizer.Authorize(ctx, actor, permission, domain.AuthorizationScope{
		InstallationID: actor.InstallationID, ProjectID: projectID,
	}); err != nil {
		return domain.Actor{}, err
	}
	return actor, nil
}
