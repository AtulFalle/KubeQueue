# Architecture

## System boundaries

KubeQueue is split into three runtime processes built from two applications:

- `web`: Next.js user interface. It consumes only the generated API client.
- `api`: Gin HTTP process. It validates commands and invokes application use cases.
- `worker`: Go process. It schedules work and reconciles desired state with Kubernetes.

The API and worker share one Go module and domain model but have separate composition roots and
deployments. They communicate through durable state rather than in-memory calls.

## Dependency direction

```text
apps/web -> packages/api-client -> packages/api-contract

cmd -> platform -> application -> domain
                   application -> ports <- adapters
```

The domain is pure Go. It cannot import Gin, SQL drivers, Kubernetes clients, or generated
transport types. Interfaces are defined by the package that consumes them.

## Control-plane package ownership

- `internal/domain`: entities, value objects, lifecycle policy, domain errors.
- `internal/application`: use cases and transaction orchestration.
- `internal/ports`: narrow interfaces required by application code.
- `internal/adapters/persistence`: PostgreSQL and SQLite implementations.
- `internal/adapters/kubernetes`: Kubernetes reads, watches, and commands.
- `internal/scheduler`: queue admission and ordering.
- `internal/reconciler`: convergence of desired and observed state.
- `internal/platform`: process composition, HTTP server, configuration, and lifecycle.

## Source-of-truth rules

- PostgreSQL is the production control-plane store; SQLite is limited to single-process local use.
- Kubernetes is the source of truth for observed execution state.
- Durable control-plane records are the source of truth for user intent and history.
- Desired and observed state are stored separately and converged idempotently.
- Standard Kubernetes Jobs are managed directly; no custom resource is introduced in Phase 1.

## Scheduling and recovery consistency

The worker uses client-go shared informers for Job and Pod events in every configured namespace.
Informer caches are synchronized before reconciliation starts; a periodic recovery pass covers
missed or coalesced events. Managed Kubernetes Jobs are created with `spec.suspend=true`. The
worker records the Kubernetes UID durably before removing suspension, so work cannot begin before
the control-plane association exists.

Production scheduling combines a renewable PostgreSQL scheduler lease with expiring per-job
claims. Eligible rows are selected with row locks and `SKIP LOCKED`, and claims survive process
failure only until their expiry. Queue ordering has its own monotonic version in
`control_plane_metadata`; entity versions are not reused as a queue-wide concurrency token.
SQLite uses the same schema and transactions but remains limited to one worker.

Schema changes are ordered embedded migrations. PostgreSQL migration execution takes an advisory
lock, and the Helm chart runs a dedicated migration hook before API and worker upgrades. Runtime
processes verify migration checksums and schema compatibility at startup but never apply migrations.

## Contract and delivery

OpenAPI is the public contract. Go handlers and the generated TypeScript client follow it;
Kubernetes API objects are never exposed as the product API. CI regenerates the client and fails
when generated output drifts from the contract.

Helm is the deployment source of truth. kind and Tilt provide a local loop over the same images
and chart. Nx is the task entry point for local development and CI.

Release tags are the source of truth for published versions. Private workspace package versions do
not track product releases. The release workflow accepts only an exact `master` commit with a
successful post-merge CI run, builds commit-addressed images once, validates them, and promotes the
same manifests to semantic-version tags. Helm chart metadata is injected from that release tag at
package time.

Docker Compose is the zero-install application loop. It runs PostgreSQL, API, worker, web, Swagger
UI, and Adminer together. Development Dockerfiles use Air and Next.js hot reload; Compose Watch
synchronizes source and rebuilds images only when dependency manifests change. The web proxies
same-origin `/api/*` requests to the API service so browser code does not depend on container DNS.

Significant changes to these decisions require an ADR under `docs/adr`.

## Phase 1 lifecycle

Each record stores user intent independently from the state reported by Kubernetes:

```text
CREATED -> QUEUED -> RUNNING -> COMPLETED
              |         |
              v         v
            PAUSED    PAUSED
              |         |
              +-----> RUNNING

Any non-terminal state -> CANCELLED
RUNNING -> FAILED -> QUEUED (retry creates a new attempt)
```

Commands are idempotent. Queue order is `priority DESC, position ASC, created_at ASC`; a one-time
`scheduledFor` timestamp makes a queued job ineligible until that instant. The worker enforces
global and per-namespace concurrency limits.

New jobs are persisted before Kubernetes objects are created and use the
`kubequeue.io/job-id` label. Jobs discovered in configured namespaces are automatically adopted,
including a sanitized snapshot of their specification. Already-running adopted jobs are visible
and controllable, but cannot be retroactively ordered.

Pausing a queued job changes only durable intent. Pausing a running job sets `spec.suspend=true`;
Kubernetes terminates active Pods and resumes with new Pods when suspension is removed. Termination
deletes the Kubernetes Job while KubeQueue retains its history. Retry always creates a new attempt
from the stored template.

## Phase 2 reconciliation foundation

Discovery no longer implies lifecycle ownership. API-created Jobs carry an explicit management
marker and remain `MANAGED`; unmarked external Jobs are `OBSERVED` and cannot receive lifecycle or
queue mutations. Ignored workloads, Helm hooks, and KubeQueue internal Jobs are excluded before
adoption. Claimed durable identities are validated against namespace, name, and UID instead of
being rebound silently.

Durable synchronization state is separate from lifecycle state. Missing Kubernetes objects retain
their last observed lifecycle and become `MISSING`; stale, conflicted, archived, and out-of-scope
records do not consume scheduling concurrency. Kubernetes resource versions are stored as opaque
compare-and-set tokens. Public Job responses expose bounded synchronization status, pending intent,
sanitized observations, and stable reconciliation diagnostics without exposing resource versions
or retry internals.

Namespace authority is explicit. Selected mode is the least-privilege default and grants one
Role/RoleBinding per configured namespace. All-namespace mode requires explicit cluster-wide RBAC
consent and always excludes Kubernetes system namespaces plus the KubeQueue release namespace.
Helm renders the effective scope into a checksummed ConfigMap consumed by both API and worker. The
API rejects submissions outside that scope before durable intent is created.

The worker publishes a durable heartbeat, last successful reconciliation, effective scope,
informer synchronization, Kubernetes authorization, concurrency, release, and sanitized error
status. Its readiness probe remains false until database access, informer synchronization, and
required Job permissions are healthy. `GET /api/v1/system/status` exposes this bounded operational
view so clients can distinguish an empty queue from unavailable or incomplete inventory.

## Phase 1 trust boundary

The API is intended for cluster-internal or otherwise trusted networks and supports a single
deployment-wide bearer token. Kubernetes access is limited to configured namespaces through the
worker service account. Multi-user identity, team RBAC, quotas, logs, metrics insights, recurring
schedules, Kueue, and preemption are deferred.
