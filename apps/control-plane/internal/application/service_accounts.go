package application

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain/serviceaccountcredential"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/ports"
	"github.com/google/uuid"
)

type ServiceAccountRepository interface {
	Project(
		context.Context, domain.InstallationID, domain.ProjectID,
	) (domain.ManagedProject, error)
	CreateServiceAccount(
		context.Context, serviceaccountcredential.ServiceAccount,
	) (serviceaccountcredential.ServiceAccount, error)
	ListServiceAccounts(
		context.Context, domain.InstallationID, domain.AccessPage,
	) ([]serviceaccountcredential.ServiceAccount, error)
	ServiceAccount(context.Context, domain.PrincipalID) (serviceaccountcredential.ServiceAccount, error)
	BindServiceAccountOIDCIdentity(
		context.Context, domain.InstallationID, domain.PrincipalID,
		serviceaccountcredential.OIDCIdentity, domain.PrincipalID, time.Time,
	) (serviceaccountcredential.ServiceAccount, error)
	RemoveServiceAccountOIDCIdentity(
		context.Context, domain.InstallationID, domain.PrincipalID,
	) error
	ListNativeCredentialMetadata(
		context.Context, domain.InstallationID, domain.PrincipalID, domain.AccessPage,
	) ([]serviceaccountcredential.CredentialMetadata, error)
	CreateNativeCredential(context.Context, serviceaccountcredential.Credential) error
	NativeCredentialByID(context.Context, string) (
		serviceaccountcredential.Credential, serviceaccountcredential.ServiceAccount, error,
	)
	NativeCredentialByPrefix(context.Context, string) (
		serviceaccountcredential.Credential, serviceaccountcredential.ServiceAccount, error,
	)
	RotateNativeCredential(
		context.Context,
		serviceaccountcredential.Credential,
		serviceaccountcredential.Credential,
	) error
	RevokeNativeCredential(context.Context, string, time.Time) error
	TouchNativeCredential(context.Context, string, time.Time) error
}

type CreateServiceAccountInput struct {
	PrincipalID domain.PrincipalID
	ProjectID   domain.ProjectID
	DisplayName string
}

type IssueServiceAccountCredentialInput struct {
	ServiceAccountPrincipalID domain.PrincipalID
	Permissions               []domain.Permission
	ExpiresAt                 time.Time
}

type RotateServiceAccountCredentialInput struct {
	ServiceAccountPrincipalID domain.PrincipalID
	CredentialID              string
	Permissions               []domain.Permission
	ExpiresAt                 time.Time
	Overlap                   time.Duration
}

type AuthenticatedServiceAccount struct {
	Actor        domain.Actor
	CredentialID string
	Permissions  []domain.Permission
}

type IssuedServiceAccountCredential struct {
	Credential serviceaccountcredential.Credential
	Plaintext  *serviceaccountcredential.OneTimePlaintext
}

type ServiceAccountCredentialRotation struct {
	Previous    serviceaccountcredential.Credential
	Replacement IssuedServiceAccountCredential
}

type ServiceAccounts struct {
	repository ServiceAccountRepository
	authorizer Authorizer
	lifecycle  *serviceaccountcredential.Lifecycle
	delegable  []domain.Permission
	now        func() time.Time
	newID      func() string
}

func NewServiceAccounts(
	repository ServiceAccountRepository,
	authorizer Authorizer,
	lifecycle *serviceaccountcredential.Lifecycle,
	delegablePermissions []domain.Permission,
) (*ServiceAccounts, error) {
	if repository == nil || authorizer == nil || lifecycle == nil ||
		len(delegablePermissions) == 0 {
		return nil, errors.New("service-account repository, authorizer, lifecycle, and delegable permissions are required")
	}
	delegable := slices.Clone(delegablePermissions)
	for _, permission := range delegable {
		if !permission.Valid() || permission == domain.PermissionInternalAll {
			return nil, errors.New("service-account delegable permission policy is invalid")
		}
	}
	slices.Sort(delegable)
	delegable = slices.Compact(delegable)
	return &ServiceAccounts{
		repository: repository,
		authorizer: authorizer,
		lifecycle:  lifecycle,
		delegable:  delegable,
		now:        time.Now,
		newID:      uuid.NewString,
	}, nil
}

