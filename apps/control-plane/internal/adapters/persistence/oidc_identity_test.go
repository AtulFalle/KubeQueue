package persistence

import (
	"errors"
	"testing"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
)

func TestOIDCJITProvisioningRequiresExplicitMappingAndRejectsDisabledPrincipal(t *testing.T) {
	store := openAuthorizationStore(t, "oidc-jit")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	execAuthorizationFixture(t, store,
		`INSERT INTO identity_providers
		 (id,installation_id,issuer,audience,authorized_party,allowed_algorithms,
		  groups_claim,email_claim,name_claim,cache_ttl_seconds,enabled,created_at,updated_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,TRUE,?,?)`,
		"workforce", "default", "https://identity.example.com", "kubequeue-api",
		"kubequeue-bff", `["RS256"]`, "groups", "email", "name", 300, now, now)
	execAuthorizationFixture(t, store,
		`INSERT INTO teams(id,installation_id,name,created_at) VALUES(?,?,?,?)`,
		"oidc_operators", "default", "OIDC Operators", now)
	execAuthorizationFixture(t, store,
		`INSERT INTO role_bindings
		 (id,installation_id,role_definition_id,scope_type,team_id,created_at)
		 VALUES(?,?,?,?,?,?)`,
		"oidc_operator_binding", "default", "installation_owner", "INSTALLATION",
		"oidc_operators", now)

	providers, err := store.ActiveOIDCProviders(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 1 || providers[0].Issuer != "https://identity.example.com" {
		t.Fatalf("providers = %#v", providers)
	}
	claims := domain.OIDCIdentityClaims{
		Issuer: "https://identity.example.com", Subject: "subject-1",
		Email: "user@example.com", DisplayName: "Example User", Groups: []string{"operators"},
	}
	if _, err := store.ResolveOIDCPrincipal(t.Context(), providers[0], claims); !errors.Is(
		err, domain.ErrJITProvisioningDenied,
	) {
		t.Fatalf("ResolveOIDCPrincipal() error = %v, want provisioning denied", err)
	}

	execAuthorizationFixture(t, store,
		`INSERT INTO oidc_jit_provisioning_mappings
		 (id,identity_provider_id,mapping_type,claim_value,team_id,created_at)
		 VALUES(?,?,?,?,?,?)`,
		"operators_mapping", "workforce", domain.OIDCMappingGroup, "operators",
		"oidc_operators", now)
	actor, err := store.ResolveOIDCPrincipal(t.Context(), providers[0], claims)
	if err != nil {
		t.Fatal(err)
	}
	if actor.InstallationID != "default" || actor.PrincipalID == "" {
		t.Fatalf("actor = %#v", actor)
	}
	var memberships int
	if err := store.db.QueryRowContext(t.Context(), store.bind(
		`SELECT COUNT(*) FROM team_memberships WHERE team_id=? AND principal_id=?`,
	), "oidc_operators", actor.PrincipalID).Scan(&memberships); err != nil {
		t.Fatal(err)
	}
	if memberships != 1 {
		t.Fatalf("team memberships = %d, want 1", memberships)
	}

	execAuthorizationFixture(t, store,
		`UPDATE principals SET disabled_at=? WHERE id=?`, now, actor.PrincipalID)
	if _, err := store.ResolveOIDCPrincipal(t.Context(), providers[0], claims); !errors.Is(
		err, domain.ErrIdentityDisabled,
	) {
		t.Fatalf("disabled ResolveOIDCPrincipal() error = %v", err)
	}
}

func TestOIDCJITProvisioningAcceptsExplicitEmailDomainMapping(t *testing.T) {
	store := openAuthorizationStore(t, "oidc-domain-jit")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	execAuthorizationFixture(t, store,
		`INSERT INTO identity_providers
		 (id,installation_id,issuer,audience,allowed_algorithms,
		  groups_claim,email_claim,name_claim,cache_ttl_seconds,enabled,created_at,updated_at)
		 VALUES(?,?,?,?,?,?,?,?,?,TRUE,?,?)`,
		"partners", "default", "https://partners.example.com", "kubequeue-api",
		`["RS256"]`, "groups", "email", "name", 300, now, now)
	execAuthorizationFixture(t, store,
		`INSERT INTO teams(id,installation_id,name,created_at) VALUES(?,?,?,?)`,
		"partners_team", "default", "Partners", now)
	execAuthorizationFixture(t, store,
		`INSERT INTO role_bindings
		 (id,installation_id,role_definition_id,scope_type,team_id,created_at)
		 VALUES(?,?,?,?,?,?)`,
		"partners_binding", "default", "installation_owner", "INSTALLATION", "partners_team", now)
	execAuthorizationFixture(t, store,
		`INSERT INTO oidc_jit_provisioning_mappings
		 (id,identity_provider_id,mapping_type,claim_value,team_id,created_at)
		 VALUES(?,?,?,?,?,?)`,
		"partners_domain", "partners", domain.OIDCMappingDomain, "example.org", "partners_team", now)
	providers, err := store.ActiveOIDCProviders(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	actor, err := store.ResolveOIDCPrincipal(t.Context(), providers[0], domain.OIDCIdentityClaims{
		Issuer: "https://partners.example.com", Subject: "partner-1",
		Email: "user@EXAMPLE.ORG", EmailVerified: false,
	})
	if !errors.Is(err, domain.ErrJITProvisioningDenied) {
		t.Fatalf("unverified domain ResolveOIDCPrincipal() error = %v", err)
	}
	actor, err = store.ResolveOIDCPrincipal(t.Context(), providers[0], domain.OIDCIdentityClaims{
		Issuer: "https://partners.example.com", Subject: "partner-1",
		Email: "user@EXAMPLE.ORG", EmailVerified: true,
	})
	if err != nil || actor.PrincipalID == "" {
		t.Fatalf("ResolveOIDCPrincipal() actor = %#v, error = %v", actor, err)
	}
}

func TestOIDCLoginSynchronizesProviderMembershipsAndPreservesManualMemberships(t *testing.T) {
	store := openAuthorizationStore(t, "oidc-group-sync")
	provider, now := insertOIDCTestProvider(t, store, "workforce", "https://identity.example.com")
	for _, team := range []string{"operators", "viewers", "manual"} {
		execAuthorizationFixture(t, store,
			`INSERT INTO teams(id,installation_id,name,created_at) VALUES(?,?,?,?)`,
			team, "default", team, now)
	}
	for _, team := range []string{"operators", "viewers"} {
		execAuthorizationFixture(t, store,
			`INSERT INTO role_bindings
			 (id,installation_id,role_definition_id,scope_type,team_id,created_at)
			 VALUES(?,?,?,?,?,?)`,
			team+"_binding", "default", "installation_owner", "INSTALLATION", team, now)
		execAuthorizationFixture(t, store,
			`INSERT INTO oidc_jit_provisioning_mappings
			 (id,identity_provider_id,mapping_type,claim_value,team_id,created_at)
			 VALUES(?,?,?,?,?,?)`,
			team+"_mapping", provider.ID, domain.OIDCMappingGroup, team, team, now)
	}
	claims := domain.OIDCIdentityClaims{
		Issuer: provider.Issuer, Subject: "sync-user", Groups: []string{"operators"},
	}
	actor, err := store.ResolveOIDCPrincipal(t.Context(), provider, claims)
	if err != nil {
		t.Fatal(err)
	}
	execAuthorizationFixture(t, store,
		`INSERT INTO team_memberships(team_id,principal_id,created_at)
		 VALUES(?,?,?)`, "manual", actor.PrincipalID, now)

	claims.Groups = []string{"viewers"}
	if _, err := store.ResolveOIDCPrincipal(t.Context(), provider, claims); err != nil {
		t.Fatal(err)
	}
	assertOIDCMembership(t, store, actor.PrincipalID, "operators", provider.ID, 0)
	assertOIDCMembership(t, store, actor.PrincipalID, "viewers", provider.ID, 1)
	assertOIDCMembership(t, store, actor.PrincipalID, "manual", "", 1)

	claims.Groups = nil
	if _, err := store.ResolveOIDCPrincipal(t.Context(), provider, claims); !errors.Is(
		err, domain.ErrJITProvisioningDenied,
	) {
		t.Fatalf("mapping-free existing identity error = %v, want provisioning denied", err)
	}
	assertOIDCMembership(t, store, actor.PrincipalID, "viewers", provider.ID, 0)
	assertOIDCMembership(t, store, actor.PrincipalID, "manual", "", 1)
}

func TestOIDCExistingIdentityWithDirectGrantDoesNotRequireCurrentMapping(t *testing.T) {
	store := openAuthorizationStore(t, "oidc-direct-grant")
	provider, now := insertOIDCTestProvider(t, store, "workforce", "https://identity.example.com")
	execAuthorizationFixture(t, store,
		`INSERT INTO teams(id,installation_id,name,created_at) VALUES(?,?,?,?)`,
		"initial_team", "default", "Initial Team", now)
	execAuthorizationFixture(t, store,
		`INSERT INTO role_bindings
		 (id,installation_id,role_definition_id,scope_type,team_id,created_at)
		 VALUES(?,?,?,?,?,?)`,
		"initial_team_binding", "default", "installation_owner", "INSTALLATION",
		"initial_team", now)
	execAuthorizationFixture(t, store,
		`INSERT INTO oidc_jit_provisioning_mappings
		 (id,identity_provider_id,mapping_type,claim_value,team_id,created_at)
		 VALUES(?,?,?,?,?,?)`,
		"initial_mapping", provider.ID, domain.OIDCMappingGroup, "initial", "initial_team", now)
	claims := domain.OIDCIdentityClaims{
		Issuer: provider.Issuer, Subject: "direct-user", Groups: []string{"initial"},
	}
	actor, err := store.ResolveOIDCPrincipal(t.Context(), provider, claims)
	if err != nil {
		t.Fatal(err)
	}
	execAuthorizationFixture(t, store,
		`INSERT INTO role_bindings
		 (id,installation_id,role_definition_id,scope_type,principal_id,created_at)
		 VALUES(?,?,?,?,?,?)`,
		"direct_owner", "default", "installation_owner", "INSTALLATION", actor.PrincipalID, now)

	claims.Groups = nil
	if _, err := store.ResolveOIDCPrincipal(t.Context(), provider, claims); err != nil {
		t.Fatalf("direct-grant ResolveOIDCPrincipal() error = %v", err)
	}
	assertOIDCMembership(t, store, actor.PrincipalID, "initial_team", provider.ID, 0)
}

func insertOIDCTestProvider(
	t *testing.T,
	store *Store,
	id string,
	issuer string,
) (domain.OIDCProvider, string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	execAuthorizationFixture(t, store,
		`INSERT INTO identity_providers
		 (id,installation_id,issuer,audience,allowed_algorithms,
		  groups_claim,email_claim,name_claim,cache_ttl_seconds,enabled,created_at,updated_at)
		 VALUES(?,?,?,?,?,?,?,?,?,TRUE,?,?)`,
		id, "default", issuer, "kubequeue-api", `["RS256"]`,
		"groups", "email", "name", 300, now, now)
	providers, err := store.ActiveOIDCProviders(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range providers {
		if provider.ID == id {
			return provider, now
		}
	}
	t.Fatalf("OIDC provider %q was not loaded", id)
	return domain.OIDCProvider{}, ""
}

func assertOIDCMembership(
	t *testing.T,
	store *Store,
	principalID domain.PrincipalID,
	teamID string,
	sourceProviderID string,
	want int,
) {
	t.Helper()
	var count int
	if err := store.db.QueryRowContext(t.Context(), store.bind(
		`SELECT COUNT(*) FROM team_memberships
		 WHERE team_id=? AND principal_id=?
		 AND COALESCE(source_identity_provider_id,'')=?`,
	), teamID, principalID, sourceProviderID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("membership %q from %q count = %d, want %d",
			teamID, sourceProviderID, count, want)
	}
}
