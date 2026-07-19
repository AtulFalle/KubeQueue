# ADR 0012: Policy, scheduling, and scale envelope

- Status: Accepted
- Date: 2026-07-19

## Context

Project delegation requires enforceable quotas and fair access to shared capacity. The GA API and
scheduler also need explicit bounds so policy checks, inventory reads, event delivery, and retained
history cannot consume unbounded resources.

## Decision

Policy is versioned at installation, project, and namespace scope, with the narrower scope allowed
to constrain but not expand its parent. The GA policy set is limited to concurrency, queued and
retained Job counts, priority range and default, delayed-start horizon, lifecycle actions, supported
execution duration, and an optional admission-time image-registry allowlist. General Kubernetes
admission policy remains outside KubeQueue.

Quota checks and reservations are transactional with intent creation and admission. Usage is
released through idempotent completion, cancellation, and recovery paths. A rejection identifies
the policy version and scope, current usage, limit, stable reason, and remediation. CPU, GPU, cost,
and budget quotas remain deferred until they can use the same transactional reservation model.

Scheduling uses weighted fair sharing among projects with positive weights. Projects with eligible
work receive bounded opportunities without starvation; priority and aging order work within each
project's share. Emergency global priority is restricted to installation administrators. Every
admission records the policy version and scheduling basis. Project queue operations may reorder only
that project's relative subsequence; global ordering remains installation-administrator authority.

The initial GA reference envelope is 10,000 active or recently retained Jobs, 100,000 historical
Jobs, 100 managed namespaces, 50 Kubernetes Job events per second, and at least 100 concurrent
dashboard or API sessions. Published limits may change only to reflect measured release evidence.

Collection APIs use cursor pagination with bounded default and maximum page sizes, allowlisted
sorting, bounded normalized search, and indexed scope and state filters. Facets are separate bounded
queries. Event streams deliver scoped changes or invalidations rather than full-inventory snapshots.
Retention is explicit for workload history, audit, sessions, and operational data; no request,
reconciliation pass, or stream loads unbounded history into memory.

A single fenced mutation leader is the GA topology. If sustained testing cannot meet the published
envelope, deterministic project or namespace sharding requires a new ADR; sharding is not introduced
preemptively.

## Consequences

- Concurrent requests cannot bypass quota by racing admission or intent creation.
- Fairness is defined across projects while preserving useful priority inside each project.
- Clients must follow pagination and event cursors instead of assuming complete snapshots.
- Capacity and retention claims require sustained load evidence before GA.
- More expressive resource accounting and horizontal mutation sharding remain explicit future
  decisions.
