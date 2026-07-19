package application

import (
	"context"
	"testing"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
)

type identityProviderRepositoryStub struct {
	provider domain.ManagedIdentityProvider
}

func (s *identityProviderRepositoryStub) ListIdentityProviders(
	context.Context, domain.InstallationID,
) ([]domain.ManagedIdentityProvider, error) {
	if s.provider.ID == "" {
		return nil, nil
	}
	return []domain.ManagedIdentityProvider{s.provider}, nil
}
func (s *identityProviderRepositoryStub) IdentityProvider(
	_ context.Context, _ domain.InstallationID, _ string,
) (domain.ManagedIdentityProvider, error) {
	return s.provider, nil
}
func (s *identityProviderRepositoryStub) CreateIdentityProvider(
	_ context.Context, provider domain.ManagedIdentityProvider,
) (domain.ManagedIdentityProvider, error) {
	s.provider = provider
	return provider, nil
}
func (s *identityProviderRepositoryStub) UpdateIdentityProvider(
	_ context.Context, provider domain.ManagedIdentityProvider, _ uint64,
) (domain.ManagedIdentityProvider, error) {
	s.provider = provider
	return provider, nil
}
func (s *identityProviderRepositoryStub) RecordIdentityProviderTest(
	_ context.Context, _ domain.InstallationID, _ string, expected uint64,
	passed bool, message string, at time.Time,
) (domain.ManagedIdentityProvider, error) {
	s.provider.Version = expected + 1
	s.provider.TestedVersion = s.provider.Version
	s.provider.TestedAt, s.provider.TestMessage = &at, message
	s.provider.TestStatus = domain.IdentityProviderTestFailed
	if passed {
		s.provider.TestStatus = domain.IdentityProviderTestPassed
	}
	return s.provider, nil
}
func (s *identityProviderRepositoryStub) SetIdentityProviderEnabled(
	_ context.Context, _ domain.InstallationID, _ string, expected uint64,
	enabled bool, at time.Time,
) (domain.ManagedIdentityProvider, error) {
	s.provider.Version, s.provider.UpdatedAt = expected+1, at
	s.provider.TestedVersion = s.provider.Version
	s.provider.State = domain.IdentityProviderDisabled
	if enabled {
		s.provider.State = domain.IdentityProviderEnabled
	}
	return s.provider, nil
}
func (*identityProviderRepositoryStub) IsInstallationOwner(
	context.Context, domain.Actor,
) (bool, error) {
	return true, nil
}

type oidcPreflightStub struct{ err error }

func (s oidcPreflightStub) Preflight(context.Context, domain.OIDCProvider) error {
	return s.err
}

func TestIdentityProvidersEncryptsSecretsAndBindsTestsToVersion(t *testing.T) {
	t.Parallel()
	repository := &identityProviderRepositoryStub{}
	service, err := NewIdentityProviders(repository, oidcPreflightStub{}, make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithActor(t.Context(), domain.Actor{
		PrincipalID: "owner", InstallationID: "default", AuthenticationMethod: "OIDC",
	})
	created, err := service.Create(ctx, "corp", validIdentityProviderConfiguration("top-secret"))
	if err != nil {
		t.Fatal(err)
	}
	if created.Configuration.ClientSecret != "" ||
		created.ClientSecretCiphertext == "" ||
		created.ClientSecretCiphertext == "top-secret" {
		t.Fatalf("client secret was not write-only and encrypted: %#v", created)
	}
	tested, err := service.Test(ctx, "corp", created.Version)
	if err != nil {
		t.Fatal(err)
	}
	if !tested.CanEnable() {
		t.Fatal("successful test was not bound to resulting version")
	}
	enabled, err := service.Enable(ctx, "corp", tested.Version)
	if err != nil {
		t.Fatal(err)
	}
	if enabled.State != domain.IdentityProviderEnabled {
		t.Fatalf("state = %s, want enabled", enabled.State)
	}
}

func validIdentityProviderConfiguration(secret string) domain.IdentityProviderConfiguration {
	return domain.IdentityProviderConfiguration{
		DisplayName: "Corporate", Issuer: "https://id.example.com", Audience: "api",
		ClientID: "web", ClientSecret: secret, RedirectURI: "https://app.example.com/callback",
		AllowedAlgorithms: []string{"RS256"}, GroupsClaim: "groups", EmailClaim: "email",
		NameClaim: "name", CacheTTL: 5 * time.Minute,
	}
}
