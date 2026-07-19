package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain/policyquota"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/ports"
)

func (s *Store) PolicyHierarchy(
	ctx context.Context,
	installationID domain.InstallationID,
	target policyquota.Scope,
) ([]policyquota.Policy, error) {
	if installationID == "" {
		return nil, errors.New("installation ID is required")
	}
	scopes, err := hierarchyScopes(target)
	if err != nil {
		return nil, err
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(scopes)), ",")
	arguments := make([]any, 0, len(scopes)+1)
	arguments = append(arguments, installationID)
	scopeByKey := make(map[string]policyquota.Scope, len(scopes))
	for _, scope := range scopes {
		key := policyScopeKey(scope)
		scopeByKey[key] = scope
		arguments = append(arguments, key)
	}
	rows, err := s.db.QueryContext(ctx, s.bind(fmt.Sprintf(
		`SELECT ps.scope_key,ps.id,ps.current_version,pv.rules
		 FROM policy_scopes ps
		 JOIN policy_versions pv
		   ON pv.policy_id=ps.id AND pv.version=ps.current_version
		 WHERE ps.installation_id=? AND ps.scope_key IN (%s)`,
		placeholders,
	)), arguments...)
	if err != nil {
		return nil, fmt.Errorf("read policy hierarchy: %w", err)
	}
	defer func() { _ = rows.Close() }()
	byKey := make(map[string]policyquota.Policy, len(scopes))
	for rows.Next() {
		var key, encoded string
		var policy policyquota.Policy
		if err := rows.Scan(&key, &policy.Ref.ID, &policy.Ref.Version, &encoded); err != nil {
			return nil, fmt.Errorf("scan current policy: %w", err)
		}
		policy.Ref.Scope = scopeByKey[key]
		if err := json.Unmarshal([]byte(encoded), &policy.Rules); err != nil {
			return nil, fmt.Errorf("decode policy rules: %w", err)
		}
		byKey[key] = policy
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read policy hierarchy rows: %w", err)
	}
	if _, found := byKey[policyScopeKey(scopes[0])]; !found {
		return nil, ports.ErrNotFound
	}
	policies := make([]policyquota.Policy, 0, len(scopes))
	for _, scope := range scopes {
		if policy, found := byKey[policyScopeKey(scope)]; found {
			policies = append(policies, policy)
		}
	}
	return policies, nil
}

func (s *Store) CompareAndSetPolicy(
	ctx context.Context,
	installationID domain.InstallationID,
	expectedVersion uint64,
	next policyquota.Policy,
) error {
	if installationID == "" || strings.TrimSpace(next.Ref.ID) == "" {
		return errors.New("installation ID and policy ID are required")
	}
	if err := validatePolicyScope(next.Ref.Scope); err != nil {
		return err
	}
	if expectedVersion >= math.MaxInt64 || next.Ref.Version > math.MaxInt64 ||
		next.Ref.Version != expectedVersion+1 || next.Ref.Version == 0 {
		return fmt.Errorf("%w: next policy version must follow expected version", ports.ErrConflict)
	}
	encoded, err := json.Marshal(next.Rules)
	if err != nil {
		return fmt.Errorf("encode policy rules: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	return s.auditTransaction(ctx, func(tx *sql.Tx) error {
		if err := s.validatePolicyAgainstParents(ctx, tx, installationID, next); err != nil {
			return err
		}
		if expectedVersion == 0 {
			result, err := tx.ExecContext(ctx, s.bind(
				`INSERT INTO policy_scopes(
				 id,installation_id,scope_key,scope_type,project_id,namespace,
				 current_version,created_at,updated_at
				 ) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`,
			), next.Ref.ID, installationID, policyScopeKey(next.Ref.Scope), next.Ref.Scope.Kind,
				nullableString(next.Ref.Scope.Project), nullableString(next.Ref.Scope.Namespace),
				next.Ref.Version, now, now)
			if err != nil {
				return fmt.Errorf("create policy scope: %w", err)
			}
			created, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if created != 1 {
				return ports.ErrConflict
			}
		} else {
			result, err := tx.ExecContext(ctx, s.bind(
				`UPDATE policy_scopes SET current_version=?,updated_at=?
				 WHERE id=? AND installation_id=? AND scope_key=? AND current_version=?`,
			), next.Ref.Version, now, next.Ref.ID, installationID,
				policyScopeKey(next.Ref.Scope), expectedVersion)
			if err != nil {
				return fmt.Errorf("advance policy version: %w", err)
			}
			updated, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if updated != 1 {
				return ports.ErrConflict
			}
		}
		if _, err := tx.ExecContext(ctx, s.bind(
			`INSERT INTO policy_versions(policy_id,version,rules,created_at)
			 VALUES(?,?,?,?)`,
		), next.Ref.ID, next.Ref.Version, string(encoded), now); err != nil {
			return fmt.Errorf("write policy version: %w", err)
		}
		return nil
	})
}

