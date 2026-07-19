package application

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain/serviceaccountcredential"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/ports"
)

type serviceAccountAuthorizer struct {
	allowed map[domain.Permission]bool
}

func (a serviceAccountAuthorizer) Authorize(
	_ context.Context,
	_ domain.Actor,
	permission domain.Permission,
	_ domain.AuthorizationScope,
) error {
	if !a.allowed[permission] {
		return domain.ErrAccessDenied
	}
	return nil
}

func (serviceAccountAuthorizer) AccessibleScope(
	context.Context, domain.Actor, domain.Permission,
) (domain.AccessScope, error) {
	return domain.AccessScope{}, nil
}

type serviceAccountRepositoryStub struct {
	accounts    map[domain.PrincipalID]serviceaccountcredential.ServiceAccount
	credentials map[string]serviceaccountcredential.Credential
	touches     int
}

func (*serviceAccountRepositoryStub) Project(
	_ context.Context,
	installationID domain.InstallationID,
	id domain.ProjectID,
) (domain.ManagedProject, error) {
	return domain.ManagedProject{ID: id, InstallationID: installationID}, nil
}

func (r *serviceAccountRepositoryStub) CreateServiceAccount(
	_ context.Context,
	account serviceaccountcredential.ServiceAccount,
) (serviceaccountcredential.ServiceAccount, error) {
	if existing, ok := r.accounts[account.PrincipalID]; ok {
		return existing, nil
	}
	r.accounts[account.PrincipalID] = account
	return account, nil
}

func (r *serviceAccountRepositoryStub) ServiceAccount(
	_ context.Context,
	id domain.PrincipalID,
) (serviceaccountcredential.ServiceAccount, error) {
	account, ok := r.accounts[id]
	if !ok {
		return serviceaccountcredential.ServiceAccount{}, ports.ErrServiceAccountNotFound
	}
	return account, nil
}

func (r *serviceAccountRepositoryStub) BindServiceAccountOIDCIdentity(
	_ context.Context,
	_ domain.InstallationID,
	id domain.PrincipalID,
	identity serviceaccountcredential.OIDCIdentity,
	_ domain.PrincipalID,
	_ time.Time,
) (serviceaccountcredential.ServiceAccount, error) {
	account, ok := r.accounts[id]
	if !ok {
		return serviceaccountcredential.ServiceAccount{}, ports.ErrServiceAccountNotFound
	}
	account.OIDCIdentity = &identity
	r.accounts[id] = account
	return account, nil
}

func (r *serviceAccountRepositoryStub) RemoveServiceAccountOIDCIdentity(
	_ context.Context,
	_ domain.InstallationID,
	id domain.PrincipalID,
) error {
	account, ok := r.accounts[id]
	if !ok {
		return ports.ErrServiceAccountNotFound
	}
	account.OIDCIdentity = nil
	r.accounts[id] = account
	return nil
}

func (r *serviceAccountRepositoryStub) ListServiceAccounts(
	_ context.Context,
	installationID domain.InstallationID,
	page domain.AccessPage,
) ([]serviceaccountcredential.ServiceAccount, error) {
	var ids []string
	for id, account := range r.accounts {
		if account.InstallationID == installationID && string(id) > page.After {
			ids = append(ids, string(id))
		}
	}
	sort.Strings(ids)
	if len(ids) > page.Limit {
		ids = ids[:page.Limit]
	}
	result := make([]serviceaccountcredential.ServiceAccount, 0, len(ids))
	for _, id := range ids {
		result = append(result, r.accounts[domain.PrincipalID(id)])
	}
	return result, nil
}

