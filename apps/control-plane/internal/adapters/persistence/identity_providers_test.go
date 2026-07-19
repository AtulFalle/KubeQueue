package persistence

import (
	"errors"
	"testing"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
)

func TestIdentityProviderPersistenceUsesOptimisticVersionedTests(t *testing.T) {
	t.Parallel()
	store, err := Open(t.Context(), sqliteTestURL(t, "identity-provider-settings.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	if _, err := store.db.ExecContext(t.Context(),
		`INSERT INTO installations(id,name,created_at) VALUES('default','Default',?)`,
		now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	provider := domain.ManagedIdentityProvider{
		ID: "corp", InstallationID: "default",
		Configuration: domain.IdentityProviderConfiguration{
			DisplayName: "Corporate", Issuer: "https://id.example.com", Audience: "api",
			ClientID: "web", RedirectURI: "https://app.example.com/callback",
			AllowedAlgorithms: []string{"RS256"}, GroupsClaim: "groups",
			EmailClaim: "email", NameClaim: "name", CacheTTL: 5 * time.Minute,
		},
		ClientSecretCiphertext: "encrypted-value", ClientSecretConfigured: true,
		State: domain.IdentityProviderDisabled, TestStatus: domain.IdentityProviderNotTested,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	created, err := store.CreateIdentityProvider(t.Context(), provider)
	if err != nil {
		t.Fatal(err)
	}
	if created.ClientSecretCiphertext != "encrypted-value" || !created.ClientSecretConfigured {
		t.Fatalf("stored secret metadata = %#v", created)
	}
	tested, err := store.RecordIdentityProviderTest(
		t.Context(), "default", "corp", 1, true, "available", now.Add(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if tested.Version != 2 || tested.TestedVersion != 2 || !tested.CanEnable() {
		t.Fatalf("version-bound test = %#v", tested)
	}
	_, err = store.RecordIdentityProviderTest(
		t.Context(), "default", "corp", 1, true, "stale", now,
	)
	if !errors.Is(err, domain.ErrIdentityProviderConflict) {
		t.Fatalf("stale test error = %v", err)
	}
}
