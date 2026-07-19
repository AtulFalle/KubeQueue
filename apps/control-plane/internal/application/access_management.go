package application

import (
	"context"
	"errors"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/ports"
)

type AccessManagement struct {
	repository ports.AccessManagementRepository
	authorizer Authorizer
	now        func() time.Time
}

type UpdatePrincipalInput struct {
	ID          domain.PrincipalID
	DisplayName string
	Disabled    bool
}

type PutMembershipInput struct {
	TeamID      domain.TeamID
	PrincipalID domain.PrincipalID
	Active      bool
}

type PutRoleDefinitionInput struct {
	ID               domain.RoleDefinitionID
	Name             string
	Scope            domain.RoleScope
	Permissions      []domain.Permission
	ExpectedRevision uint64
}

type PutRoleBindingInput struct {
	ID               domain.RoleBindingID
	RoleDefinitionID domain.RoleDefinitionID
	Scope            domain.RoleScope
	ProjectID        domain.ProjectID
	SubjectKind      domain.BindingSubjectKind
	PrincipalID      domain.PrincipalID
	TeamID           domain.TeamID
}

func NewAccessManagement(
	repository ports.AccessManagementRepository,
	authorizer Authorizer,
) (*AccessManagement, error) {
	if repository == nil || authorizer == nil {
		return nil, errors.New("access-management repository and authorizer are required")
	}
	return &AccessManagement{repository: repository, authorizer: authorizer, now: time.Now}, nil
}

func (a *AccessManagement) ListProjects(
	ctx context.Context, page domain.AccessPage,
) ([]domain.ManagedProject, error) {
	actor, err := a.authorize(ctx, domain.PermissionProjectsManage, "")
	if err != nil {
		return nil, err
	}
	if err := page.Validate(); err != nil {
		return nil, err
	}
	return a.repository.ListProjects(ctx, actor.InstallationID, page)
}

func (a *AccessManagement) Project(
	ctx context.Context, id domain.ProjectID,
) (domain.ManagedProject, error) {
	actor, err := a.authorize(ctx, domain.PermissionProjectsManage, id)
	if err != nil {
		return domain.ManagedProject{}, err
	}
	return a.repository.Project(ctx, actor.InstallationID, id)
}

func (a *AccessManagement) CreateProject(
	ctx context.Context, id domain.ProjectID, name string,
) (domain.ManagedProject, error) {
	actor, err := a.authorize(ctx, domain.PermissionProjectsManage, "")
	if err != nil {
		return domain.ManagedProject{}, err
	}
	project, err := domain.NewManagedProject(id, actor.InstallationID, name, a.now())
	if err != nil {
		return domain.ManagedProject{}, err
	}
	ctx, err = withAdministrativeAudit(
		ctx, actor, "projects.create", "project", string(project.ID),
		project.ID, "CREATED", "name",
	)
	if err != nil {
		return domain.ManagedProject{}, err
	}
	return a.repository.CreateProject(ctx, project)
}

func (a *AccessManagement) UpdateProject(
	ctx context.Context, id domain.ProjectID, name string,
) (domain.ManagedProject, error) {
	actor, err := a.authorize(ctx, domain.PermissionProjectsManage, id)
	if err != nil {
		return domain.ManagedProject{}, err
	}
	current, err := a.repository.Project(ctx, actor.InstallationID, id)
	if err != nil {
		return domain.ManagedProject{}, err
	}
	current.Name = name
	project, err := domain.NewManagedProject(
		current.ID, current.InstallationID, current.Name, current.CreatedAt,
	)
	if err != nil {
		return domain.ManagedProject{}, err
	}
	ctx, err = withAdministrativeAudit(
		ctx, actor, "projects.update", "project", string(project.ID),
		project.ID, "UPDATED", "name",
	)
	if err != nil {
		return domain.ManagedProject{}, err
	}
	return a.repository.UpdateProject(ctx, project)
}

func (a *AccessManagement) ListNamespaceBindings(
	ctx context.Context,
	projectID domain.ProjectID,
	page domain.AccessPage,
) ([]domain.NamespaceBinding, error) {
	actor, err := a.authorize(ctx, domain.PermissionNamespaceBindingsManage, projectID)
	if err != nil {
		return nil, err
	}
	if err := page.Validate(); err != nil {
		return nil, err
	}
	bindings, err := a.repository.ListNamespaceBindings(
		ctx, actor.InstallationID, projectID, page,
	)
	if err != nil {
		return nil, err
	}
	status, err := a.repository.WorkerStatus(ctx)
	if err != nil {
		return nil, err
	}
	for index := range bindings {
		applyNamespaceAuthority(&bindings[index], status)
	}
	return bindings, nil
}