func (s *ServiceAccounts) Create(
	ctx context.Context,
	input CreateServiceAccountInput,
) (serviceaccountcredential.ServiceAccount, error) {
	actor, err := ActorFromContext(ctx)
	if err != nil {
		return serviceaccountcredential.ServiceAccount{}, err
	}
	principal, err := domain.NewPrincipal(input.PrincipalID, actor.InstallationID, input.DisplayName)
	if err != nil {
		return serviceaccountcredential.ServiceAccount{}, fmt.Errorf(
			"%w: %w", domain.ErrInvalidAccessChange, err,
		)
	}
	scope := domain.AuthorizationScope{
		InstallationID: actor.InstallationID,
		ProjectID:      input.ProjectID,
	}
	if err := s.authorizer.Authorize(
		ctx, actor, domain.PermissionServiceAccountsManage, scope,
	); err != nil {
		return serviceaccountcredential.ServiceAccount{}, err
	}
	if input.ProjectID != "" {
		if _, err := s.repository.Project(
			ctx, actor.InstallationID, input.ProjectID,
		); err != nil {
			return serviceaccountcredential.ServiceAccount{}, err
		}
	}
	account := serviceaccountcredential.ServiceAccount{
		PrincipalID: principal.ID, InstallationID: principal.InstallationID,
		ProjectID: input.ProjectID, DisplayName: principal.DisplayName,
		CreatedBy: actor.PrincipalID, CreatedAt: s.now().UTC(),
	}
	ctx, err = withAdministrativeAudit(
		ctx, actor, "service_accounts.create", "service_account",
		string(account.PrincipalID), account.ProjectID, "CREATED",
		"display_name", "project_id",
	)
	if err != nil {
		return serviceaccountcredential.ServiceAccount{}, err
	}
	stored, err := s.repository.CreateServiceAccount(ctx, account)
	if err != nil {
		return serviceaccountcredential.ServiceAccount{}, fmt.Errorf("create service account: %w", err)
	}
	return stored, nil
}

func (s *ServiceAccounts) List(
	ctx context.Context, page domain.AccessPage,
) ([]serviceaccountcredential.ServiceAccount, error) {
	actor, err := ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	page, err = page.Normalize()
	if err != nil {
		return nil, err
	}
	if err := s.authorizer.Authorize(
		ctx, actor, domain.PermissionServiceAccountsManage,
		domain.AuthorizationScope{InstallationID: actor.InstallationID},
	); err != nil {
		return nil, err
	}
	return s.repository.ListServiceAccounts(ctx, actor.InstallationID, page)
}

func (s *ServiceAccounts) Get(
	ctx context.Context, principalID domain.PrincipalID,
) (serviceaccountcredential.ServiceAccount, error) {
	_, account, err := s.authorizeAccount(
		ctx, principalID, domain.PermissionServiceAccountsManage,
	)
	return account, err
}

func (s *ServiceAccounts) BindOIDCIdentity(
	ctx context.Context,
	principalID domain.PrincipalID,
	identity serviceaccountcredential.OIDCIdentity,
) (serviceaccountcredential.ServiceAccount, error) {
	actor, account, err := s.authorizeAccount(
		ctx, principalID, domain.PermissionServiceAccountsManage,
	)
	if err != nil {
		return serviceaccountcredential.ServiceAccount{}, err
	}
	if identity.Issuer == "" || strings.TrimSpace(identity.Issuer) != identity.Issuer ||
		identity.Subject == "" || strings.TrimSpace(identity.Subject) != identity.Subject ||
		len(identity.Issuer) > 2048 || len(identity.Subject) > 512 {
		return serviceaccountcredential.ServiceAccount{}, domain.ErrInvalidAccessChange
	}
	ctx, err = withAdministrativeAudit(
		ctx, actor, "service_accounts.oidc_identity.bind", "service_account",
		string(account.PrincipalID), account.ProjectID, "UPDATED", "oidc_identity",
	)
	if err != nil {
		return serviceaccountcredential.ServiceAccount{}, err
	}
	return s.repository.BindServiceAccountOIDCIdentity(
		ctx, actor.InstallationID, account.PrincipalID, identity,
		actor.PrincipalID, s.now().UTC(),
	)
}

