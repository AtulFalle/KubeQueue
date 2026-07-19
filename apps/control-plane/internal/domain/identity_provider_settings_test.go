package domain

import (
	"testing"
	"time"
)

func TestManagedIdentityProviderRequiresCurrentSuccessfulTest(t *testing.T) {
	t.Parallel()
	provider := ManagedIdentityProvider{
		ID: "corp", InstallationID: "default",
		Configuration: IdentityProviderConfiguration{
			DisplayName: "Corporate", Issuer: "https://id.example.com",
			Audience: "api", ClientID: "web", RedirectURI: "https://app.example.com/callback",
			AllowedAlgorithms: []string{"RS256"}, GroupsClaim: "groups",
			EmailClaim: "email", NameClaim: "name", CacheTTL: 5 * time.Minute,
		},
		ClientSecretConfigured: true, State: IdentityProviderDisabled,
		TestStatus: IdentityProviderTestPassed, TestedVersion: 2, Version: 2,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := provider.Validate(); err != nil {
		t.Fatal(err)
	}
	if !provider.CanEnable() {
		t.Fatal("current successful test did not permit enablement")
	}
	provider.Version++
	if provider.CanEnable() {
		t.Fatal("stale successful test permitted enablement")
	}
}

func TestIdentityProviderConfigurationRejectsDualSecretSources(t *testing.T) {
	t.Parallel()
	configuration := IdentityProviderConfiguration{
		DisplayName: "Corporate", Issuer: "https://id.example.com", Audience: "api",
		ClientID: "web", ClientSecret: "secret", ClientSecretRef: "vault://secret",
		RedirectURI: "https://app.example.com/callback", AllowedAlgorithms: []string{"RS256"},
		GroupsClaim: "groups", EmailClaim: "email", NameClaim: "name", CacheTTL: time.Minute,
	}
	if configuration.Validate() == nil {
		t.Fatal("dual secret sources were accepted")
	}
}