func (a *AccessManagement) CreateNamespaceBinding(
	ctx context.Context,
	projectID domain.ProjectID,
	namespace string,
) (domain.NamespaceBinding, error) {
	actor, err := a.authorize(ctx, domain.PermissionNamespaceBindingsManage, projectID)
	if err != nil {
		return domain.NamespaceBinding{}, err
	}
	binding, err := domain.NewNamespaceBinding(projectID, namespace)
	if err != nil {
		return domain.NamespaceBinding{}, domain.ErrInvalidAccessChange
	}
	binding.InstallationID = actor.InstallationID
	ctx, err = withAdministrativeAudit(
		ctx, actor, "namespace_bindings.create", "namespace_binding",
		string(binding.ID), projectID, "CREATED", "namespace", "project_id",
	)
	if err != nil {
		return domain.NamespaceBinding{}, err
	}
	binding, err = a.repository.CreateNamespaceBinding(
		ctx, actor.InstallationID, binding, a.now(),
	)
	if err != nil {
		return domain.NamespaceBinding{}, err
	}
	return a.bindingWithAuthority(ctx, binding)
}

func (a *AccessManagement) ReassignNamespaceBinding(
	ctx context.Context,
	projectID domain.ProjectID,
	namespace string,
) (domain.NamespaceBinding, error) {
	actor, err := ActorFromContext(ctx)
	if err != nil {
		return domain.NamespaceBinding{}, err
	}
	validated, err := domain.NewNamespaceBinding(projectID, namespace)
	if err != nil {
		return domain.NamespaceBinding{}, domain.ErrInvalidAccessChange
	}
	current, err := a.repository.ManagedNamespaceBinding(
		ctx, actor.InstallationID, validated.Namespace,
	)
	if err != nil {
		return domain.NamespaceBinding{}, err
	}
	if _, err := a.authorize(ctx, domain.PermissionNamespaceBindingsManage, current.ProjectID); err != nil {
		return domain.NamespaceBinding{}, err
	}
	if _, err := a.authorize(ctx, domain.PermissionNamespaceBindingsManage, projectID); err != nil {
		return domain.NamespaceBinding{}, err
	}
	if current.ProjectID == projectID {
		return a.bindingWithAuthority(ctx, current)
	}
	ctx, err = withAdministrativeAudit(
		ctx, actor, "namespace_bindings.reassign", "namespace_binding",
		string(current.ID), projectID, "UPDATED", "project_id",
	)
	if err != nil {
		return domain.NamespaceBinding{}, err
	}
	binding, err := a.repository.ReassignNamespaceBinding(
		ctx, actor.InstallationID, validated.Namespace, projectID, a.now(),
	)
	if err != nil {
		return domain.NamespaceBinding{}, err
	}
	return a.bindingWithAuthority(ctx, binding)
}

func (a *AccessManagement) RemoveNamespaceBinding(
	ctx context.Context,
	projectID domain.ProjectID,
	namespace string,
) error {
	actor, err := a.authorize(ctx, domain.PermissionNamespaceBindingsManage, projectID)
	if err != nil {
		return err
	}
	validated, err := domain.NewNamespaceBinding(projectID, namespace)
	if err != nil {
		return domain.ErrInvalidAccessChange
	}
	current, err := a.repository.ManagedNamespaceBinding(
		ctx, actor.InstallationID, validated.Namespace,
	)
	if err != nil {
		return err
	}
	if current.ProjectID != projectID {
		return domain.ErrAccessResourceNotFound
	}
	ctx, err = withAdministrativeAudit(
		ctx, actor, "namespace_bindings.remove", "namespace_binding",
		string(current.ID), projectID, "DELETED", "desired",
	)
	if err != nil {
		return err
	}
	return a.repository.RemoveNamespaceBinding(
		ctx, actor.InstallationID, projectID, validated.Namespace, a.now(),
	)
}

