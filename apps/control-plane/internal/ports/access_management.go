package ports

import (
	"context"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
)

// AccessManagementRepository is the bounded persistence surface for access
// administration. Implementations must scope every lookup to an installation
// and return domain.ErrAccessResourceNotFound for absent or cross-scope IDs.
type AccessManagementRepository interface {
	ListProjects(context.Context, domain.InstallationID, domain.AccessPage) ([]domain.ManagedProject, error)
	Project(context.Context, domain.InstallationID, domain.ProjectID) (domain.ManagedProject, error)
	CreateProject(context.Context, domain.ManagedProject) (domain.ManagedProject, error)
	UpdateProject(context.Context, domain.ManagedProject) (domain.ManagedProject, error)
	ListNamespaceBindings(context.Context, domain.InstallationID, domain.ProjectID, domain.AccessPage) ([]domain.NamespaceBinding, error)
	ManagedNamespaceBinding(context.Context, domain.InstallationID, string) (domain.NamespaceBinding, error)
	CreateNamespaceBinding(context.Context, domain.InstallationID, domain.NamespaceBinding, time.Time) (domain.NamespaceBinding, error)
	ReassignNamespaceBinding(context.Context, domain.InstallationID, string, domain.ProjectID, time.Time) (domain.NamespaceBinding, error)
	RemoveNamespaceBinding(context.Context, domain.InstallationID, domain.ProjectID, string, time.Time) error
	WorkerStatus(context.Context) (domain.WorkerStatus, error)

	ListTeams(context.Context, domain.InstallationID, domain.AccessPage) ([]domain.Team, error)
	Team(context.Context, domain.InstallationID, domain.TeamID) (domain.Team, error)
	CreateTeam(context.Context, domain.Team) (domain.Team, error)
	UpdateTeam(context.Context, domain.Team) (domain.Team, error)
	ListTeamMemberships(context.Context, domain.InstallationID, domain.TeamID, domain.AccessPage) ([]domain.TeamMembership, error)
	TeamMembership(context.Context, domain.InstallationID, domain.TeamID, domain.PrincipalID) (domain.TeamMembership, error)
	PutTeamMembership(context.Context, domain.InstallationID, domain.TeamMembership) (domain.TeamMembership, error)
	RemoveTeamMembership(context.Context, domain.InstallationID, domain.TeamID, domain.PrincipalID) error

	ListPrincipals(context.Context, domain.InstallationID, domain.AccessPage) ([]domain.ManagedPrincipal, error)
	Principal(context.Context, domain.InstallationID, domain.PrincipalID) (domain.ManagedPrincipal, error)
	CreatePrincipal(context.Context, domain.ManagedPrincipal) (domain.ManagedPrincipal, error)
	UpdatePrincipal(context.Context, domain.ManagedPrincipal) (domain.ManagedPrincipal, error)

	ListRoleDefinitions(context.Context, domain.InstallationID, domain.AccessPage) ([]domain.RoleDefinition, error)
	RoleDefinition(context.Context, domain.InstallationID, domain.RoleDefinitionID) (domain.RoleDefinition, error)
	ListRoleDefinitionRevisions(context.Context, domain.InstallationID, domain.RoleDefinitionID, domain.AccessPage) ([]domain.RoleDefinition, error)
	RoleDefinitionRevision(context.Context, domain.InstallationID, domain.RoleDefinitionID, uint64) (domain.RoleDefinition, error)
	CreateRoleDefinition(context.Context, domain.RoleDefinition) (domain.RoleDefinition, error)
	UpdateRoleDefinition(context.Context, domain.RoleDefinition, uint64) (domain.RoleDefinition, error)

	ListRoleBindings(context.Context, domain.InstallationID, domain.AccessPage) ([]domain.RoleBinding, error)
	RoleBinding(context.Context, domain.InstallationID, domain.RoleBindingID) (domain.RoleBinding, error)
	CreateRoleBinding(context.Context, domain.RoleBinding) (domain.RoleBinding, error)
	UpdateRoleBinding(context.Context, domain.RoleBinding) (domain.RoleBinding, error)
	DeleteRoleBinding(context.Context, domain.InstallationID, domain.RoleBindingID) error

	EffectiveAccess(context.Context, domain.InstallationID, domain.PrincipalID, domain.AccessPage) (domain.EffectiveAccess, error)
}