func (s *ServiceAccounts) RemoveOIDCIdentity(
	ctx context.Context,
	principalID domain.PrincipalID,
) error {
	actor, account, err := s.authorizeAccount(
		ctx, principalID, domain.PermissionServiceAccountsManage,
	)
	if err != nil {
		return err
	}
	ctx, err = withAdministrativeAudit(
		ctx, actor, "service_accounts.oidc_identity.remove", "service_account",
		string(account.PrincipalID), account.ProjectID, "UPDATED", "oidc_identity",
	)
	if err != nil {
		return err
	}
	return s.repository.RemoveServiceAccountOIDCIdentity(
		ctx, actor.InstallationID, account.PrincipalID,
	)
}

func (s *ServiceAccounts) ListCredentials(
	ctx context.Context,
	principalID domain.PrincipalID,
	page domain.AccessPage,
) ([]serviceaccountcredential.CredentialMetadata, error) {
	actor, account, err := s.authorizeAccount(
		ctx, principalID, domain.PermissionTokensManage,
	)
	if err != nil {
		return nil, err
	}
	page, err = page.Normalize()
	if err != nil {
		return nil, err
	}
	return s.repository.ListNativeCredentialMetadata(
		ctx, actor.InstallationID, account.PrincipalID, page,
	)
}

func (s *ServiceAccounts) CredentialMetadata(
	ctx context.Context,
	principalID domain.PrincipalID,
	credentialID string,
) (serviceaccountcredential.CredentialMetadata, error) {
	credential, account, err := s.repository.NativeCredentialByID(ctx, credentialID)
	if err != nil {
		return serviceaccountcredential.CredentialMetadata{}, err
	}
	actor, err := ActorFromContext(ctx)
	if err != nil {
		return serviceaccountcredential.CredentialMetadata{}, err
	}
	if account.PrincipalID != principalID {
		return serviceaccountcredential.CredentialMetadata{}, ports.ErrCredentialNotFound
	}
	if account.InstallationID != actor.InstallationID {
		return serviceaccountcredential.CredentialMetadata{}, ports.ErrCredentialNotFound
	}
	if _, err := s.authorize(ctx, account, domain.PermissionTokensManage); err != nil {
		return serviceaccountcredential.CredentialMetadata{}, err
	}
	return credential.Metadata(), nil
}

func (s *ServiceAccounts) Issue(
	ctx context.Context,
	input IssueServiceAccountCredentialInput,
) (IssuedServiceAccountCredential, error) {
	actor, account, err := s.authorizeAccount(
		ctx, input.ServiceAccountPrincipalID, domain.PermissionTokensManage,
	)
	if err != nil {
		return IssuedServiceAccountCredential{}, err
	}
	issued, err := s.issue(
		ctx, actor, account, input.Permissions, input.ExpiresAt, s.now().UTC(),
	)
	if err != nil {
		return IssuedServiceAccountCredential{}, err
	}
	credential := serviceaccountcredential.Credential{
		ID: s.newID(), ServiceAccountPrincipalID: account.PrincipalID, Stored: issued.Stored,
	}
	ctx, err = withAdministrativeAudit(
		ctx, actor, "credentials.issue", "service_account_credential",
		credential.ID, account.ProjectID, "CREATED", "permissions", "expires_at",
	)
	if err != nil {
		return IssuedServiceAccountCredential{}, err
	}
	if err := s.repository.CreateNativeCredential(ctx, credential); err != nil {
		return IssuedServiceAccountCredential{}, fmt.Errorf("store service-account credential: %w", err)
	}
	return IssuedServiceAccountCredential{Credential: credential, Plaintext: issued.Plaintext}, nil
}