func (a *AccessManagement) bindingWithAuthority(
	ctx context.Context,
	binding domain.NamespaceBinding,
) (domain.NamespaceBinding, error) {
	status, err := a.repository.WorkerStatus(ctx)
	if err != nil {
		return domain.NamespaceBinding{}, err
	}
	applyNamespaceAuthority(&binding, status)
	return binding, nil
}

func applyNamespaceAuthority(binding *domain.NamespaceBinding, status domain.WorkerStatus) {
	binding.AuthorityState = domain.NamespaceAuthorityOutOfScope
	binding.Message = "namespace is not in the worker's effective scope"
	for _, namespace := range status.Namespaces {
		if namespace.Namespace != binding.Namespace {
			continue
		}
		binding.InformerSynced = namespace.InformerSynced
		binding.Authorized = namespace.Authorized
		binding.Message = namespace.Message
		binding.ObservedAt = namespace.ObservedAt
		switch {
		case !namespace.Authorized:
			binding.AuthorityState = domain.NamespaceAuthorityUnauthorized
		case !namespace.InformerSynced:
			binding.AuthorityState = domain.NamespaceAuthorityUnsynchronized
		default:
			binding.AuthorityState = domain.NamespaceAuthorityReady
		}
		return
	}
	for _, namespace := range status.EffectiveNamespaces {
		if namespace == binding.Namespace {
			binding.AuthorityState = domain.NamespaceAuthorityPending
			binding.Message = "worker authority has not been observed"
			return
		}
	}
}

func (a *AccessManagement) ListTeams(
	ctx context.Context, page domain.AccessPage,
) ([]domain.Team, error) {
	actor, err := a.authorize(ctx, domain.PermissionMembersRead, "")
	if err != nil {
		return nil, err
	}
	if err := page.Validate(); err != nil {
		return nil, err
	}
	return a.repository.ListTeams(ctx, actor.InstallationID, page)
}

func (a *AccessManagement) Team(ctx context.Context, id domain.TeamID) (domain.Team, error) {
	actor, err := a.authorize(ctx, domain.PermissionMembersRead, "")
	if err != nil {
		return domain.Team{}, err
	}
	return a.repository.Team(ctx, actor.InstallationID, id)
}

func (a *AccessManagement) CreateTeam(
	ctx context.Context, id domain.TeamID, name string,
) (domain.Team, error) {
	actor, err := a.authorize(ctx, domain.PermissionMembersManage, "")
	if err != nil {
		return domain.Team{}, err
	}
	team, err := domain.NewTeam(id, actor.InstallationID, name, a.now())
	if err != nil {
		return domain.Team{}, err
	}
	ctx, err = withAdministrativeAudit(
		ctx, actor, "teams.create", "team", string(team.ID),
		"", "CREATED", "name",
	)
	if err != nil {
		return domain.Team{}, err
	}
	return a.repository.CreateTeam(ctx, team)
}

func (a *AccessManagement) UpdateTeam(
	ctx context.Context, id domain.TeamID, name string,
) (domain.Team, error) {
	actor, err := a.authorize(ctx, domain.PermissionMembersManage, "")
	if err != nil {
		return domain.Team{}, err
	}
	current, err := a.repository.Team(ctx, actor.InstallationID, id)
	if err != nil {
		return domain.Team{}, err
	}
	team, err := domain.NewTeam(current.ID, current.InstallationID, name, current.CreatedAt)
	if err != nil {
		return domain.Team{}, err
	}
	ctx, err = withAdministrativeAudit(
		ctx, actor, "teams.update", "team", string(team.ID),
		"", "UPDATED", "name",
	)
	if err != nil {
		return domain.Team{}, err
	}
	return a.repository.UpdateTeam(ctx, team)
}

func (a *AccessManagement) ListMemberships(
	ctx context.Context, teamID domain.TeamID, page domain.AccessPage,
) ([]domain.TeamMembership, error) {
	actor, err := a.authorize(ctx, domain.PermissionMembersRead, "")
	if err != nil {
		return nil, err
	}
	if err := page.Validate(); err != nil {
		return nil, err
	}
	return a.repository.ListTeamMemberships(ctx, actor.InstallationID, teamID, page)
}

func (a *AccessManagement) Membership(
	ctx context.Context, teamID domain.TeamID, principalID domain.PrincipalID,
) (domain.TeamMembership, error) {
	actor, err := a.authorize(ctx, domain.PermissionMembersRead, "")
	if err != nil {
		return domain.TeamMembership{}, err
	}
	return a.repository.TeamMembership(
		ctx, actor.InstallationID, teamID, principalID,
	)
}

