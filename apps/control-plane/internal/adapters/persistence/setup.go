package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain/policyquota"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/ports"
)

func (s *Store) HasVerifiedInstallationOwner(ctx context.Context) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*)
		FROM role_bindings rb
		JOIN role_definitions rd ON rd.id=rb.role_definition_id
		JOIN principals p ON p.id=rb.principal_id
		WHERE rd.id='installation_owner' AND rd.built_in=TRUE
		  AND rb.scope_type='INSTALLATION' AND p.kind='HUMAN'
		  AND p.disabled_at IS NULL
		  AND (
		    EXISTS (SELECT 1 FROM external_identities ei WHERE ei.principal_id=p.id)
		    OR EXISTS (SELECT 1 FROM local_accounts la WHERE la.principal_id=p.id)
		  )`,
	).Scan(&count)
	return count > 0, err
}

func (s *Store) ClaimSetup(
	ctx context.Context, input domain.SetupClaimInput, fingerprint string,
) (domain.SetupClaim, error) {
	if !s.postgres {
		s.setupMu.Lock()
		defer s.setupMu.Unlock()
	}
	var claim domain.SetupClaim
	err := s.auditTransaction(ctx, func(tx *sql.Tx) error {
		now := time.Now().UTC()
		nowText := now.Format(time.RFC3339Nano)
		installationID := domain.InstallationID("default")
		lockQuery := `SELECT id FROM installations WHERE id=?`
		if s.postgres {
			lockQuery += ` FOR UPDATE`
		}
		var lockedInstallation string
		if err := tx.QueryRowContext(
			ctx, s.bind(lockQuery), installationID,
		).Scan(&lockedInstallation); err != nil {
			return fmt.Errorf("lock installation setup: %w", err)
		}
		var owners int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*)
			FROM role_bindings rb JOIN role_definitions rd ON rd.id=rb.role_definition_id
			JOIN principals p ON p.id=rb.principal_id
			WHERE rd.id='installation_owner' AND p.kind='HUMAN'
			  AND p.disabled_at IS NULL AND (
			    EXISTS (SELECT 1 FROM external_identities ei WHERE ei.principal_id=p.id)
			    OR EXISTS (SELECT 1 FROM local_accounts la WHERE la.principal_id=p.id)
			  )`,
		).Scan(&owners); err != nil {
			return err
		}
		if owners > 0 {
			if err := s.scanLocalSetupClaim(ctx, tx, fingerprint, &claim); err == nil {
				return nil
			}
			return domain.ErrSetupClaimConflict
		}

		projectID := domain.ProjectID("default")
		if _, err := tx.ExecContext(ctx, s.bind(
			`UPDATE installations SET name=? WHERE id=?`,
		), strings.TrimSpace(input.InstallationName), installationID); err != nil {
			return fmt.Errorf("configure installation: %w", err)
		}
		if _, err := tx.ExecContext(ctx, s.bind(
			`UPDATE projects SET name=? WHERE id=? AND installation_id=?`,
		), strings.TrimSpace(input.ProjectName), projectID, installationID); err != nil {
			return fmt.Errorf("configure initial project: %w", err)
		}
		if _, err := tx.ExecContext(ctx, s.bind(
			`INSERT INTO principals(id,installation_id,kind,display_name,created_at)
			 VALUES(?,?,'HUMAN',?,?)`,
		), input.LocalAdmin.PrincipalID, installationID,
			strings.TrimSpace(input.LocalAdmin.Username), nowText); err != nil {
			return fmt.Errorf("create local owner principal: %w", err)
		}
		if _, err := tx.ExecContext(ctx, s.bind(
			`INSERT INTO local_accounts(
			 principal_id,normalized_username,username,password_hash,created_at,updated_at
			 ) VALUES(?,?,?,?,?,?)`,
		), input.LocalAdmin.PrincipalID,
			domain.NormalizeLocalUsername(input.LocalAdmin.Username),
			strings.TrimSpace(input.LocalAdmin.Username), input.LocalAdmin.PasswordHash,
			nowText, nowText); err != nil {
			return fmt.Errorf("create local owner credential: %w", err)
		}
		if _, err := tx.ExecContext(ctx, s.bind(
			`INSERT INTO role_bindings(
			 id,installation_id,role_definition_id,scope_type,principal_id,created_at
			 ) VALUES(?,?,'installation_owner','INSTALLATION',?,?)`,
		), "setup_owner", installationID, input.LocalAdmin.PrincipalID, nowText); err != nil {
			return fmt.Errorf("grant installation owner access: %w", err)
		}
		for _, namespace := range input.Namespaces {
			binding, _ := domain.NewNamespaceBinding(projectID, namespace)
			if _, err := tx.ExecContext(ctx, s.bind(
				`INSERT INTO namespace_bindings(
				 id,installation_id,project_id,namespace,created_at
				 ) VALUES(?,?,?,?,?) ON CONFLICT(namespace) DO UPDATE SET
				 installation_id=excluded.installation_id,project_id=excluded.project_id`,
			), binding.ID, installationID, projectID, binding.Namespace, nowText); err != nil {
				return fmt.Errorf("bind namespace %q: %w", binding.Namespace, err)
			}
		}
		p := input.Policy
		if _, err := tx.ExecContext(ctx, s.bind(
			`INSERT INTO installation_admission_policy(
			 installation_id,global_concurrency,namespace_concurrency,queue_capacity,
			 minimum_priority,maximum_priority,maximum_delay_seconds
			 ) VALUES(?,?,?,?,?,?,?) ON CONFLICT(installation_id) DO UPDATE SET
			 global_concurrency=excluded.global_concurrency,
			 namespace_concurrency=excluded.namespace_concurrency,
			 queue_capacity=excluded.queue_capacity,minimum_priority=excluded.minimum_priority,
			 maximum_priority=excluded.maximum_priority,
			 maximum_delay_seconds=excluded.maximum_delay_seconds`,
		), installationID, p.GlobalConcurrency, p.NamespaceConcurrency, p.QueueCapacity,
			p.MinimumPriority, p.MaximumPriority, p.MaximumDelaySeconds); err != nil {
			return fmt.Errorf("configure admission policy: %w", err)
		}
		if _, err := tx.ExecContext(ctx, s.bind(
			`INSERT INTO project_quotas(project_id,maximum_running_jobs,maximum_queued_jobs)
			 VALUES(?,?,?) ON CONFLICT(project_id) DO UPDATE SET
			 maximum_running_jobs=excluded.maximum_running_jobs,
			 maximum_queued_jobs=excluded.maximum_queued_jobs`,
		), projectID, p.MaximumRunningJobs, p.MaximumQueuedJobs); err != nil {
			return fmt.Errorf("configure project quota: %w", err)
		}
		if err := s.ensureSetupPolicyQuotaTx(
			ctx, tx, installationID, nowText,
			localSetupPolicyInput{ProjectID: projectID, Policy: input.Policy},
		); err != nil {
			return err
		}
		claim = domain.SetupClaim{
			InstallationID:   installationID,
			OwnerPrincipalID: input.LocalAdmin.PrincipalID,
			Username:         strings.TrimSpace(input.LocalAdmin.Username),
			Status:           "COMPLETED",
			CreatedAt:        now,
		}
		if _, err := tx.ExecContext(ctx, s.bind(
			`INSERT INTO local_setup_completions(
			 installation_id,owner_principal_id,username,claim_fingerprint,created_at
			 ) VALUES(?,?,?,?,?)`,
		), installationID, claim.OwnerPrincipalID, claim.Username, fingerprint, nowText); err != nil {
			return fmt.Errorf("record local setup completion: %w", err)
		}
		return nil
	})
	return claim, err
}

