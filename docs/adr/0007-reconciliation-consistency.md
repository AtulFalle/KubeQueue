# ADR 0007: Reconciliation consistency

- Status: Accepted
- Date: 2026-07-19

## Context

The API and worker coordinate through PostgreSQL, and multiple worker replicas are supported by a
scheduler lease and per-Job claims. Observation and lifecycle command execution are not protected
by the scheduler lease, however, so workers can issue duplicate Kubernetes mutations or persist
stale informer observations. Phase 1 reconciliation also stops on the first error, allowing one
inaccessible Job to block unrelated scheduling.

KubeQueue needs deterministic mutation ownership while preserving fast failover and idempotent
recovery.

## Decision

Use a renewable PostgreSQL reconciliation-leader lease for Kubernetes mutations and scheduling.
Only the lease holder may execute desired-state commands, create or unsuspend Jobs, delete Jobs, or
admit queue entries.

All worker replicas may maintain informer caches and publish heartbeats. Observation persistence is
allowed from any healthy worker only through Kubernetes resource-version-aware compare-and-set.
This protects handoff periods and prevents an older cache from regressing durable state.

Reconciliation is divided into independent namespace discovery, observation, command, and
scheduling phases. Errors are handled per namespace and per Job with bounded retry metadata; they
do not terminate processing of unrelated records. Scheduler claims are released in guaranteed
cleanup, while expiry remains the crash-recovery fallback.

The PostgreSQL lease is used instead of a Kubernetes Lease so coordination remains in the existing
durable boundary and does not add cluster-scoped coordination RBAC. All Kubernetes commands remain
idempotent and treat already-applied and not-found outcomes as convergence where appropriate.

## Consequences

- At most one healthy worker intentionally mutates Kubernetes at a time.
- Worker failover is delayed by the bounded lease expiry but requires no manual intervention.
- Informer processing can remain warm on followers, reducing failover recovery time.
- Observation writes require compare-and-set support in every persistence adapter.
- Per-Job failures become visible degradation rather than global reconciliation failure.
- PostgreSQL availability remains required for scheduling and mutation, consistent with the
  production architecture.
- Lease and claim behavior needs multi-worker PostgreSQL integration and failure-injection tests.