func (a *AccessManagement) PutMembership(
	ctx context.Context, input PutMembershipInput,
) (domain.TeamMembership, error) {
	actor, err := a.authorize(ctx, domain.PermissionMembersManage, "")
	if err != nil {
		return domain.TeamMembership{}, err
	}
	if input.TeamID == "" || input.PrincipalID == "" {
		return domain.TeamMembership{}, domain.ErrInvalidAccessChange
	}
	if _, err := a.repository.Team(ctx, actor.InstallationID, input.TeamID); err != nil {
		return domain.TeamMembership{}, err
	}
	if _, err := a.repository.Principal(
		ctx, actor.InstallationID, input.PrincipalID,
	); err != nil {
		return domain.TeamMembership{}, err
	}
	if !input.Active {
		ctx, err = withAdministrativeAudit(
			ctx, actor, "memberships.remove", "team_membership",
			string(input.TeamID)+"/"+string(input.PrincipalID),
			"", "DELETED", "membership.active",
		)
		if err != nil {
			return domain.TeamMembership{}, err
		}
		return domain.TeamMembership{}, a.repository.RemoveTeamMembership(
			ctx, actor.InstallationID, input.TeamID, input.PrincipalID,
		)
	}
	ctx, err = withAdministrativeAudit(
		ctx, actor, "memberships.add", "team_membership",
		string(input.TeamID)+"/"+string(input.PrincipalID),
		"", "CREATED", "membership.active",
	)
	if err != nil {
		return domain.TeamMembership{}, err
	}
	return a.repository.PutTeamMembership(ctx, actor.InstallationID, domain.TeamMembership{
		TeamID: input.TeamID, PrincipalID: input.PrincipalID, CreatedAt: a.now().UTC(),
	})
}

func (a *AccessManagement) ListPrincipals(
	ctx context.Context, page domain.AccessPage,
) ([]domain.ManagedPrincipal, error) {
	actor, err := a.authorize(ctx, domain.PermissionMembersRead, "")
	if err != nil {
		return nil, err
	}
	if err := page.Validate(); err != nil {
		return nil, err
	}
	return a.repository.ListPrincipals(ctx, actor.InstallationID, page)
}

func (a *AccessManagement) Principal(
	ctx context.Context, id domain.PrincipalID,
) (domain.ManagedPrincipal, error) {
	actor, err := a.authorize(ctx, domain.PermissionMembersRead, "")
	if err != nil {
		return domain.ManagedPrincipal{}, err
	}
	return a.repository.Principal(ctx, actor.InstallationID, id)
}

func (a *AccessManagement) CreatePrincipal(
	ctx context.Context,
	id domain.PrincipalID,
	kind domain.PrincipalKind,
	displayName string,
) (domain.ManagedPrincipal, error) {
	actor, err := a.authorize(ctx, domain.PermissionMembersManage, "")
	if err != nil {
		return domain.ManagedPrincipal{}, err
	}
	if kind != domain.PrincipalKindHuman {
		// Service accounts and the compatibility principal have dedicated
		// lifecycle paths with additional invariants.
		return domain.ManagedPrincipal{}, domain.ErrInvalidAccessChange
	}
	principal, err := domain.NewManagedPrincipal(
		id, actor.InstallationID, kind, displayName, a.now(),
	)
	if err != nil {
		return domain.ManagedPrincipal{}, err
	}
	return a.repository.CreatePrincipal(ctx, principal)
}

func (a *AccessManagement) UpdatePrincipal(
	ctx context.Context, input UpdatePrincipalInput,
) (domain.ManagedPrincipal, error) {
	actor, err := a.authorize(ctx, domain.PermissionMembersManage, "")
	if err != nil {
		return domain.ManagedPrincipal{}, err
	}
	current, err := a.repository.Principal(ctx, actor.InstallationID, input.ID)
	if err != nil {
		return domain.ManagedPrincipal{}, err
	}
	validated, err := domain.NewManagedPrincipal(
		current.ID, current.InstallationID, current.Kind, input.DisplayName, current.CreatedAt,
	)
	if err != nil {
		return domain.ManagedPrincipal{}, err
	}
	current.DisplayName = validated.DisplayName
	if input.Disabled {
		now := a.now().UTC()
		current.DisabledAt = &now
	} else {
		current.DisabledAt = nil
	}
	return a.repository.UpdatePrincipal(ctx, current)
}

