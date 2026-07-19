package domain

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

type TeamID string
type RoleDefinitionID string
type RoleBindingID string

type PrincipalKind string

const (
	PrincipalKindHuman          PrincipalKind = "HUMAN"
	PrincipalKindServiceAccount PrincipalKind = "SERVICE_ACCOUNT"
	PrincipalKindLegacyAdmin    PrincipalKind = "LEGACY_ADMIN"
)

type RoleScope string

const (
	RoleScopeInstallation RoleScope = "INSTALLATION"
	RoleScopeProject      RoleScope = "PROJECT"
)

type BindingSubjectKind string

const (
	BindingSubjectPrincipal BindingSubjectKind = "PRINCIPAL"
	BindingSubjectTeam      BindingSubjectKind = "TEAM"
)

var (
	ErrAccessResourceNotFound = errors.New("access resource not found")
	ErrAccessConflict         = errors.New("access resource conflict")
	ErrInvalidAccessChange    = errors.New("invalid access change")
	ErrDelegationCeiling      = errors.New("delegation ceiling exceeded")
	ErrNonDelegablePermission = errors.New("permission is not delegable")
	ErrFinalInstallationOwner = errors.New("final installation owner is protected")
)

const (
	DefaultAccessPageSize = 50
	MaxAccessPageSize     = 200
)

type AccessPage struct {
	Limit int
	After string
}

func (p AccessPage) Validate() error {
	if p.Limit < 0 || p.Limit > MaxAccessPageSize || len(p.After) > 128 {
		return fmt.Errorf("%w: limit must be 0..%d and cursor must be bounded",
			ErrInvalidAccessChange, MaxAccessPageSize)
	}
	return nil
}

func (p AccessPage) Normalize() (AccessPage, error) {
	p.After = strings.TrimSpace(p.After)
	if err := p.Validate(); err != nil {
		return AccessPage{}, err
	}
	if p.Limit == 0 {
		p.Limit = DefaultAccessPageSize
	}
	return p, nil
}

type ManagedProject struct {
	ID             ProjectID
	InstallationID InstallationID
	Name           string
	CreatedAt      time.Time
}

type Team struct {
	ID             TeamID
	InstallationID InstallationID
	Name           string
	CreatedAt      time.Time
}

type TeamMembership struct {
	TeamID                   TeamID
	PrincipalID              PrincipalID
	SourceIdentityProviderID string
	CreatedAt                time.Time
}

type ManagedPrincipal struct {
	ID                      PrincipalID
	InstallationID          InstallationID
	Kind                    PrincipalKind
	DisplayName             string
	DisabledAt              *time.Time
	AuthorizationGeneration uint64
	CreatedAt               time.Time
}

type RoleDefinition struct {
	ID             RoleDefinitionID
	InstallationID InstallationID
	Name           string
	Scope          RoleScope
	Permissions    []Permission
	BuiltIn        bool
	Revision       uint64
	CreatedAt      time.Time
}

type RoleBinding struct {
	ID               RoleBindingID
	InstallationID   InstallationID
	RoleDefinitionID RoleDefinitionID
	Scope            RoleScope
	ProjectID        ProjectID
	SubjectKind      BindingSubjectKind
	PrincipalID      PrincipalID
	TeamID           TeamID
	CreatedAt        time.Time
}

type EffectiveGrant struct {
	RoleBindingID    RoleBindingID
	RoleDefinitionID RoleDefinitionID
	RoleName         string
	Scope            RoleScope
	ProjectID        ProjectID
	Permissions      []Permission
	Direct           bool
	ViaTeamID        TeamID
}

type EffectiveAccess struct {
	PrincipalID PrincipalID
	Grants      []EffectiveGrant
}

type EffectivePermission struct {
	Permission Permission
	Scope      AccessScope
}

type CurrentAccess struct {
	Principal         ManagedPrincipal
	InstallationOwner bool
	Permissions       []EffectivePermission
}

func NewManagedProject(
	id ProjectID, installationID InstallationID, name string, createdAt time.Time,
) (ManagedProject, error) {
	project, err := NewProject(id, installationID, name)
	if err != nil {
		return ManagedProject{}, fmt.Errorf("%w: %w", ErrInvalidAccessChange, err)
	}
	return ManagedProject{
		ID: project.ID, InstallationID: project.InstallationID,
		Name: project.Name, CreatedAt: createdAt.UTC(),
	}, nil
}

func NewTeam(
	id TeamID, installationID InstallationID, name string, createdAt time.Time,
) (Team, error) {
	if err := validateIdentityID("team", string(id)); err != nil {
		return Team{}, fmt.Errorf("%w: %w", ErrInvalidAccessChange, err)
	}
	if err := validateIdentityID("installation", string(installationID)); err != nil {
		return Team{}, fmt.Errorf("%w: %w", ErrInvalidAccessChange, err)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return Team{}, fmt.Errorf("%w: team name is required", ErrInvalidAccessChange)
	}
	return Team{ID: id, InstallationID: installationID, Name: name, CreatedAt: createdAt.UTC()}, nil
}