func (s *ServiceAccounts) Rotate(
	ctx context.Context,
	input RotateServiceAccountCredentialInput,
) (ServiceAccountCredentialRotation, error) {
	if strings.TrimSpace(input.CredentialID) == "" {
		return ServiceAccountCredentialRotation{}, serviceaccountcredential.ErrInvalidRequest
	}
	previous, account, err := s.repository.NativeCredentialByID(ctx, input.CredentialID)
	if err != nil {
		return ServiceAccountCredentialRotation{}, err
	}
	requestActor, err := ActorFromContext(ctx)
	if err != nil {
		return ServiceAccountCredentialRotation{}, err
	}
	if input.ServiceAccountPrincipalID != "" &&
		account.PrincipalID != input.ServiceAccountPrincipalID {
		return ServiceAccountCredentialRotation{}, ports.ErrCredentialNotFound
	}
	if account.InstallationID != requestActor.InstallationID {
		return ServiceAccountCredentialRotation{}, ports.ErrCredentialNotFound
	}
	actor, err := s.authorize(ctx, account, domain.PermissionTokensManage)
	if err != nil {
		return ServiceAccountCredentialRotation{}, err
	}
	now := s.now().UTC()
	request, err := s.issueRequest(ctx, actor, account, input.Permissions, input.ExpiresAt)
	if err != nil {
		return ServiceAccountCredentialRotation{}, err
	}
	rotation, err := s.lifecycle.Rotate(previous.Stored, request, input.Overlap, now)
	if err != nil {
		return ServiceAccountCredentialRotation{}, err
	}
	replacement := serviceaccountcredential.Credential{
		ID: s.newID(), ServiceAccountPrincipalID: account.PrincipalID,
		Stored: rotation.Replacement.Stored,
	}
	previous.Stored = rotation.Previous
	ctx, err = withAdministrativeAudit(
		ctx, actor, "credentials.rotate", "service_account_credential",
		previous.ID, account.ProjectID, "ROTATED",
		"credential.lifecycle", "permissions", "expires_at",
	)
	if err != nil {
		return ServiceAccountCredentialRotation{}, err
	}
	if err := s.repository.RotateNativeCredential(ctx, previous, replacement); err != nil {
		return ServiceAccountCredentialRotation{}, fmt.Errorf("rotate service-account credential: %w", err)
	}
	return ServiceAccountCredentialRotation{
		Previous: previous,
		Replacement: IssuedServiceAccountCredential{
			Credential: replacement, Plaintext: rotation.Replacement.Plaintext,
		},
	}, nil
}

func (s *ServiceAccounts) Revoke(ctx context.Context, credentialID string) error {
	credential, account, err := s.repository.NativeCredentialByID(ctx, credentialID)
	if err != nil {
		return err
	}
	actor, err := s.authorize(ctx, account, domain.PermissionTokensManage)
	if err != nil {
		return err
	}
	revoked := serviceaccountcredential.Revoke(credential.Stored, s.now().UTC())
	ctx, err = withAdministrativeAudit(
		ctx, actor, "credentials.revoke", "service_account_credential",
		credential.ID, account.ProjectID, "REVOKED", "credential.lifecycle",
	)
	if err != nil {
		return err
	}
	return s.repository.RevokeNativeCredential(ctx, credential.ID, *revoked.RevokedAt)
}

func (s *ServiceAccounts) RevokeForAccount(
	ctx context.Context,
	principalID domain.PrincipalID,
	credentialID string,
) error {
	credential, account, err := s.repository.NativeCredentialByID(ctx, credentialID)
	if err != nil {
		return err
	}
	actor, err := ActorFromContext(ctx)
	if err != nil {
		return err
	}
	if account.PrincipalID != principalID {
		return ports.ErrCredentialNotFound
	}
	if account.InstallationID != actor.InstallationID {
		return ports.ErrCredentialNotFound
	}
	actor, err = s.authorize(ctx, account, domain.PermissionTokensManage)
	if err != nil {
		return err
	}
	revoked := serviceaccountcredential.Revoke(credential.Stored, s.now().UTC())
	ctx, err = withAdministrativeAudit(
		ctx, actor, "credentials.revoke", "service_account_credential",
		credential.ID, account.ProjectID, "REVOKED", "credential.lifecycle",
	)
	if err != nil {
		return err
	}
	return s.repository.RevokeNativeCredential(ctx, credential.ID, *revoked.RevokedAt)
}

