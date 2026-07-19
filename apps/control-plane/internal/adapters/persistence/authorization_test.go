package persistence

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
)

func TestAuthorizerResolvesDirectAndTeamProjectBindings(t *testing.T) {
	t.Parallel()
	store := openAuthorizationStore(t, "auth-bindings")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	execAuthorizationFixture(t, store,
		`INSERT INTO projects(id,installation_id,name,created_at) VALUES(?,?,?,?)`,
		"other", "default", "Other", now)
	execAuthorizationFixture(t, store,
		`INSERT INTO principals(id,installation_id,kind,display_name,created_at) VALUES(?,?,?,?,?)`,
		"user_one", "default", "HUMAN", "User One", now)
	execAuthorizationFixture(t, store,
		`INSERT INTO teams(id,installation_id,name,created_at) VALUES(?,?,?,?)`,
		"team_one", "default", "Team One", now)
	execAuthorizationFixture(t, store,
		`INSERT INTO team_memberships(team_id,principal_id,created_at) VALUES(?,?,?)`,
		"team_one", "user_one", now)
	execAuthorizationFixture(t, store,
		`INSERT INTO role_definitions
		 (id,installation_id,name,scope_type,permissions,built_in,created_at)
		 VALUES(?,?,?,?,?,FALSE,?)`,
		"reader_test", "default", "Reader Test", "PROJECT", `["jobs.read"]`, now)
	execAuthorizationFixture(t, store,
		`INSERT INTO role_bindings
		 (id,installation_id,role_definition_id,scope_type,project_id,principal_id,created_at)
		 VALUES(?,?,?,?,?,?,?)`,
		"direct_binding", "default", "reader_test", "PROJECT", "default", "user_one", now)
	execAuthorizationFixture(t, store,
		`INSERT INTO role_bindings
		 (id,installation_id,role_definition_id,scope_type,project_id,team_id,created_at)
		 VALUES(?,?,?,?,?,?,?)`,
		"team_binding", "default", "reader_test", "PROJECT", "other", "team_one", now)

	actor := domain.Actor{PrincipalID: "user_one", InstallationID: "default"}
	access, err := store.AccessibleScope(t.Context(), actor, domain.PermissionJobsRead)
	if err != nil {
		t.Fatal(err)
	}
	if access.InstallationWide || len(access.ProjectIDs) != 2 {
		t.Fatalf("access = %#v, want two project grants", access)
	}
	if err := store.Authorize(t.Context(), actor, domain.PermissionJobsRead,
		domain.AuthorizationScope{InstallationID: "default", ProjectID: "other"}); err != nil {
		t.Fatalf("team-scoped authorization failed: %v", err)
	}
	if err := store.Authorize(t.Context(), actor, domain.PermissionJobsPause,
		domain.AuthorizationScope{InstallationID: "default", ProjectID: "default"}); !errors.Is(err, domain.ErrAccessDenied) {
		t.Fatalf("ungranted permission error = %v, want access denied", err)
	}
}

func TestAuthorizerDeniesDisabledPrincipal(t *testing.T) {
	t.Parallel()
	store := openAuthorizationStore(t, "auth-disabled")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	execAuthorizationFixture(t, store,
		`UPDATE principals SET disabled_at=? WHERE id='legacy_admin'`, now)
	actor := domain.Actor{PrincipalID: "legacy_admin", InstallationID: "default"}
	_, err := store.AccessibleScope(t.Context(), actor, domain.PermissionJobsList)
	if !errors.Is(err, domain.ErrAccessDenied) {
		t.Fatalf("disabled principal error = %v, want access denied", err)
	}
}

func TestAuthorizerUsesOnlyNativeCredentialBounds(t *testing.T) {
	t.Parallel()
	store := openAuthorizationStore(t, "auth-native-credential")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	execAuthorizationFixture(t, store,
		`INSERT INTO projects(id,installation_id,name,created_at) VALUES(?,?,?,?)`,
		"other", "default", "Other", now)
	execAuthorizationFixture(t, store,
		`INSERT INTO principals(id,installation_id,kind,display_name,created_at)
		 VALUES(?,?,?,?,?)`,
		"build_bot", "default", "SERVICE_ACCOUNT", "Build Bot", now)
	actor := domain.Actor{
		PrincipalID: "build_bot", InstallationID: "default",
		AuthenticationMethod: domain.AuthenticationMethodNativeServiceAccount,
		CredentialID:         "credential-one",
		CredentialPermissions: []domain.Permission{
			domain.PermissionJobsRead,
		},
		CredentialScope: domain.AccessScope{
			InstallationID: "default", ProjectIDs: []domain.ProjectID{"default"},
		},
	}
	if err := store.Authorize(
		t.Context(), actor, domain.PermissionJobsRead,
		domain.AuthorizationScope{InstallationID: "default", ProjectID: "default"},
	); err != nil {
		t.Fatalf("bounded native credential was denied: %v", err)
	}
	for _, denied := range []struct {
		permission domain.Permission
		projectID  domain.ProjectID
	}{
		{permission: domain.PermissionJobsPause, projectID: "default"},
		{permission: domain.PermissionJobsRead, projectID: "other"},
	} {
		if err := store.Authorize(
			t.Context(), actor, denied.permission,
			domain.AuthorizationScope{
				InstallationID: "default", ProjectID: denied.projectID,
			},
		); !errors.Is(err, domain.ErrAccessDenied) {
			t.Fatalf("native credential excess authority error = %v", err)
		}
	}
}