func NewManagedPrincipal(
	id PrincipalID,
	installationID InstallationID,
	kind PrincipalKind,
	displayName string,
	createdAt time.Time,
) (ManagedPrincipal, error) {
	principal, err := NewPrincipal(id, installationID, displayName)
	if err != nil {
		return ManagedPrincipal{}, fmt.Errorf("%w: %w", ErrInvalidAccessChange, err)
	}
	if !kind.Valid() {
		return ManagedPrincipal{}, fmt.Errorf("%w: unknown principal kind", ErrInvalidAccessChange)
	}
	return ManagedPrincipal{
		ID: principal.ID, InstallationID: principal.InstallationID, Kind: kind,
		DisplayName: principal.DisplayName, AuthorizationGeneration: 1,
		CreatedAt: createdAt.UTC(),
	}, nil
}

func NewRoleDefinition(
	id RoleDefinitionID,
	installationID InstallationID,
	name string,
	scope RoleScope,
	permissions []Permission,
	createdAt time.Time,
) (RoleDefinition, error) {
	if err := validateIdentityID("role definition", string(id)); err != nil {
		return RoleDefinition{}, fmt.Errorf("%w: %w", ErrInvalidAccessChange, err)
	}
	if err := validateIdentityID("installation", string(installationID)); err != nil {
		return RoleDefinition{}, fmt.Errorf("%w: %w", ErrInvalidAccessChange, err)
	}
	name = strings.TrimSpace(name)
	if name == "" || !scope.Valid() {
		return RoleDefinition{}, fmt.Errorf("%w: role name and scope are required", ErrInvalidAccessChange)
	}
	normalized, err := NormalizeDelegablePermissions(permissions)
	if err != nil {
		return RoleDefinition{}, err
	}
	role := RoleDefinition{
		ID: id, InstallationID: installationID, Name: name, Scope: scope,
		Permissions: normalized, Revision: 1, CreatedAt: createdAt.UTC(),
	}
	return role, nil
}

func NewRoleBinding(
	id RoleBindingID,
	installationID InstallationID,
	roleID RoleDefinitionID,
	scope RoleScope,
	projectID ProjectID,
	subjectKind BindingSubjectKind,
	principalID PrincipalID,
	teamID TeamID,
	createdAt time.Time,
) (RoleBinding, error) {
	if err := validateIdentityID("role binding", string(id)); err != nil {
		return RoleBinding{}, fmt.Errorf("%w: %w", ErrInvalidAccessChange, err)
	}
	if installationID == "" || roleID == "" || !scope.Valid() {
		return RoleBinding{}, fmt.Errorf("%w: binding installation, role, and scope are required",
			ErrInvalidAccessChange)
	}
	if (scope == RoleScopeProject) != (projectID != "") {
		return RoleBinding{}, fmt.Errorf("%w: project scope must name exactly one project",
			ErrInvalidAccessChange)
	}
	if (subjectKind == BindingSubjectPrincipal && principalID != "" && teamID == "") ||
		(subjectKind == BindingSubjectTeam && teamID != "" && principalID == "") {
		return RoleBinding{
			ID: id, InstallationID: installationID, RoleDefinitionID: roleID,
			Scope: scope, ProjectID: projectID, SubjectKind: subjectKind,
			PrincipalID: principalID, TeamID: teamID, CreatedAt: createdAt.UTC(),
		}, nil
	}
	return RoleBinding{}, fmt.Errorf("%w: binding must name exactly one subject",
		ErrInvalidAccessChange)
}

func (k PrincipalKind) Valid() bool {
	return k == PrincipalKindHuman || k == PrincipalKindServiceAccount ||
		k == PrincipalKindLegacyAdmin
}

func (s RoleScope) Valid() bool {
	return s == RoleScopeInstallation || s == RoleScopeProject
}

func NormalizeDelegablePermissions(permissions []Permission) ([]Permission, error) {
	if len(permissions) == 0 {
		return nil, fmt.Errorf("%w: at least one permission is required", ErrInvalidAccessChange)
	}
	result := slices.Clone(permissions)
	slices.Sort(result)
	result = slices.Compact(result)
	for _, permission := range result {
		if !permission.Valid() {
			return nil, fmt.Errorf("%w: unknown permission %q", ErrInvalidAccessChange, permission)
		}
		if !PermissionDelegable(permission) {
			return nil, fmt.Errorf("%w: %s", ErrNonDelegablePermission, permission)
		}
	}
	return result, nil
}

// PermissionDelegable excludes installation bootstrap/owner-recovery authority.
// Identity-provider administration permissions will also be excluded when they
// are introduced into the stable catalog.
func PermissionDelegable(permission Permission) bool {
	return permission.Valid() && permission != PermissionInternalAll &&
		permission != PermissionAuthenticated
}
