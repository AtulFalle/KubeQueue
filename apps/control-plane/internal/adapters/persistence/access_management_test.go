package persistence

import (
	"errors"
	"testing"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
)

func TestAccessManagementRevisesRolesAndInvalidatesAffectedPrincipal(t *testing.T) {
	t.Parallel()
	store := openAuthorizationStore(t, "access-role-version")
	now := time.Now().UTC()
	principal, err := domain.NewManagedPrincipal(
		"role_user", "default", domain.PrincipalKindHuman, "Role User", now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreatePrincipal(t.Context(), principal); err != nil {
		t.Fatal(err)
	}
	role, err := domain.NewRoleDefinition(
		"custom_reader", "default", "Custom Reader", domain.RoleScopeProject,
		[]domain.Permission{domain.PermissionJobsRead}, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	storedRole, err := store.CreateRoleDefinition(t.Context(), role)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := domain.NewRoleBinding(
		"custom_reader_user", "default", role.ID, domain.RoleScopeProject,
		"default", domain.BindingSubjectPrincipal, principal.ID, "", now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRoleBinding(t.Context(), binding); err != nil {
		t.Fatal(err)
	}
	afterBinding, err := store.Principal(t.Context(), "default", principal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterBinding.AuthorizationGeneration != 2 {
		t.Fatalf("generation after binding = %d, want 2", afterBinding.AuthorizationGeneration)
	}
	duplicate, err := domain.NewRoleBinding(
		"duplicate_reader_user", "default", role.ID, domain.RoleScopeProject,
		"default", domain.BindingSubjectPrincipal, principal.ID, "", now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRoleBinding(
		t.Context(), duplicate,
	); !errors.Is(err, domain.ErrAccessConflict) {
		t.Fatalf("duplicate binding error = %v, want conflict", err)
	}

	changed, err := domain.NewRoleDefinition(
		role.ID, "default", "Custom Reader", domain.RoleScopeProject,
		[]domain.Permission{domain.PermissionJobsList, domain.PermissionJobsRead}, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := store.UpdateRoleDefinition(t.Context(), changed, storedRole.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 2 {
		t.Fatalf("updated revision = %d, want 2", updated.Revision)
	}
	reverted, err := store.UpdateRoleDefinition(t.Context(), role, updated.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if reverted.Revision != 3 {
		t.Fatalf("reverted revision = %d, want 3", reverted.Revision)
	}
	if _, err := store.UpdateRoleDefinition(
		t.Context(), changed, storedRole.Revision,
	); !errors.Is(err, domain.ErrAccessConflict) {
		t.Fatalf("ABA stale role update error = %v, want conflict", err)
	}
	revisions, err := store.ListRoleDefinitionRevisions(
		t.Context(), "default", role.ID, domain.AccessPage{Limit: 10},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(revisions) != 3 ||
		revisions[0].Revision != 3 ||
		revisions[1].Revision != 2 ||
		revisions[2].Revision != 1 {
		t.Fatalf("role revision history = %#v", revisions)
	}
	if len(revisions[2].Permissions) != 1 ||
		revisions[2].Permissions[0] != domain.PermissionJobsRead ||
		len(revisions[1].Permissions) != 2 {
		t.Fatalf("historical role contents were mutated: %#v", revisions)
	}
	afterRole, err := store.Principal(t.Context(), "default", principal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterRole.AuthorizationGeneration != 4 {
		t.Fatalf("generation after role updates = %d, want 4", afterRole.AuthorizationGeneration)
	}
	access, err := store.EffectiveAccess(
		t.Context(), "default", principal.ID, domain.AccessPage{Limit: 10},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(access.Grants) != 1 || !access.Grants[0].Direct ||
		access.Grants[0].RoleDefinitionID != role.ID ||
		access.Grants[0].RoleBindingID != binding.ID {
		t.Fatalf("effective access = %#v", access)
	}
	if err := store.DeleteRoleBinding(t.Context(), "default", binding.ID); err != nil {
		t.Fatal(err)
	}
	afterDelete, err := store.Principal(t.Context(), "default", principal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterDelete.AuthorizationGeneration != 5 {
		t.Fatalf("generation after binding deletion = %d, want 5",
			afterDelete.AuthorizationGeneration)
	}
}

func TestAccessManagementProtectsFinalEffectiveOwner(t *testing.T) {
	t.Parallel()
	store := openAuthorizationStore(t, "access-final-owner")
	owner, err := store.Principal(t.Context(), "default", "legacy_admin")
	if err != nil {
		t.Fatal(err)
	}
	disabledAt := time.Now().UTC()
	owner.DisabledAt = &disabledAt
	if _, err := store.UpdatePrincipal(
		t.Context(), owner,
	); !errors.Is(err, domain.ErrFinalInstallationOwner) {
		t.Fatalf("disable final owner error = %v, want final-owner protection", err)
	}
	current, err := store.Principal(t.Context(), "default", "legacy_admin")
	if err != nil {
		t.Fatal(err)
	}
	if current.DisabledAt != nil || current.AuthorizationGeneration != owner.AuthorizationGeneration {
		t.Fatalf("protected owner changed: %#v", current)
	}
	if err := store.DeleteRoleBinding(
		t.Context(), "default", "legacy_admin_owner",
	); !errors.Is(err, domain.ErrFinalInstallationOwner) {
		t.Fatalf("delete final owner binding error = %v", err)
	}
	if _, err := store.RoleBinding(
		t.Context(), "default", "legacy_admin_owner",
	); err != nil {
		t.Fatalf("protected owner binding was deleted: %v", err)
	}
}

func TestAccessManagementRejectsDuplicateTeamBinding(t *testing.T) {
	t.Parallel()
	store := openAuthorizationStore(t, "access-team-binding-unique")
	now := time.Now().UTC()
	team, err := domain.NewTeam("reviewers", "default", "Reviewers", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateTeam(t.Context(), team); err != nil {
		t.Fatal(err)
	}
	role, err := domain.NewRoleDefinition(
		"installation_reader", "default", "Installation Reader",
		domain.RoleScopeInstallation, []domain.Permission{domain.PermissionJobsRead}, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRoleDefinition(t.Context(), role); err != nil {
		t.Fatal(err)
	}
	for index, id := range []domain.RoleBindingID{"reviewers_reader_one", "reviewers_reader_two"} {
		binding, err := domain.NewRoleBinding(
			id, "default", role.ID, domain.RoleScopeInstallation, "",
			domain.BindingSubjectTeam, "", team.ID, now,
		)
		if err != nil {
			t.Fatal(err)
		}
		_, err = store.CreateRoleBinding(t.Context(), binding)
		if index == 0 && err != nil {
			t.Fatal(err)
		}
		if index == 1 && !errors.Is(err, domain.ErrAccessConflict) {
			t.Fatalf("duplicate team binding error = %v, want conflict", err)
		}
	}
}

func TestAccessManagementUsesBoundedNonEnumeratingReads(t *testing.T) {
	t.Parallel()
	store := openAuthorizationStore(t, "access-bounded-reads")
	if _, err := store.ListProjects(
		t.Context(), "default", domain.AccessPage{Limit: domain.MaxAccessPageSize + 1},
	); !errors.Is(err, domain.ErrInvalidAccessChange) {
		t.Fatalf("oversized list error = %v", err)
	}
	for _, installationID := range []domain.InstallationID{"default", "unknown_installation"} {
		_, err := store.Project(t.Context(), installationID, "missing_project")
		if !errors.Is(err, domain.ErrAccessResourceNotFound) {
			t.Fatalf("Project(%q) error = %v, want stable not found", installationID, err)
		}
	}
	now := time.Now().UTC()
	for _, id := range []domain.ProjectID{"alpha", "omega"} {
		project, err := domain.NewManagedProject(id, "default", string(id), now)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.CreateProject(t.Context(), project); err != nil {
			t.Fatal(err)
		}
	}
	first, err := store.ListProjects(t.Context(), "default", domain.AccessPage{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.ListProjects(t.Context(), "default", domain.AccessPage{
		Limit: 1, After: string(first[0].ID),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || first[0].ID != "alpha" ||
		len(second) != 1 || second[0].ID != "default" {
		t.Fatalf("keyset pages = %#v then %#v", first, second)
	}
}

func TestAccessRoleMigrationBackfillsAndDeduplicates(t *testing.T) {
	t.Parallel()
	store, err := open(
		t.Context(), "file:access-role-migration?mode=memory&cache=shared",
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	_, err = store.db.ExecContext(t.Context(), `
		CREATE TABLE installations(id TEXT PRIMARY KEY);
		CREATE TABLE projects(id TEXT PRIMARY KEY);
		CREATE TABLE principals(id TEXT PRIMARY KEY);
		CREATE TABLE teams(id TEXT PRIMARY KEY);
		CREATE TABLE role_definitions(
			id TEXT PRIMARY KEY,
			installation_id TEXT NOT NULL REFERENCES installations(id),
			name TEXT NOT NULL,
			scope_type TEXT NOT NULL,
			permissions TEXT NOT NULL,
			built_in BOOLEAN NOT NULL DEFAULT FALSE,
			created_at TEXT NOT NULL
		);
		CREATE TABLE role_bindings(
			id TEXT PRIMARY KEY,
			installation_id TEXT NOT NULL REFERENCES installations(id),
			role_definition_id TEXT NOT NULL REFERENCES role_definitions(id),
			scope_type TEXT NOT NULL,
			project_id TEXT NULL REFERENCES projects(id),
			principal_id TEXT NULL REFERENCES principals(id),
			team_id TEXT NULL REFERENCES teams(id),
			created_at TEXT NOT NULL
		);
		INSERT INTO installations(id) VALUES('default');
		INSERT INTO projects(id) VALUES('default');
		INSERT INTO principals(id) VALUES('user_one');
		INSERT INTO role_definitions(
			id,installation_id,name,scope_type,permissions,built_in,created_at
		) VALUES(
			'custom_reader','default','Custom Reader','PROJECT',
			'["jobs.read"]',FALSE,'2026-07-19T00:00:00Z'
		);
		INSERT INTO role_bindings(
			id,installation_id,role_definition_id,scope_type,project_id,
			principal_id,created_at
		) VALUES
			('reader_one','default','custom_reader','PROJECT','default',
			 'user_one','2026-07-19T00:00:00Z'),
			('reader_two','default','custom_reader','PROJECT','default',
			 'user_one','2026-07-19T00:00:00Z');
	`)
	if err != nil {
		t.Fatal(err)
	}
	source, err := migrationFiles.ReadFile(
		"migrations/017_access_role_revisions.sql",
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(
		t.Context(), store.renderMigration(string(source)),
	); err != nil {
		t.Fatal(err)
	}
	var revision, history, bindings int
	if err := store.db.QueryRowContext(t.Context(),
		`SELECT current_revision FROM role_definitions WHERE id='custom_reader'`,
	).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM role_definition_revisions
		 WHERE role_definition_id='custom_reader'`,
	).Scan(&history); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM role_bindings`,
	).Scan(&bindings); err != nil {
		t.Fatal(err)
	}
	if revision != 1 || history != 1 || bindings != 1 {
		t.Fatalf(
			"migration state = revision %d, history %d, bindings %d",
			revision, history, bindings,
		)
	}
	_, err = store.db.ExecContext(t.Context(), `
		INSERT INTO role_bindings(
			id,installation_id,role_definition_id,scope_type,project_id,
			principal_id,created_at
		) VALUES(
			'reader_three','default','custom_reader','PROJECT','default',
			'user_one','2026-07-19T00:00:00Z'
		)
	`)
	if !errors.Is(accessWriteError(err), domain.ErrAccessConflict) {
		t.Fatalf("post-migration duplicate error = %v, want conflict", err)
	}
}