func (s *Store) scanLocalSetupClaim(
	ctx context.Context, tx *sql.Tx, fingerprint string, claim *domain.SetupClaim,
) error {
	var created string
	if err := tx.QueryRowContext(ctx, s.bind(
		`SELECT installation_id,owner_principal_id,username,created_at
		 FROM local_setup_completions WHERE claim_fingerprint=?`,
	), fingerprint).Scan(
		&claim.InstallationID, &claim.OwnerPrincipalID, &claim.Username, &created,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ports.ErrNotFound
		}
		return err
	}
	claim.Status = "COMPLETED"
	claim.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return nil
}

func (s *Store) ensureSetupPolicyQuotaTx(
	ctx context.Context,
	tx *sql.Tx,
	installationID domain.InstallationID,
	now string,
	input localSetupPolicyInput,
) error {
	var projectID domain.ProjectID
	var globalConcurrent, namespaceConcurrent, queueCapacity int
	var minimumPriority, maximumPriority, maximumDelaySeconds int
	var projectConcurrent, projectQueued int
	projectID = input.ProjectID
	policy := input.Policy
	globalConcurrent = policy.GlobalConcurrency
	namespaceConcurrent = policy.NamespaceConcurrency
	queueCapacity = policy.QueueCapacity
	minimumPriority = policy.MinimumPriority
	maximumPriority = policy.MaximumPriority
	maximumDelaySeconds = policy.MaximumDelaySeconds
	projectConcurrent = policy.MaximumRunningJobs
	projectQueued = policy.MaximumQueuedJobs
	delay := time.Duration(maximumDelaySeconds) * time.Second
	if delay == 0 {
		delay = time.Nanosecond
	}
	execution := 24 * time.Hour
	defaultPriority := 0
	if minimumPriority > defaultPriority {
		defaultPriority = minimumPriority
	}
	if maximumPriority < defaultPriority {
		defaultPriority = maximumPriority
	}
	global := policyquota.ScopedLimits{
		MaxConcurrent: setupUint(globalConcurrent),
		MaxQueued:     setupUint(queueCapacity),
		MaxRetained:   setupUint(queueCapacity),
	}
	parentProject := policyquota.ScopedLimits{
		MaxConcurrent: setupUint(max(globalConcurrent, projectConcurrent)),
		MaxQueued:     setupUint(max(queueCapacity, projectQueued)),
		MaxRetained:   setupUint(max(queueCapacity, projectQueued)),
	}
	namespace := policyquota.ScopedLimits{
		MaxConcurrent: setupUint(namespaceConcurrent),
		MaxQueued:     setupUint(queueCapacity),
		MaxRetained:   setupUint(queueCapacity),
	}
	installationPolicy := policyquota.Policy{
		Ref: policyquota.PolicyRef{
			ID: "setup_installation_policy_" + string(installationID), Version: 1,
			Scope: policyquota.Scope{Kind: policyquota.ScopeInstallation},
		},
		Rules: policyquota.Rules{
			Quotas: policyquota.QuotaLimits{
				Global: global, Project: parentProject, Namespace: namespace,
			},
			Priority: &policyquota.PriorityRange{
				Min: minimumPriority, Max: maximumPriority, Default: defaultPriority,
			},
			MaxDelayedStart: &delay, MaxExecutionDuration: &execution,
		},
	}
	projectPolicy := policyquota.Policy{
		Ref: policyquota.PolicyRef{
			ID: "setup_project_policy_" + string(projectID), Version: 1,
			Scope: policyquota.Scope{
				Kind: policyquota.ScopeProject, Project: string(projectID),
			},
		},
		Rules: policyquota.Rules{
			Quotas: policyquota.QuotaLimits{
				Project: policyquota.ScopedLimits{
					MaxConcurrent: setupUint(projectConcurrent),
					MaxQueued:     setupUint(projectQueued),
					MaxRetained:   setupUint(projectQueued),
				},
			},
		},
	}
	if err := s.insertSetupPolicyTx(
		ctx, tx, installationID, installationPolicy, now,
	); err != nil {
		return err
	}
	if err := s.insertSetupPolicyTx(
		ctx, tx, installationID, projectPolicy, now,
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.bind(
		`UPDATE projects SET scheduling_weight=1
		 WHERE id=? AND installation_id=?`,
	), projectID, installationID); err != nil {
		return fmt.Errorf("activate setup project scheduling weight: %w", err)
	}
	for _, scope := range []policyquota.Scope{
		{Kind: policyquota.ScopeInstallation},
		{Kind: policyquota.ScopeProject, Project: string(projectID)},
	} {
		if _, err := tx.ExecContext(ctx, s.bind(
			`INSERT INTO quota_usage(
			 installation_id,scope_key,scope_type,project_id,namespace,updated_at
			 ) VALUES(?,?,?,?,?,?)
			 ON CONFLICT(installation_id,scope_key) DO NOTHING`,
		), installationID, policyScopeKey(scope), scope.Kind,
			nullableString(scope.Project), nil, now); err != nil {
			return fmt.Errorf("initialize setup quota usage: %w", err)
		}
	}
	return nil
}