func (s *Store) validatePolicyAgainstParents(
	ctx context.Context,
	tx *sql.Tx,
	installationID domain.InstallationID,
	next policyquota.Policy,
) error {
	proposed := make([]policyquota.Policy, 0, 3)
	parentScopes, err := hierarchyScopes(next.Ref.Scope)
	if err != nil {
		return err
	}
	for _, scope := range parentScopes[:len(parentScopes)-1] {
		parent, found, err := s.currentPolicyTx(ctx, tx, installationID, scope)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("%w: parent policy is missing", ports.ErrNotFound)
		}
		proposed = append(proposed, parent)
	}
	proposed = append(proposed, next)
	if _, err := policyquota.Compose(proposed...); err != nil {
		return fmt.Errorf("validate policy hierarchy: %w", err)
	}
	return nil
}

func (s *Store) currentPolicyTx(
	ctx context.Context,
	tx *sql.Tx,
	installationID domain.InstallationID,
	scope policyquota.Scope,
) (policyquota.Policy, bool, error) {
	query := `SELECT ps.id,ps.current_version,pv.rules
		FROM policy_scopes ps
		JOIN policy_versions pv
		  ON pv.policy_id=ps.id AND pv.version=ps.current_version
		WHERE ps.installation_id=? AND ps.scope_key=?`
	if s.postgres {
		query += ` FOR UPDATE OF ps`
	}
	var policy policyquota.Policy
	var encoded string
	err := tx.QueryRowContext(ctx, s.bind(query),
		installationID, policyScopeKey(scope)).Scan(
		&policy.Ref.ID, &policy.Ref.Version, &encoded,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return policyquota.Policy{}, false, nil
	}
	if err != nil {
		return policyquota.Policy{}, false, fmt.Errorf("read parent policy: %w", err)
	}
	policy.Ref.Scope = scope
	if err := json.Unmarshal([]byte(encoded), &policy.Rules); err != nil {
		return policyquota.Policy{}, false, fmt.Errorf("decode parent policy: %w", err)
	}
	return policy, true, nil
}

func hierarchyScopes(target policyquota.Scope) ([]policyquota.Scope, error) {
	if err := validatePolicyScope(target); err != nil {
		return nil, err
	}
	scopes := []policyquota.Scope{{Kind: policyquota.ScopeInstallation}}
	if target.Kind == policyquota.ScopeProject || target.Kind == policyquota.ScopeNamespace {
		scopes = append(scopes, policyquota.Scope{
			Kind: policyquota.ScopeProject, Project: target.Project,
		})
	}
	if target.Kind == policyquota.ScopeNamespace {
		scopes = append(scopes, target)
	}
	return scopes, nil
}

func validatePolicyScope(scope policyquota.Scope) error {
	switch scope.Kind {
	case policyquota.ScopeInstallation:
		if scope.Project != "" || scope.Namespace != "" {
			return errors.New("installation scope cannot name a project or namespace")
		}
	case policyquota.ScopeProject:
		if strings.TrimSpace(scope.Project) == "" || scope.Namespace != "" {
			return errors.New("project scope requires only a project")
		}
	case policyquota.ScopeNamespace:
		if strings.TrimSpace(scope.Project) == "" || strings.TrimSpace(scope.Namespace) == "" {
			return errors.New("namespace scope requires a project and namespace")
		}
	default:
		return fmt.Errorf("unknown policy scope %q", scope.Kind)
	}
	return nil
}

func policyScopeKey(scope policyquota.Scope) string {
	switch scope.Kind {
	case policyquota.ScopeInstallation:
		return "I"
	case policyquota.ScopeProject:
		return "P:" + lengthPrefixed(scope.Project)
	case policyquota.ScopeNamespace:
		return "N:" + lengthPrefixed(scope.Project) + lengthPrefixed(scope.Namespace)
	default:
		return "I"
	}
}

func lengthPrefixed(value string) string {
	return strconv.Itoa(len(value)) + ":" + value
}