func (r *serviceAccountRepositoryStub) ListNativeCredentialMetadata(
	_ context.Context,
	installationID domain.InstallationID,
	principalID domain.PrincipalID,
	page domain.AccessPage,
) ([]serviceaccountcredential.CredentialMetadata, error) {
	var ids []string
	for id, credential := range r.credentials {
		account := r.accounts[credential.ServiceAccountPrincipalID]
		if account.InstallationID == installationID &&
			credential.ServiceAccountPrincipalID == principalID && id > page.After {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	if len(ids) > page.Limit {
		ids = ids[:page.Limit]
	}
	result := make([]serviceaccountcredential.CredentialMetadata, 0, len(ids))
	for _, id := range ids {
		result = append(result, r.credentials[id].Metadata())
	}
	return result, nil
}

func (r *serviceAccountRepositoryStub) CreateNativeCredential(
	_ context.Context,
	credential serviceaccountcredential.Credential,
) error {
	r.credentials[credential.ID] = credential
	return nil
}

func (r *serviceAccountRepositoryStub) NativeCredentialByID(
	_ context.Context,
	id string,
) (serviceaccountcredential.Credential, serviceaccountcredential.ServiceAccount, error) {
	credential, ok := r.credentials[id]
	if !ok {
		return serviceaccountcredential.Credential{},
			serviceaccountcredential.ServiceAccount{}, ports.ErrCredentialNotFound
	}
	account := r.accounts[credential.ServiceAccountPrincipalID]
	return credential, account, nil
}

func (r *serviceAccountRepositoryStub) NativeCredentialByPrefix(
	_ context.Context,
	prefix string,
) (serviceaccountcredential.Credential, serviceaccountcredential.ServiceAccount, error) {
	for _, credential := range r.credentials {
		if credential.Stored.Prefix == prefix {
			account := r.accounts[credential.ServiceAccountPrincipalID]
			return credential, account, nil
		}
	}
	return serviceaccountcredential.Credential{},
		serviceaccountcredential.ServiceAccount{}, ports.ErrCredentialNotFound
}

func (r *serviceAccountRepositoryStub) RotateNativeCredential(
	_ context.Context,
	previous serviceaccountcredential.Credential,
	replacement serviceaccountcredential.Credential,
) error {
	r.credentials[previous.ID] = previous
	r.credentials[replacement.ID] = replacement
	return nil
}

func (r *serviceAccountRepositoryStub) RevokeNativeCredential(
	_ context.Context,
	id string,
	at time.Time,
) error {
	credential, ok := r.credentials[id]
	if !ok {
		return ports.ErrCredentialNotFound
	}
	credential.Stored = serviceaccountcredential.Revoke(credential.Stored, at)
	r.credentials[id] = credential
	return nil
}

func (r *serviceAccountRepositoryStub) TouchNativeCredential(
	_ context.Context,
	id string,
	at time.Time,
) error {
	credential, ok := r.credentials[id]
	if !ok {
		return ports.ErrCredentialNotFound
	}
	credential.Stored.LastUsedAt = at
	r.credentials[id] = credential
	r.touches++
	return nil
}

func TestServiceAccountsCreationDelegationAndAuthentication(t *testing.T) {
	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	service, repository := newServiceAccountsForTest(t, &now)
	ctx := WithActor(t.Context(), domain.Actor{
		PrincipalID: "creator", InstallationID: "default",
	})
	account, err := service.Create(ctx, CreateServiceAccountInput{
		PrincipalID: "build_bot", ProjectID: "platform", DisplayName: "Build Bot",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if account.CreatedBy != "creator" || account.ProjectID != "platform" {
		t.Fatalf("created service account = %#v", account)
	}

	if _, err := service.Issue(ctx, IssueServiceAccountCredentialInput{
		ServiceAccountPrincipalID: account.PrincipalID,
		Permissions:               []domain.Permission{domain.PermissionJobsSubmit},
		ExpiresAt:                 now.Add(time.Hour),
	}); !errors.Is(err, serviceaccountcredential.ErrDelegationDenied) {
		t.Fatalf("Issue() non-delegable error = %v", err)
	}
	if _, err := service.Issue(ctx, IssueServiceAccountCredentialInput{
		ServiceAccountPrincipalID: account.PrincipalID,
		Permissions:               []domain.Permission{domain.PermissionQueueRead},
		ExpiresAt:                 now.Add(time.Hour),
	}); !errors.Is(err, serviceaccountcredential.ErrDelegationDenied) {
		t.Fatalf("Issue() creator-permission ceiling error = %v", err)
	}

	issued, err := service.Issue(ctx, IssueServiceAccountCredentialInput{
		ServiceAccountPrincipalID: account.PrincipalID,
		Permissions:               []domain.Permission{domain.PermissionJobsRead},
		ExpiresAt:                 now.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	plaintext, err := issued.Plaintext.Reveal()
	if err != nil {
		t.Fatalf("Reveal() error = %v", err)
	}
	authenticated, err := service.Authenticate(t.Context(), plaintext)
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if authenticated.Actor.PrincipalID != account.PrincipalID ||
		authenticated.CredentialID != issued.Credential.ID ||
		len(authenticated.Permissions) != 1 ||
		authenticated.Permissions[0] != domain.PermissionJobsRead ||
		authenticated.Actor.CredentialID != issued.Credential.ID ||
		len(authenticated.Actor.CredentialPermissions) != 1 ||
		authenticated.Actor.CredentialPermissions[0] != domain.PermissionJobsRead ||
		authenticated.Actor.CredentialScope.InstallationID != "default" ||
		len(authenticated.Actor.CredentialScope.ProjectIDs) != 1 ||
		authenticated.Actor.CredentialScope.ProjectIDs[0] != "platform" {
		t.Fatalf("authenticated service account = %#v", authenticated)
	}
	if repository.touches != 1 {
		t.Fatalf("last-used touches = %d, want 1", repository.touches)
	}
	if _, err := service.Authenticate(t.Context(), plaintext); err != nil {
		t.Fatalf("second Authenticate() error = %v", err)
	}
	if repository.touches != 1 {
		t.Fatalf("throttled last-used touches = %d, want 1", repository.touches)
	}
	if err := service.Revoke(ctx, issued.Credential.ID); err != nil {
		t.Fatalf("Revoke() error = %v", err)
	}
	if _, err := service.Authenticate(
		t.Context(), plaintext,
	); !errors.Is(err, serviceaccountcredential.ErrCredentialRevoked) {
		t.Fatalf("Authenticate() revoked error = %v", err)
	}
}

func TestServiceAccountRotationUsesBoundedOverlap(t *testing.T) {
	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	service, repository := newServiceAccountsForTest(t, &now)
	account := serviceaccountcredential.ServiceAccount{
		PrincipalID: "build_bot", InstallationID: "default", ProjectID: "platform",
		DisplayName: "Build Bot", CreatedBy: "creator", CreatedAt: now,
	}
	repository.accounts[account.PrincipalID] = account
	ctx := WithActor(t.Context(), domain.Actor{
		PrincipalID: "creator", InstallationID: "default",
	})
	issued, err := service.Issue(ctx, IssueServiceAccountCredentialInput{
		ServiceAccountPrincipalID: account.PrincipalID,
		Permissions:               []domain.Permission{domain.PermissionJobsRead},
		ExpiresAt:                 now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	previousPlaintext, err := issued.Plaintext.Reveal()
	if err != nil {
		t.Fatal(err)
	}
	rotation, err := service.Rotate(ctx, RotateServiceAccountCredentialInput{
		CredentialID: issued.Credential.ID,
		Permissions:  []domain.Permission{domain.PermissionJobsRead},
		ExpiresAt:    now.Add(2 * time.Hour),
		Overlap:      2 * time.Minute,
	})
	if err != nil {
		t.Fatalf("Rotate() error = %v", err)
	}
	replacementPlaintext, err := rotation.Replacement.Plaintext.Reveal()
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2*time.Minute - time.Nanosecond)
	if _, err := service.Authenticate(t.Context(), previousPlaintext); err != nil {
		t.Fatalf("previous credential rejected during overlap: %v", err)
	}
	now = now.Add(time.Nanosecond)
	if _, err := service.Authenticate(
		t.Context(), previousPlaintext,
	); !errors.Is(err, serviceaccountcredential.ErrCredentialRotated) {
		t.Fatalf("previous credential overlap-boundary error = %v", err)
	}
	if _, err := service.Authenticate(t.Context(), replacementPlaintext); err != nil {
		t.Fatalf("replacement credential rejected: %v", err)
	}
}

func TestServiceAccountsExposeBoundedMetadataLists(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	service, repository := newServiceAccountsForTest(t, &now)
	for _, id := range []domain.PrincipalID{"zeta_bot", "alpha_bot"} {
		repository.accounts[id] = serviceaccountcredential.ServiceAccount{
			PrincipalID: id, InstallationID: "default", DisplayName: string(id),
			CreatedBy: "creator", CreatedAt: now,
		}
	}
	repository.credentials["credential-one"] = serviceaccountcredential.Credential{
		ID: "credential-one", ServiceAccountPrincipalID: "alpha_bot",
		Stored: serviceaccountcredential.StoredCredential{
			Prefix: "kqsa_alpha", Permissions: []domain.Permission{domain.PermissionJobsRead},
			CreatedBy: "creator", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		},
	}
	ctx := WithActor(t.Context(), domain.Actor{
		PrincipalID: "creator", InstallationID: "default",
	})
	accounts, err := service.List(ctx, domain.AccessPage{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := service.ListCredentials(ctx, "alpha_bot", domain.AccessPage{})
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || accounts[0].PrincipalID != "alpha_bot" ||
		len(credentials) != 1 || credentials[0].Prefix != "kqsa_alpha" {
		t.Fatalf("metadata lists = accounts %#v credentials %#v", accounts, credentials)
	}
}

func newServiceAccountsForTest(
	t *testing.T,
	now *time.Time,
) (*ServiceAccounts, *serviceAccountRepositoryStub) {
	t.Helper()
	repository := &serviceAccountRepositoryStub{
		accounts:    make(map[domain.PrincipalID]serviceaccountcredential.ServiceAccount),
		credentials: make(map[string]serviceaccountcredential.Credential),
	}
	entropy := make([]byte, 0, 44*4)
	for value := byte(1); value <= 4; value++ {
		entropy = append(entropy, bytes.Repeat([]byte{value}, 44)...)
	}
	lifecycle, err := serviceaccountcredential.NewLifecycleWithRandom(
		bytes.Repeat([]byte{0x42}, serviceaccountcredential.MinimumDigestKeyBytes),
		bytes.NewReader(entropy),
		serviceaccountcredential.DefaultPolicy(),
	)
	if err != nil {
		t.Fatal(err)
	}
	authorizer := serviceAccountAuthorizer{allowed: map[domain.Permission]bool{
		domain.PermissionServiceAccountsManage: true,
		domain.PermissionTokensManage:          true,
		domain.PermissionJobsRead:              true,
		domain.PermissionJobsSubmit:            true,
	}}
	service, err := NewServiceAccounts(
		repository, authorizer, lifecycle,
		[]domain.Permission{domain.PermissionJobsRead, domain.PermissionQueueRead},
	)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return *now }
	nextID := 0
	service.newID = func() string {
		nextID++
		if nextID == 1 {
			return "credential-one"
		}
		return "credential-two"
	}
	return service, repository
}
