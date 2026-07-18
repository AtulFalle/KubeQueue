# ADR 0003: Namespace authority and discovery

- Status: Accepted
- Date: 2026-07-19

## Context

KubeQueue must discover Jobs without requiring operators to diagnose missing namespace watches.
Kubernetes RBAC cannot grant Job mutation by namespace label: namespace-scoped Roles require an
explicit namespace list, while automatic cluster-wide discovery requires a ClusterRole with broad
authority.

The Phase 1 comma-separated environment value is read only at worker startup. Submissions outside
that scope can be persisted even though the worker cannot observe or create them, and changing the
scope can leave stale records affecting scheduling.

## Decision

Support two explicit installation modes:

- `selected` is the secure default. Helm creates one Role and RoleBinding in each validated
  namespace. An empty list resolves to the release namespace.
- `all` is an explicit opt-in. Helm creates a ClusterRole and ClusterRoleBinding, and KubeQueue
  applies configurable namespace exclusions. System namespaces and the KubeQueue release namespace
  are excluded by default.

The API exposes the effective mode, namespace scope, and authorization health. It rejects
submissions to namespaces outside the effective scope before creating durable intent.

Helm remains the source of truth for RBAC-changing scope configuration. Effective configuration is
mounted through a ConfigMap whose checksum triggers an automatic worker rollout. Runtime mutation
of namespace authority is not part of this decision.

Records from a namespace removed from scope remain visible as out of scope, do not consume
concurrency, and cannot receive lifecycle commands until authority is restored or the record is
archived.

## Consequences

- Secure installations retain least-privilege namespace-local RBAC.
- Operators wanting install-once cluster discovery can explicitly accept cluster-wide Job
  authority.
- KubeQueue cannot silently make a selected-mode namespace manageable; a Helm change is required.
- Cluster mode increases the impact of a compromised worker and requires prominent installation
  warnings.
- Namespace configuration changes restart the worker automatically but do not require a manual
  rollout.
- Namespace-label discovery is deferred because it still requires broad cluster authority while
  adding informer lifecycle complexity.