func (a *AccessManagement) ListRoleDefinitions(
	ctx context.Context, page domain.AccessPage,
) ([]domain.RoleDefinition, error) {
	actor, err := a.authorize(ctx, domain.PermissionRolesRead, "")
	if err != nil {
		return nil, err
	}
	if err := page.Validate(); err != nil {
		return nil, err
	}
	return a.repository.ListRoleDefinitions(ctx, actor.InstallationID, page)
}

func (a *AccessManagement) RoleDefinition(
	ctx context.Context, id domain.RoleDefinitionID,
) (domain.RoleDefinition, error) {
	actor, err := a.authorize(ctx, domain.PermissionRolesRead, "")
	if err != nil {
		return domain.RoleDefinition{}, err
	}
	return a.repository.RoleDefinition(ctx, actor.InstallationID, id)
}

func (a *AccessManagement) ListRoleDefinitionRevisions(
	ctx context.Context, id domain.RoleDefinitionID, page domain.AccessPage,
) ([]domain.RoleDefinition, error) {
	actor, err := a.authorize(ctx, domain.PermissionRolesRead, "")
	if err != nil {
		return nil, err
	}
	if err := page.Validate(); err != nil {
		return nil, err
	}
	return a.repository.ListRoleDefinitionRevisions(
		ctx, actor.InstallationID, id, page,
	)
}

func (a *AccessManagement) RoleDefinitionRevision(
	ctx context.Context, id domain.RoleDefinitionID, revision uint64,
) (domain.RoleDefinition, error) {
	actor, err := a.authorize(ctx, domain.PermissionRolesRead, "")
	if err != nil {
		return domain.RoleDefinition{}, err
	}
	if revision == 0 {
		return domain.RoleDefinition{}, domain.ErrInvalidAccessChange
	}
	return a.repository.RoleDefinitionRevision(
		ctx, actor.InstallationID, id, revision,
	)
}

func (a *AccessManagement) CreateRoleDefinition(
	ctx context.Context, input PutRoleDefinitionInput,
) (domain.RoleDefinition, error) {
	actor, err := ActorFromContext(ctx)
	if err != nil {
		return domain.RoleDefinition{}, err
	}
	role, err := domain.NewRoleDefinition(
		input.ID, actor.InstallationID, input.Name, input.Scope, input.Permissions, a.now(),
	)
	if err != nil {
		return domain.RoleDefinition{}, err
	}
	if err := a.authorizeDelegation(ctx, actor, role, ""); err != nil {
		return domain.RoleDefinition{}, err
	}
	ctx, err = withAdministrativeAudit(
		ctx, actor, "roles.create", "role_definition", string(role.ID),
		"", "CREATED", "name", "scope", "permissions",
	)
	if err != nil {
		return domain.RoleDefinition{}, err
	}
	return a.repository.CreateRoleDefinition(ctx, role)
}

func (a *AccessManagement) UpdateRoleDefinition(
	ctx context.Context, input PutRoleDefinitionInput,
) (domain.RoleDefinition, error) {
	actor, err := ActorFromContext(ctx)
	if err != nil {
		return domain.RoleDefinition{}, err
	}
	current, err := a.repository.RoleDefinition(ctx, actor.InstallationID, input.ID)
	if err != nil {
		return domain.RoleDefinition{}, err
	}
	if current.BuiltIn {
		return domain.RoleDefinition{}, domain.ErrInvalidAccessChange
	}
	role, err := domain.NewRoleDefinition(
		current.ID, current.InstallationID, input.Name, input.Scope,
		input.Permissions, current.CreatedAt,
	)
	if err != nil {
		return domain.RoleDefinition{}, err
	}
	if err := a.authorizeDelegation(ctx, actor, role, ""); err != nil {
		return domain.RoleDefinition{}, err
	}
	ctx, err = withAdministrativeAudit(
		ctx, actor, "roles.update", "role_definition", string(role.ID),
		"", "UPDATED", "name", "scope", "permissions",
	)
	if err != nil {
		return domain.RoleDefinition{}, err
	}
	return a.repository.UpdateRoleDefinition(ctx, role, input.ExpectedRevision)
}