func (s *ServiceAccounts) Authenticate(
	ctx context.Context,
	candidate string,
) (AuthenticatedServiceAccount, error) {
	prefix, err := serviceaccountcredential.Prefix(candidate)
	if err != nil {
		return AuthenticatedServiceAccount{}, err
	}
	credential, account, err := s.repository.NativeCredentialByPrefix(ctx, prefix)
	if errors.Is(err, ports.ErrCredentialNotFound) ||
		errors.Is(err, ports.ErrServiceAccountNotFound) {
		return AuthenticatedServiceAccount{}, serviceaccountcredential.ErrInvalidCredential
	}
	if err != nil {
		return AuthenticatedServiceAccount{}, fmt.Errorf("read service-account credential: %w", err)
	}
	now := s.now().UTC()
	if err := s.lifecycle.Verify(credential.Stored, candidate, now); err != nil {
		return AuthenticatedServiceAccount{}, err
	}
	if s.lifecycle.ShouldRecordLastUsed(credential.Stored.LastUsedAt, now) {
		if err := s.repository.TouchNativeCredential(ctx, credential.ID, now); err != nil {
			return AuthenticatedServiceAccount{}, fmt.Errorf("record service-account credential use: %w", err)
		}
	}
	scope := domain.AccessScope{InstallationID: account.InstallationID}
	if account.ProjectID == "" {
		scope.InstallationWide = true
	} else {
		scope.ProjectIDs = []domain.ProjectID{account.ProjectID}
	}
	permissions := slices.Clone(credential.Stored.Permissions)
	return AuthenticatedServiceAccount{
		Actor: domain.Actor{
			PrincipalID: account.PrincipalID, InstallationID: account.InstallationID,
			AuthenticationMethod:  domain.AuthenticationMethodNativeServiceAccount,
			CredentialID:          credential.ID,
			CredentialPermissions: slices.Clone(permissions),
			CredentialScope:       scope,
		},
		CredentialID: credential.ID,
		Permissions:  permissions,
	}, nil
}

func (s *ServiceAccounts) authorizeAccount(
	ctx context.Context,
	principalID domain.PrincipalID,
	permission domain.Permission,
) (domain.Actor, serviceaccountcredential.ServiceAccount, error) {
	actor, err := ActorFromContext(ctx)
	if err != nil {
		return domain.Actor{}, serviceaccountcredential.ServiceAccount{}, err
	}
	account, err := s.repository.ServiceAccount(ctx, principalID)
	if err != nil {
		return domain.Actor{}, serviceaccountcredential.ServiceAccount{}, err
	}
	if account.InstallationID != actor.InstallationID {
		return domain.Actor{}, serviceaccountcredential.ServiceAccount{},
			ports.ErrServiceAccountNotFound
	}
	actor, err = s.authorize(ctx, account, permission)
	return actor, account, err
}

func (s *ServiceAccounts) authorize(
	ctx context.Context,
	account serviceaccountcredential.ServiceAccount,
	permission domain.Permission,
) (domain.Actor, error) {
	actor, err := ActorFromContext(ctx)
	if err != nil {
		return domain.Actor{}, err
	}
	if actor.InstallationID != account.InstallationID {
		return domain.Actor{}, domain.ErrAccessDenied
	}
	err = s.authorizer.Authorize(ctx, actor, permission, domain.AuthorizationScope{
		InstallationID: account.InstallationID, ProjectID: account.ProjectID,
	})
	return actor, err
}

func (s *ServiceAccounts) issue(
	ctx context.Context,
	actor domain.Actor,
	account serviceaccountcredential.ServiceAccount,
	permissions []domain.Permission,
	expiresAt time.Time,
	now time.Time,
) (serviceaccountcredential.IssuedCredential, error) {
	request, err := s.issueRequest(ctx, actor, account, permissions, expiresAt)
	if err != nil {
		return serviceaccountcredential.IssuedCredential{}, err
	}
	return s.lifecycle.Issue(request, now)
}

func (s *ServiceAccounts) issueRequest(
	ctx context.Context,
	actor domain.Actor,
	account serviceaccountcredential.ServiceAccount,
	permissions []domain.Permission,
	expiresAt time.Time,
) (serviceaccountcredential.IssueRequest, error) {
	scope := domain.AuthorizationScope{
		InstallationID: account.InstallationID, ProjectID: account.ProjectID,
	}
	for _, permission := range permissions {
		if !permission.Valid() {
			return serviceaccountcredential.IssueRequest{}, serviceaccountcredential.ErrInvalidRequest
		}
		if err := s.authorizer.Authorize(ctx, actor, permission, scope); err != nil {
			if errors.Is(err, domain.ErrAccessDenied) {
				return serviceaccountcredential.IssueRequest{}, serviceaccountcredential.ErrDelegationDenied
			}
			return serviceaccountcredential.IssueRequest{}, err
		}
	}
	return serviceaccountcredential.IssueRequest{
		CreatedBy: actor.PrincipalID, RequestedPermissions: permissions,
		CreatorPermissions: permissions, DelegablePermissions: s.delegable,
		ExpiresAt: expiresAt,
	}, nil
}