func TestFacetsInProjectsAggregatesOnlyAuthorizedScope(t *testing.T) {
	t.Parallel()
	store := openAuthorizationStore(t, "scoped-facets")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	execAuthorizationFixture(t, store,
		`INSERT INTO projects(id,installation_id,name,created_at) VALUES(?,?,?,?)`,
		"other", "default", "Other", now)
	execAuthorizationFixture(t, store,
		`INSERT INTO namespace_bindings
		 (id,installation_id,project_id,namespace,created_at) VALUES(?,?,?,?,?)`,
		"other__other-ns", "default", "other", "other-ns", now)
	createScopedFacetJob(t, store, "default-job", "default", "default__default", "default", "alpha")
	createScopedFacetJob(t, store, "other-job", "other", "other__other-ns", "other-ns", "secret")

	scoped, err := store.FacetsInProjects(t.Context(), []domain.ProjectID{"default"})
	if err != nil {
		t.Fatal(err)
	}
	if scoped.Total != 1 ||
		scoped.ObservedStateCounts[string(domain.StateCreated)] != 1 ||
		len(scoped.Namespaces) != 1 || scoped.Namespaces[0] != "default" ||
		len(scoped.Teams) != 1 || scoped.Teams[0] != "alpha" {
		t.Fatalf("default project facets leaked scope: %#v", scoped)
	}

	denied, err := store.FacetsInProjects(t.Context(), []domain.ProjectID{})
	if err != nil {
		t.Fatal(err)
	}
	if denied.Total != 0 || len(denied.ObservedStateCounts) != 0 ||
		len(denied.Namespaces) != 0 || len(denied.Teams) != 0 {
		t.Fatalf("empty project scope facets = %#v, want no results", denied)
	}

	installationWide, err := store.FacetsInProjects(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if installationWide.Total != 2 ||
		installationWide.ObservedStateCounts[string(domain.StateCreated)] != 2 ||
		len(installationWide.Namespaces) != 2 || len(installationWide.Teams) != 2 {
		t.Fatalf("installation-wide facets = %#v", installationWide)
	}
}

func TestJobChangesDeliverOnlyAuthorizedProjectInvalidations(t *testing.T) {
	t.Parallel()
	store := openAuthorizationStore(t, "scoped-job-changes")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	execAuthorizationFixture(t, store,
		`INSERT INTO projects(id,installation_id,name,created_at) VALUES(?,?,?,?)`,
		"other", "default", "Other", now)
	execAuthorizationFixture(t, store,
		`INSERT INTO namespace_bindings
		 (id,installation_id,project_id,namespace,created_at) VALUES(?,?,?,?,?)`,
		"other__other-ns", "default", "other", "other-ns", now)
	createScopedFacetJob(t, store, "default-job", "default", "default__default", "default", "alpha")
	createScopedFacetJob(t, store, "other-job", "other", "other__other-ns", "other-ns", "beta")

	defaultPage, err := store.JobChanges(t.Context(), []domain.ProjectID{"default"}, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	otherPage, err := store.JobChanges(t.Context(), []domain.ProjectID{"other"}, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	deniedPage, err := store.JobChanges(t.Context(), []domain.ProjectID{}, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(defaultPage.Changes) != 1 || len(otherPage.Changes) != 1 ||
		defaultPage.Changes[0].JobID == otherPage.Changes[0].JobID ||
		len(deniedPage.Changes) != 0 {
		t.Fatalf("scoped changes = default %#v, other %#v, denied %#v",
			defaultPage, otherPage, deniedPage)
	}
}

func createScopedFacetJob(
	t *testing.T,
	store *Store,
	name string,
	projectID domain.ProjectID,
	bindingID domain.NamespaceBindingID,
	namespace string,
	team string,
) {
	t.Helper()
	_, err := store.Create(t.Context(), domain.CreateJob{
		Name: name, Namespace: namespace, Team: team,
		Template: json.RawMessage(`{
			"spec":{"template":{"spec":{"restartPolicy":"Never","containers":[{"name":"job","image":"busybox"}]}}}
		}`),
		ProjectID: projectID, NamespaceBindingID: bindingID,
		CreatorPrincipalID: "legacy_admin",
		SubmissionSource:   domain.SubmissionSourceLegacyCompatibility,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func openAuthorizationStore(t *testing.T, name string) *Store {
	t.Helper()
	store, err := Open(t.Context(), "file:"+name+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	scope, err := domain.NewNamespaceScope(domain.WatchModeSelected, []string{"default"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BackfillCompatibility(t.Context(), scope); err != nil {
		t.Fatal(err)
	}
	return store
}

func execAuthorizationFixture(t *testing.T, store *Store, query string, args ...any) {
	t.Helper()
	if _, err := store.db.ExecContext(t.Context(), store.bind(query), args...); err != nil {
		t.Fatal(err)
	}
}