func (a *AccessManagement) ListRoleBindings(
	ctx context.Context, page domain.AccessPage,
) ([]domain.RoleBinding, error) {
	actor, err := a.authorize(ctx, domain.PermissionRolesRead, "")
	if err != nil {
		return nil, err
	}
	if err := page.Validate(); err != nil {
		return nil, err
	}
	return a.repository.ListRoleBindings(ctx, actor.InstallationID, page)
}

func (a *AccessManagement) RoleBinding(
	ctx context.Context, id domain.RoleBindingID,
) (domain.RoleBinding, error) {
	actor, err := a.authorize(ctx, domain.PermissionRolesRead, "")
	if err != nil {
		return domain.RoleBinding{}, err
	}
	return a.repository.RoleBinding(ctx, actor.InstallationID, id)
}

func (a *AccessManagement) CreateRoleBinding(
	ctx context.Context, input PutRoleBindingInput,
) (domain.RoleBinding, error) {
	actor, binding, err := a.prepareBinding(ctx, input)
	if err != nil {
		return domain.RoleBinding{}, err
	}
	if err := a.authorizeBindingDelegation(ctx, actor, binding); err != nil {
		return domain.RoleBinding{}, err
	}
	ctx, err = withAdministrativeAudit(
		ctx, actor, "role_bindings.create", "role_binding", string(binding.ID),
		binding.ProjectID, "CREATED", "role_definition", "scope", "subject",
	)
	if err != nil {
		return domain.RoleBinding{}, err
	}
	return a.repository.CreateRoleBinding(ctx, binding)
}

func (a *AccessManagement) UpdateRoleBinding(
	ctx context.Context, input PutRoleBindingInput,
) (domain.RoleBinding, error) {
	actor, binding, err := a.prepareBinding(ctx, input)
	if err != nil {
		return domain.RoleBinding{}, err
	}
	if _, err := a.repository.RoleBinding(ctx, actor.InstallationID, input.ID); err != nil {
		return domain.RoleBinding{}, err
	}
	if err := a.authorizeBindingDelegation(ctx, actor, binding); err != nil {
		return domain.RoleBinding{}, err
	}
	ctx, err = withAdministrativeAudit(
		ctx, actor, "role_bindings.update", "role_binding", string(binding.ID),
		binding.ProjectID, "UPDATED", "role_definition", "scope", "subject",
	)
	if err != nil {
		return domain.RoleBinding{}, err
	}
	return a.repository.UpdateRoleBinding(ctx, binding)
}

func (a *AccessManagement) DeleteRoleBinding(
	ctx context.Context, id domain.RoleBindingID,
) error {
	actor, err := ActorFromContext(ctx)
	if err != nil {
		return err
	}
	binding, err := a.repository.RoleBinding(ctx, actor.InstallationID, id)
	if err != nil {
		return err
	}
	if err := a.authorizer.Authorize(
		ctx, actor, domain.PermissionRolesAssign,
		domain.AuthorizationScope{
			InstallationID: actor.InstallationID,
			ProjectID:      binding.ProjectID,
		},
	); err != nil {
		return err
	}
	if binding.RoleDefinitionID == "installation_owner" {
		if err := a.authorizer.Authorize(
			ctx, actor, domain.PermissionInternalAll,
			domain.AuthorizationScope{InstallationID: actor.InstallationID},
		); err != nil {
			return domain.ErrDelegationCeiling
		}
	}
	ctx, err = withAdministrativeAudit(
		ctx, actor, "role_bindings.delete", "role_binding", string(binding.ID),
		binding.ProjectID, "DELETED", "role_definition", "scope", "subject",
	)
	if err != nil {
		return err
	}
	return a.repository.DeleteRoleBinding(ctx, actor.InstallationID, id)
}

func (a *AccessManagement) EffectiveAccess(
	ctx context.Context, principalID domain.PrincipalID, page domain.AccessPage,
) (domain.EffectiveAccess, error) {
	actor, err := a.authorize(ctx, domain.PermissionRolesRead, "")
	if err != nil {
		return domain.EffectiveAccess{}, err
	}
	if err := page.Validate(); err != nil {
		return domain.EffectiveAccess{}, err
	}
	return a.repository.EffectiveAccess(ctx, actor.InstallationID, principalID, page)
}

