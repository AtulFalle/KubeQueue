package persistence

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain/policyquota"
)

func TestSetupClaimAtomicallyCreatesLocalOwnerAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, sqliteTestURL(t, "setup.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	scope, err := domain.NewNamespaceScope(domain.WatchModeSelected, []string{"default"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BackfillCompatibility(ctx, scope); err != nil {
		t.Fatal(err)
	}
	input := validSetupClaimInput()

	const callers = 2
	results := make(chan domain.SetupClaim, callers)
	failures := make(chan error, callers)
	var group sync.WaitGroup
	for range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			claim, claimErr := store.ClaimSetup(ctx, input, "same-fingerprint")
			if claimErr != nil {
				failures <- claimErr
				return
			}
			results <- claim
		}()
	}
	group.Wait()
	close(results)
	close(failures)
	for failure := range failures {
		t.Fatalf("concurrent idempotent ClaimSetup() error = %v", failure)
	}
	var ownerID domain.PrincipalID
	for claim := range results {
		if ownerID != "" && claim.OwnerPrincipalID != ownerID {
			t.Fatalf("idempotent claims created different owners %q and %q", ownerID, claim.OwnerPrincipalID)
		}
		ownerID = claim.OwnerPrincipalID
	}
	var claims int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM local_setup_completions`).Scan(&claims); err != nil {
		t.Fatal(err)
	}
	if claims != 1 {
		t.Fatalf("setup claim count = %d, want 1", claims)
	}
	owner, err := store.HasVerifiedInstallationOwner(ctx)
	if err != nil || !owner {
		t.Fatalf("owner after local setup = %v, error = %v", owner, err)
	}
	account, err := store.LocalAccountByUsername(ctx, "admin")
	if err != nil || account.PasswordHash != input.LocalAdmin.PasswordHash {
		t.Fatalf("local owner account = %#v, error = %v", account, err)
	}
	assertVersionedSetupState(t, store, "default", "default", input.Policy)
	if _, err := store.ClaimSetup(ctx, input, "new-fingerprint"); !errors.Is(err, domain.ErrSetupClaimConflict) {
		t.Fatalf("ClaimSetup() after completion error = %v, want conflict", err)
	}
}

func TestMigration012BackfillsGuardedSetupPolicyAndQuota(t *testing.T) {
	ctx := t.Context()
	store, err := open(ctx, sqliteTestURL(t, "setup-migration-012.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	connection, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.prepareMigrationHistory(ctx, connection, migrations); err != nil {
		_ = connection.Close()
		t.Fatal(err)
	}
	var migration012 migration
	for _, item := range migrations {
		if item.version == "migrations/012_policy_quota_fair_scheduling.sql" {
			migration012 = item
			break
		}
		if err := store.applyMigration(ctx, connection, item); err != nil {
			_ = connection.Close()
			t.Fatal(err)
		}
	}
	now := "2026-07-19T00:00:00Z"
	if _, err := connection.ExecContext(ctx,
		`INSERT INTO installations(id,name,created_at) VALUES('default','Example',?)
		 ON CONFLICT(id) DO NOTHING`, now,
	); err != nil {
		_ = connection.Close()
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(ctx,
		`INSERT INTO projects(id,installation_id,name,created_at)
		 VALUES('default','default','Platform',?)
		 ON CONFLICT(id) DO NOTHING`, now,
	); err != nil {
		_ = connection.Close()
		t.Fatal(err)
	}
	policy := validSetupClaimInput().Policy
	if _, err := connection.ExecContext(ctx,
		`INSERT INTO installation_admission_policy(
		 installation_id,global_concurrency,namespace_concurrency,queue_capacity,
		 minimum_priority,maximum_priority,maximum_delay_seconds
		 ) VALUES('default',?,?,?,?,?,?)`,
		policy.GlobalConcurrency, policy.NamespaceConcurrency, policy.QueueCapacity,
		policy.MinimumPriority, policy.MaximumPriority, policy.MaximumDelaySeconds,
	); err != nil {
		_ = connection.Close()
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(ctx,
		`INSERT INTO project_quotas(
		 project_id,maximum_running_jobs,maximum_queued_jobs
		 ) VALUES('default',?,?)`,
		policy.MaximumRunningJobs, policy.MaximumQueuedJobs,
	); err != nil {
		_ = connection.Close()
		t.Fatal(err)
	}
	if migration012.version == "" {
		_ = connection.Close()
		t.Fatal("migration 012 was not loaded")
	}
	if err := store.applyMigration(ctx, connection, migration012); err != nil {
		_ = connection.Close()
		t.Fatal(err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	assertVersionedSetupState(t, store, "default", "default", policy)
}

func TestLocalSetupRollsBackWhenVersionedPolicyCannotPersist(t *testing.T) {
	ctx := t.Context()
	store, err := Open(ctx, sqliteTestURL(t, "setup-policy-rollback.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	scope, err := domain.NewNamespaceScope(
		domain.WatchModeSelected, []string{"default"}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BackfillCompatibility(ctx, scope); err != nil {
		t.Fatal(err)
	}
	input := validSetupClaimInput()
	if _, err := store.db.ExecContext(ctx,
		`CREATE TRIGGER reject_setup_policy BEFORE INSERT ON policy_versions
		 BEGIN SELECT RAISE(ABORT, 'setup policy rejected'); END`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimSetup(ctx, input, "rollback-fingerprint"); err == nil {
		t.Fatal("ClaimSetup() error = nil")
	}
	var completed, policies int
	if err := store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM local_setup_completions`,
	).Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM policy_scopes WHERE installation_id='default'`,
	).Scan(&policies); err != nil {
		t.Fatal(err)
	}
	owner, err := store.HasVerifiedInstallationOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if completed != 0 || policies != 0 || owner {
		t.Fatalf(
			"rolled-back setup = completed %d policies %d owner %t",
			completed, policies, owner,
		)
	}
}

func TestCompetingSetupClaimsHaveOneWinner(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, sqliteTestURL(t, "setup-race.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	scope, err := domain.NewNamespaceScope(domain.WatchModeSelected, []string{"default"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BackfillCompatibility(ctx, scope); err != nil {
		t.Fatal(err)
	}
	input := validSetupClaimInput()
	start := make(chan struct{})
	errorsByClaim := make(chan error, 2)
	for _, fingerprint := range []string{"claim-a", "claim-b"} {
		go func() {
			<-start
			_, claimErr := store.ClaimSetup(ctx, input, fingerprint)
			errorsByClaim <- claimErr
		}()
	}
	close(start)
	var successes, conflicts int
	for range 2 {
		switch err := <-errorsByClaim; {
		case err == nil:
			successes++
		case errors.Is(err, domain.ErrSetupClaimConflict):
			conflicts++
		default:
			t.Fatalf("competing ClaimSetup() error = %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("race results = %d successes, %d conflicts", successes, conflicts)
	}
}

func validSetupClaimInput() domain.SetupClaimInput {
	return domain.SetupClaimInput{
		InstallationName: "Example", ProjectName: "Platform",
		Namespaces: []string{"default"},
		LocalAdmin: domain.SetupLocalAdmin{
			PrincipalID:  "local_owner",
			Username:     "Admin",
			PasswordHash: "$argon2id$v=19$m=65536,t=3,p=2$MDEyMzQ1Njc4OWFiY2RlZg$MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY",
		},
		Policy: domain.SetupPolicy{
			GlobalConcurrency: 10, NamespaceConcurrency: 2, QueueCapacity: 100,
			MinimumPriority: -100, MaximumPriority: 100, MaximumDelaySeconds: 3600,
			MaximumRunningJobs: 10, MaximumQueuedJobs: 100,
		},
	}
}

func assertVersionedSetupState(
	t *testing.T,
	store *Store,
	installationID domain.InstallationID,
	projectID domain.ProjectID,
	setup domain.SetupPolicy,
) {
	t.Helper()
	target := policyquota.Scope{
		Kind:    policyquota.ScopeNamespace,
		Project: string(projectID), Namespace: "not-materialized",
	}
	policies, err := store.PolicyHierarchy(t.Context(), installationID, target)
	if err != nil {
		t.Fatal(err)
	}
	if len(policies) != 2 ||
		policies[0].Ref.ID != "setup_installation_policy_"+string(installationID) ||
		policies[1].Ref.ID != "setup_project_policy_"+string(projectID) {
		t.Fatalf("setup policy hierarchy = %#v", policies)
	}
	effective, err := policyquota.Compose(policies...)
	if err != nil {
		t.Fatal(err)
	}
	if *effective.Rules.Quotas.Global.MaxConcurrent != uint64(setup.GlobalConcurrency) ||
		*effective.Rules.Quotas.Project.MaxConcurrent != uint64(setup.MaximumRunningJobs) ||
		*effective.Rules.Quotas.Project.MaxQueued != uint64(setup.MaximumQueuedJobs) ||
		*effective.Rules.Quotas.Namespace.MaxConcurrent != uint64(setup.NamespaceConcurrency) {
		t.Fatalf("effective setup policy = %#v", effective.Rules)
	}
	var weight, schedulingVersion uint64
	if err := store.db.QueryRowContext(t.Context(),
		`SELECT scheduling_weight,scheduling_version FROM projects WHERE id=?`,
		projectID,
	).Scan(&weight, &schedulingVersion); err != nil {
		t.Fatal(err)
	}
	if weight != 1 || schedulingVersion == 0 {
		t.Fatalf("setup scheduling state = weight %d version %d", weight, schedulingVersion)
	}
	var policyScopes, policyVersions, usageRows, namespacePolicies int
	if err := store.db.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM policy_scopes WHERE installation_id=?`,
		installationID,
	).Scan(&policyScopes); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM policy_versions
		 WHERE policy_id IN (
		   SELECT id FROM policy_scopes WHERE installation_id=?
		 )`,
		installationID,
	).Scan(&policyVersions); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM quota_usage
		 WHERE installation_id=? AND scope_type IN ('INSTALLATION','PROJECT')`,
		installationID,
	).Scan(&usageRows); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM policy_scopes
		 WHERE installation_id=? AND scope_type='NAMESPACE'`,
		installationID,
	).Scan(&namespacePolicies); err != nil {
		t.Fatal(err)
	}
	if policyScopes != 2 || policyVersions != 2 ||
		usageRows != 2 || namespacePolicies != 0 {
		t.Fatalf(
			"setup activation rows = scopes %d versions %d usage %d namespace policies %d",
			policyScopes, policyVersions, usageRows, namespacePolicies,
		)
	}
}