type localSetupPolicyInput struct {
	ProjectID domain.ProjectID
	Policy    domain.SetupPolicy
}

func (s *Store) insertSetupPolicyTx(
	ctx context.Context,
	tx *sql.Tx,
	installationID domain.InstallationID,
	policy policyquota.Policy,
	now string,
) error {
	encoded, err := json.Marshal(policy.Rules)
	if err != nil {
		return fmt.Errorf("encode setup policy: %w", err)
	}
	scope := policy.Ref.Scope
	if _, err := tx.ExecContext(ctx, s.bind(
		`INSERT INTO policy_scopes(
		 id,installation_id,scope_key,scope_type,project_id,namespace,
		 current_version,created_at,updated_at
		 ) VALUES(?,?,?,?,?,?,1,?,?)
		 ON CONFLICT DO NOTHING`,
	), policy.Ref.ID, installationID, policyScopeKey(scope), scope.Kind,
		nullableString(scope.Project), nil, now, now); err != nil {
		return fmt.Errorf("activate setup policy scope: %w", err)
	}
	if _, err := tx.ExecContext(ctx, s.bind(
		`INSERT INTO policy_versions(policy_id,version,rules,created_at)
		 SELECT id,1,?,? FROM policy_scopes
		 WHERE id=? AND installation_id=? AND scope_key=? AND current_version=1
		 ON CONFLICT(policy_id,version) DO NOTHING`,
	), string(encoded), now, policy.Ref.ID, installationID,
		policyScopeKey(scope)); err != nil {
		return fmt.Errorf("activate setup policy version: %w", err)
	}
	return nil
}

func setupUint(value int) *uint64 {
	result := uint64(value)
	return &result
}

func (s *Store) SetupRecovery(ctx context.Context) (domain.SetupRecovery, error) {
	var completed int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM local_setup_completions`,
	).Scan(&completed); err != nil {
		return domain.SetupRecovery{}, err
	}
	return domain.SetupRecovery{
		Completed: completed > 0,
		Checklist: []string{
			"Create and verify a second installation-owner recovery path.",
			"Back up PostgreSQL and the referenced encryption Secrets.",
			"Record the local owner password-reset and session-revocation procedure.",
			"Verify worker and Kubernetes namespace authority readiness.",
			"Store break-glass configuration offline; it is not enabled by setup.",
		},
	}, nil
}