func (a *AccessManagement) CurrentAccess(ctx context.Context) (domain.CurrentAccess, error) {
	actor, err := ActorFromContext(ctx)
	if err != nil {
		return domain.CurrentAccess{}, err
	}
	principal, err := a.repository.Principal(ctx, actor.InstallationID, actor.PrincipalID)
	if err != nil {
		return domain.CurrentAccess{}, err
	}
	result := domain.CurrentAccess{Principal: principal}
	if err := a.authorizer.Authorize(
		ctx, actor, domain.PermissionInternalAll,
		domain.AuthorizationScope{InstallationID: actor.InstallationID},
	); err == nil {
		result.InstallationOwner = true
	}
	for _, permission := range domain.PermissionCatalog() {
		if permission == domain.PermissionInternalAll ||
			permission == domain.PermissionAuthenticated {
			continue
		}
		scope, err := a.authorizer.AccessibleScope(ctx, actor, permission)
		if errors.Is(err, domain.ErrAccessDenied) {
			continue
		}
		if err != nil {
			return domain.CurrentAccess{}, err
		}
		result.Permissions = append(result.Permissions, domain.EffectivePermission{
			Permission: permission,
			Scope:      scope,
		})
	}
	return result, nil
}

func (a *AccessManagement) authorize(
	ctx context.Context, permission domain.Permission, projectID domain.ProjectID,
) (domain.Actor, error) {
	actor, err := ActorFromContext(ctx)
	if err != nil {
		return domain.Actor{}, err
	}
	err = a.authorizer.Authorize(ctx, actor, permission, domain.AuthorizationScope{
		InstallationID: actor.InstallationID, ProjectID: projectID,
	})
	return actor, err
}

func (a *AccessManagement) authorizeDelegation(
	ctx context.Context, actor domain.Actor, role domain.RoleDefinition, projectID domain.ProjectID,
) error {
	if _, err := a.authorize(ctx, domain.PermissionRolesDefine, projectID); err != nil {
		return err
	}
	for _, permission := range role.Permissions {
		if !domain.PermissionDelegable(permission) {
			return domain.ErrNonDelegablePermission
		}
		if err := a.authorizer.Authorize(ctx, actor, permission, domain.AuthorizationScope{
			InstallationID: actor.InstallationID, ProjectID: projectID,
		}); err != nil {
			return domain.ErrDelegationCeiling
		}
	}
	return nil
}

func (a *AccessManagement) prepareBinding(
	ctx context.Context, input PutRoleBindingInput,
) (domain.Actor, domain.RoleBinding, error) {
	actor, err := ActorFromContext(ctx)
	if err != nil {
		return domain.Actor{}, domain.RoleBinding{}, err
	}
	binding, err := domain.NewRoleBinding(
		input.ID, actor.InstallationID, input.RoleDefinitionID, input.Scope,
		input.ProjectID, input.SubjectKind, input.PrincipalID, input.TeamID, a.now(),
	)
	return actor, binding, err
}

func (a *AccessManagement) authorizeBindingDelegation(
	ctx context.Context, actor domain.Actor, binding domain.RoleBinding,
) error {
	if _, err := a.authorize(ctx, domain.PermissionRolesAssign, binding.ProjectID); err != nil {
		return err
	}
	role, err := a.repository.RoleDefinition(
		ctx, actor.InstallationID, binding.RoleDefinitionID,
	)
	if err != nil {
		return err
	}
	if role.Scope != binding.Scope {
		return domain.ErrInvalidAccessChange
	}
	if role.BuiltIn && slicesContains(role.Permissions, domain.PermissionInternalAll) {
		if err := a.authorizer.Authorize(
			ctx, actor, domain.PermissionInternalAll,
			domain.AuthorizationScope{InstallationID: actor.InstallationID},
		); err != nil {
			return domain.ErrDelegationCeiling
		}
		return nil
	}
	for _, permission := range role.Permissions {
		if !domain.PermissionDelegable(permission) {
			return domain.ErrNonDelegablePermission
		}
		if err := a.authorizer.Authorize(ctx, actor, permission, domain.AuthorizationScope{
			InstallationID: actor.InstallationID, ProjectID: binding.ProjectID,
		}); err != nil {
			return domain.ErrDelegationCeiling
		}
	}
	return nil
}

func slicesContains(values []domain.Permission, target domain.Permission) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
