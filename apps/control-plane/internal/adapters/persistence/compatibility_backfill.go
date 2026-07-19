package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
)

const (
	defaultInstallationID = domain.InstallationID("default")
	defaultProjectID      = domain.ProjectID("default")
	legacyAdminID         = domain.PrincipalID("legacy_admin")
)

type builtInRole struct {
	id          string
	name        string
	scope       string
	permissions []domain.Permission
}

var builtInRoles = []builtInRole{
	{"installation_owner", "Installation Owner", "INSTALLATION",
		[]domain.Permission{domain.PermissionInternalAll}},
	{"installation_administrator", "Installation Administrator", "INSTALLATION",
		[]domain.Permission{domain.PermissionProjectsManage, domain.PermissionNamespaceBindingsManage,
			domain.PermissionMembersRead, domain.PermissionMembersManage, domain.PermissionRolesRead,
			domain.PermissionRolesAssign, domain.PermissionRolesDefine,
			domain.PermissionServiceAccountsManage, domain.PermissionTokensManage,
			domain.PermissionPoliciesRead, domain.PermissionPoliciesManage, domain.PermissionQuotasManage,
			domain.PermissionSystemStatusRead, domain.PermissionSupportDiagnosticsRead}},
	{"project_administrator", "Project Administrator", "PROJECT",
		[]domain.Permission{domain.PermissionJobsList, domain.PermissionJobsRead,
			domain.PermissionJobsManifestRead, domain.PermissionJobsSubmit, domain.PermissionJobsPause,
			domain.PermissionJobsResume, domain.PermissionJobsTerminate, domain.PermissionJobsRetry,
			domain.PermissionJobsTakeControl, domain.PermissionJobsArchive, domain.PermissionJobEventsRead,
			domain.PermissionEventStreamRead, domain.PermissionQueueRead,
			domain.PermissionQueueEntryUpdate, domain.PermissionQueueProjectReorder,
			domain.PermissionMembersRead, domain.PermissionMembersManage, domain.PermissionPoliciesRead,
			domain.PermissionPoliciesManage, domain.PermissionQuotasManage}},
	{"operator", "Operator", "PROJECT",
		[]domain.Permission{domain.PermissionJobsList, domain.PermissionJobsRead,
			domain.PermissionJobsManifestRead, domain.PermissionJobsSubmit, domain.PermissionJobsPause,
			domain.PermissionJobsResume, domain.PermissionJobsTerminate, domain.PermissionJobsRetry,
			domain.PermissionJobsTakeControl, domain.PermissionJobsArchive, domain.PermissionJobEventsRead,
			domain.PermissionEventStreamRead, domain.PermissionQueueRead,
			domain.PermissionQueueEntryUpdate, domain.PermissionQueueProjectReorder}},
	{"submitter", "Submitter", "PROJECT",
		[]domain.Permission{domain.PermissionJobsList, domain.PermissionJobsRead,
			domain.PermissionJobsSubmit, domain.PermissionQueueRead}},
	{"viewer", "Viewer", "PROJECT",
		[]domain.Permission{domain.PermissionJobsList, domain.PermissionJobsRead,
			domain.PermissionJobEventsRead, domain.PermissionEventStreamRead, domain.PermissionQueueRead}},
	{"auditor", "Auditor", "INSTALLATION",
		[]domain.Permission{domain.PermissionAuditRead, domain.PermissionAuditExport,
			domain.PermissionSystemStatusRead, domain.PermissionSupportDiagnosticsRead}},
}

