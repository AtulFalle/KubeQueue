# ADR 0013: High availability, recovery, and compatibility

- Status: Accepted
- Date: 2026-07-19

## Context

ADR 0007 selects PostgreSQL lease-based reconciliation leadership, and ADR 0008 defines migration
ownership. GA requires those decisions to cover stale leaders, recovery ownership, and compatibility
across the API, chart, database, and customer-operated dependencies.

## Decision

This decision extends ADR 0007. API and web processes are active across at least two replicas.
Worker replicas keep warm informer caches, but one PostgreSQL-backed mutation leader performs
scheduling and Kubernetes writes. Every leadership acquisition receives a monotonically increasing
fencing generation. Mutation authorization and durable claims are conditional on the current
generation, and a worker revalidates leadership immediately before a Kubernetes write. Lease loss
cancels mutation work and removes mutation readiness.

Kubernetes commands remain idempotent and use stable identities and resource preconditions where
the Kubernetes API supports them. An expired generation cannot acquire or complete new durable
mutation intent. A command already accepted by Kubernetes may be observed only after failover, so
the new leader reconciles observed state before retrying. KubeQueue guarantees no intentional
concurrent mutation and no duplicate control-plane intent; it does not claim exactly-once
Kubernetes Job execution. Target leader failover is within 15 seconds.

PostgreSQL remains customer-operated. The customer owns database availability, encryption, full
backups, WAL retention and point-in-time recovery, backup storage, and restore execution. KubeQueue
publishes supported versions and provider-neutral requirements, emits release, schema, migration
checksum, and backup timestamps as verification metadata, and supplies preflight and isolated
restore verification for control-plane records. Reference objectives are an RPO no greater than 15
minutes and an RTO no greater than two hours; customers must operate infrastructure capable of
meeting them. Restore drills are required quarterly and before GA.

Compatibility is declared and tested as a release matrix:

- `/api/v1` follows documented deprecation and sunset rules, with automated OpenAPI breaking-change
  detection;
- each release lists supported Kubernetes and PostgreSQL versions;
- the Helm chart, application images, and migration command are released as one tested set;
- upgrades cover the previous published chart and schema; and
- schema changes follow the expand/contract window in ADR 0008, with application rollback supported
  only while the resulting schema remains in the declared range.

Helm owns deployment configuration, Kubernetes RBAC, workload identity, Secrets references, and
upgrade ordering. KubeQueue owns product-state compatibility checks and fails readiness rather than
mutating against an unsupported schema or unavailable PostgreSQL database. Restores reconnect to
Kubernetes only after identity, queue, lease, claim, policy, audit, and workload association checks
pass, preventing blind replay against already-running Jobs.

## Consequences

- PostgreSQL is both the consistency boundary and a required dependency for safe mutation.
- Warm followers improve failover time but do not increase mutation throughput.
- Recovery objectives are shared commitments: KubeQueue provides metadata and verification while
  the customer provides and operates backup infrastructure.
- Application rollback cannot reverse schema changes and is bounded by the published compatibility
  window.
- Multi-region active/active operation and automatic cloud-provider backup orchestration remain out
  of scope.
