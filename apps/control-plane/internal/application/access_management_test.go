package application

import (
	"context"
	"errors"
	"testing"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/ports"
)

type accessRepositoryStub struct {
	ports.AccessManagementRepository
	role      domain.RoleDefinition
	principal domain.ManagedPrincipal
	capture   func(ports.TransactionalAudit)
}

func (s accessRepositoryStub) RoleDefinition(
	context.Context, domain.InstallationID, domain.RoleDefinitionID,
) (domain.RoleDefinition, error) {
	return s.role, nil
}

func (s accessRepositoryStub) Principal(
	context.Context, domain.InstallationID, domain.PrincipalID,
) (domain.ManagedPrincipal, error) {
	return s.principal, nil
}

func (s accessRepositoryStub) CreateProject(
	ctx context.Context,
	project domain.ManagedProject,
) (domain.ManagedProject, error) {
	if record, ok := ports.TransactionalAuditFromContext(ctx); ok && s.capture != nil {
		s.capture(record)
	}
	return project, nil
}

type accessAuthorizerStub struct {
	denied     domain.Permission
	accessible domain.Permission
}

func (s accessAuthorizerStub) Authorize(
	_ context.Context,
	_ domain.Actor,
	permission domain.Permission,
	_ domain.AuthorizationScope,
) error {
	if permission == s.denied {
		return domain.ErrAccessDenied
	}
	return nil
}

func (s accessAuthorizerStub) AccessibleScope(
	_ context.Context, actor domain.Actor, permission domain.Permission,
) (domain.AccessScope, error) {
	if permission != s.accessible {
		return domain.AccessScope{}, domain.ErrAccessDenied
	}
	return domain.AccessScope{
		InstallationID: actor.InstallationID,
		ProjectIDs:     []domain.ProjectID{"platform"},
	}, nil
}

func TestAccessManagementEnforcesCreatorDelegationCeiling(t *testing.T) {
	t.Parallel()
	service, err := NewAccessManagement(
		accessRepositoryStub{}, accessAuthorizerStub{denied: domain.PermissionJobsPause},
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithActor(t.Context(), domain.Actor{
		PrincipalID: "operator", InstallationID: "default",
	})
	_, err = service.CreateRoleDefinition(ctx, PutRoleDefinitionInput{
		ID: "pause_delegator", Name: "Pause delegator", Scope: domain.RoleScopeProject,
		Permissions: []domain.Permission{domain.PermissionJobsPause},
	})
	if !errors.Is(err, domain.ErrDelegationCeiling) {
		t.Fatalf("CreateRoleDefinition() error = %v, want delegation ceiling", err)
	}
}

func TestAccessManagementOnlyLetsOwnersAssignInstallationOwner(t *testing.T) {
	t.Parallel()
	repository := accessRepositoryStub{role: domain.RoleDefinition{
		ID: "installation_owner", InstallationID: "default",
		Name: "Installation Owner", Scope: domain.RoleScopeInstallation,
		Permissions: []domain.Permission{domain.PermissionInternalAll}, BuiltIn: true,
	}}
	service, err := NewAccessManagement(
		repository, accessAuthorizerStub{denied: domain.PermissionInternalAll},
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithActor(t.Context(), domain.Actor{
		PrincipalID: "owner", InstallationID: "default",
	})
	_, err = service.CreateRoleBinding(ctx, PutRoleBindingInput{
		ID: "second_owner", RoleDefinitionID: "installation_owner",
		Scope: domain.RoleScopeInstallation, SubjectKind: domain.BindingSubjectPrincipal,
		PrincipalID: "another_owner",
	})
	if !errors.Is(err, domain.ErrDelegationCeiling) {
		t.Fatalf("CreateRoleBinding() error = %v, want delegation ceiling", err)
	}
}

func TestAccessManagementAggregatesCurrentAccess(t *testing.T) {
	t.Parallel()
	repository := accessRepositoryStub{principal: domain.ManagedPrincipal{
		ID: "operator", InstallationID: "default", Kind: domain.PrincipalKindHuman,
		DisplayName: "Operator",
	}}
	service, err := NewAccessManagement(repository, accessAuthorizerStub{
		denied: domain.PermissionInternalAll, accessible: domain.PermissionJobsRead,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithActor(t.Context(), domain.Actor{
		PrincipalID: "operator", InstallationID: "default",
	})
	access, err := service.CurrentAccess(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if access.InstallationOwner || access.Principal.ID != "operator" ||
		len(access.Permissions) != 1 ||
		access.Permissions[0].Permission != domain.PermissionJobsRead ||
		len(access.Permissions[0].Scope.ProjectIDs) != 1 {
		t.Fatalf("current access = %#v", access)
	}
}

func TestAccessManagementAttributesAdministrativeAudit(t *testing.T) {
	t.Parallel()
	var captured ports.TransactionalAudit
	service, err := NewAccessManagement(
		accessRepositoryStub{capture: func(record ports.TransactionalAudit) {
			captured = record
		}},
		accessAuthorizerStub{},
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithActor(t.Context(), domain.Actor{
		PrincipalID: "operator", InstallationID: "default",
		AuthenticationMethod: "OIDC", CredentialID: "session-one",
	})
	if _, err := service.CreateProject(ctx, "platform", "Platform"); err != nil {
		t.Fatal(err)
	}
	if captured.Event.Actor().PrincipalID().String() != "operator" ||
		captured.Event.Actor().CredentialID().String() != "session-one" ||
		captured.Event.Action().String() != "projects.create" ||
		captured.Event.Target().ID().String() != "platform" ||
		captured.Event.Decision() != "ALLOW" ||
		captured.Event.Result() != "SUCCESS" {
		t.Fatalf("captured administrative audit = %#v", captured.Event)
	}
}