// MigrateAndBackfill applies schema migrations and the Phase 3 compatibility
// backfill. The namespace scope is supplied by platform composition so this
// persistence package never reads process configuration.
func MigrateAndBackfill(
	ctx context.Context, databaseURL string, scope domain.NamespaceScope,
) error {
	store, err := open(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	if err := store.migrate(ctx); err != nil {
		return err
	}
	return store.backfillCompatibility(ctx, scope)
}

func (s *Store) backfillCompatibility(
	ctx context.Context, scope domain.NamespaceScope,
) error {
	installation, err := domain.NewInstallation(defaultInstallationID, "Default")
	if err != nil {
		return fmt.Errorf("build compatibility installation: %w", err)
	}
	project, err := domain.NewProject(defaultProjectID, installation.ID, "Default")
	if err != nil {
		return fmt.Errorf("build compatibility project: %w", err)
	}
	principal, err := domain.NewPrincipal(legacyAdminID, installation.ID, "Legacy administrator")
	if err != nil {
		return fmt.Errorf("build compatibility principal: %w", err)
	}

	return s.transaction(ctx, func(tx *sql.Tx) error {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if err := s.seedCompatibilityIdentity(ctx, tx, installation, project, principal, now); err != nil {
			return err
		}
		namespaces, err := compatibilityNamespaces(ctx, tx, scope)
		if err != nil {
			return err
		}
		for _, namespace := range namespaces {
			binding, err := domain.NewNamespaceBinding(project.ID, namespace)
			if err != nil {
				return fmt.Errorf("validate compatibility namespace binding: %w", err)
			}
			if _, err := tx.ExecContext(ctx, s.bind(
				`INSERT INTO namespace_bindings
					(id,installation_id,project_id,namespace,created_at)
				 VALUES(?,?,?,?,?) ON CONFLICT(namespace) DO NOTHING`,
			), binding.ID, installation.ID, binding.ProjectID, binding.Namespace, now); err != nil {
				return fmt.Errorf("seed namespace binding %q: %w", namespace, err)
			}
		}
		if _, err := tx.ExecContext(ctx, s.bind(
			`UPDATE jobs SET
				project_id=COALESCE(project_id,?),
				namespace_binding_id=COALESCE(namespace_binding_id,
					(SELECT id FROM namespace_bindings WHERE namespace=jobs.namespace)),
				creator_principal_id=COALESCE(creator_principal_id,?),
				submission_source=COALESCE(submission_source,?)
			 WHERE project_id IS NULL OR namespace_binding_id IS NULL
				OR creator_principal_id IS NULL OR submission_source IS NULL`,
		), project.ID, principal.ID, domain.SubmissionSourceLegacyCompatibility); err != nil {
			return fmt.Errorf("backfill job ownership: %w", err)
		}
		return nil
	})
}

// BackfillCompatibility applies the idempotent legacy-admin compatibility
// identity and ownership backfill to an already-open store.
func (s *Store) BackfillCompatibility(
	ctx context.Context, scope domain.NamespaceScope,
) error {
	return s.backfillCompatibility(ctx, scope)
}

func (s *Store) seedCompatibilityIdentity(
	ctx context.Context,
	tx *sql.Tx,
	installation domain.Installation,
	project domain.Project,
	principal domain.Principal,
	now string,
) error {
	if _, err := tx.ExecContext(ctx, s.bind(
		`INSERT INTO installations(id,name,created_at) VALUES(?,?,?)
		 ON CONFLICT(id) DO NOTHING`,
	), installation.ID, installation.Name, now); err != nil {
		return fmt.Errorf("seed compatibility installation: %w", err)
	}
	if _, err := tx.ExecContext(ctx, s.bind(
		`INSERT INTO projects(id,installation_id,name,created_at) VALUES(?,?,?,?)
		 ON CONFLICT(id) DO NOTHING`,
	), project.ID, project.InstallationID, project.Name, now); err != nil {
		return fmt.Errorf("seed compatibility project: %w", err)
	}
	if _, err := tx.ExecContext(ctx, s.bind(
		`INSERT INTO principals(id,installation_id,kind,display_name,created_at)
		 VALUES(?,?,?,?,?) ON CONFLICT(id) DO NOTHING`,
	), principal.ID, principal.InstallationID, "LEGACY_ADMIN", principal.DisplayName, now); err != nil {
		return fmt.Errorf("seed legacy administrator principal: %w", err)
	}
	for _, role := range builtInRoles {
		permissions, err := json.Marshal(role.permissions)
		if err != nil {
			return fmt.Errorf("encode built-in role %q permissions: %w", role.id, err)
		}
		if _, err := tx.ExecContext(ctx, s.bind(
			`INSERT INTO role_definitions
				(id,installation_id,name,scope_type,permissions,built_in,created_at)
			 VALUES(?,?,?,?,?,TRUE,?) ON CONFLICT(id) DO UPDATE SET
				name=excluded.name,scope_type=excluded.scope_type,
				permissions=excluded.permissions,built_in=TRUE`,
		), role.id, installation.ID, role.name, role.scope, string(permissions), now); err != nil {
			return fmt.Errorf("seed built-in role %q: %w", role.id, err)
		}
	}
	if _, err := tx.ExecContext(ctx, s.bind(
		`INSERT INTO role_bindings
			(id,installation_id,role_definition_id,scope_type,principal_id,created_at)
		 VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`,
	), "legacy_admin_owner", installation.ID, "installation_owner",
		"INSTALLATION", principal.ID, now); err != nil {
		return fmt.Errorf("seed legacy administrator owner binding: %w", err)
	}
	return nil
}

func compatibilityNamespaces(
	ctx context.Context, tx *sql.Tx, scope domain.NamespaceScope,
) ([]string, error) {
	unique := make(map[string]struct{})
	// Selected scope is enumerable, so every configured namespace is bound.
	// All-namespace scope is not enumerable; only namespaces represented by
	// historical jobs are bound. Future discovery creates bindings in a later slice.
	if scope.Mode() == domain.WatchModeSelected {
		for _, namespace := range scope.Namespaces() {
			unique[namespace] = struct{}{}
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT namespace FROM jobs ORDER BY namespace`)
	if err != nil {
		return nil, fmt.Errorf("read historical job namespaces: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var namespace string
		if err := rows.Scan(&namespace); err != nil {
			return nil, fmt.Errorf("scan historical job namespace: %w", err)
		}
		unique[namespace] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read historical job namespaces: %w", err)
	}
	namespaces := make([]string, 0, len(unique))
	for namespace := range unique {
		namespaces = append(namespaces, namespace)
	}
	sort.Strings(namespaces)
	return namespaces, nil
}
