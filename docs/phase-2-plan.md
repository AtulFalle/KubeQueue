# Phase 2 plan: reliable automatic integration

## Goal

KubeQueue should be install-once infrastructure for existing Kubernetes batch applications. After
installation, an administrator should be able to understand which Jobs are managed, why a Job is
missing, and whether an action has converged without inspecting Pods, restarting the worker, or
changing application code.

Phase 2 is complete when:

- eligible Jobs are discovered across the chosen cluster scope;
- internal and explicitly ignored Jobs are never adopted;
- one failing Job or namespace cannot block unrelated reconciliation;
- lifecycle actions expose pending, successful, blocked, and failed outcomes;
- install, upgrade, and failure behavior is covered by a real Kubernetes test.

## Product principles

1. Kubernetes remains authoritative for observed execution state.
2. PostgreSQL remains authoritative for desired state, history, and reconciliation status.
3. Broad Kubernetes permissions are explicit, never silently inferred.
4. Observing a Job does not imply that KubeQueue can safely control it.
5. The UI must not describe inventory as live when worker health is unknown.
6. Deployment-owned settings remain Helm-managed until stronger authentication and authorization
   exist.

## Scope

### Included

- Failure-isolated reconciliation.
- Explicit workload ownership and synchronization state.
- Selected-namespace and opt-in cluster-wide discovery.
- Internal workload and Helm hook exclusion.
- Worker health, heartbeat, and reconciliation diagnostics.
- Action convergence feedback.
- A dedicated, safe queue workflow.
- A read-only operational settings page.
- Helm install, upgrade, RBAC, and migration improvements.
- End-to-end kind validation through Nx.

### Deferred

- Multi-user identity and team RBAC.
- Public dashboard exposure.
- Bundled PostgreSQL.
- Runtime editing of cluster permissions or namespace scope.
- Logs and metrics analytics.
- Recurring schedules.
- Kueue integration and preemption.
- Advanced manifest editing.

## Required architecture decisions

The required decisions are recorded in:

1. [ADR 0003: Namespace authority and discovery](adr/0003-namespace-authority-and-discovery.md).
2. [ADR 0004: Workload ownership and adoption](adr/0004-workload-ownership-and-adoption.md).
3. [ADR 0006: Lifecycle and synchronization state](adr/0006-lifecycle-and-synchronization-state.md).
4. [ADR 0007: Reconciliation consistency](adr/0007-reconciliation-consistency.md).
5. [ADR 0008: Migration ownership and compatibility](adr/0008-migration-ownership-and-compatibility.md).

## Milestone 1: reconciliation safety

This milestone is a release blocker and must land before additional controls.

### Control-plane changes

- Split namespace discovery, observation, command execution, and scheduling into independently
  recoverable phases.
- Process errors per namespace and per Job; record an error and continue with unrelated work.
- Classify transient and permanent failures and apply bounded exponential backoff.
- Always release scheduler claims after individual scheduling attempts.
- Exclude missing, stale, ignored, conflicted, and out-of-scope records from concurrency counts.
- Persist:
  - Kubernetes resource version;
  - `lastSeenAt` and `observedAt`;
  - last reconciliation error;
  - retry count and next retry time; and
  - worker heartbeat and last successful reconciliation.
- Prevent older Kubernetes observations from overwriting newer state.
- Represent a missing Kubernetes object as synchronization state, not `CANCELLED`.
- Treat deletion and not-found operations idempotently.
- Add an archive/forget operation for stale records that does not require Kubernetes access.

### Workload classification

Every durable Job receives one management mode:

- `MANAGED`: KubeQueue owns scheduling and lifecycle.
- `OBSERVED`: visible, but not safely interceptable or controllable.
- `IGNORED`: excluded by system policy or explicit annotation.
- `CONFLICTED`: Kubernetes identity conflicts with the durable association.

Defensive adoption rules:

- Ignore Jobs annotated `kubequeue.io/ignore: "true"`.
- Ignore Helm hooks and KubeQueue internal workloads.
- Ignore configured system namespaces.
- Validate namespace, name, UID, and durable ID before trusting `kubequeue.io/job-id`.
- Preserve CronJob-owned Jobs unless another exclusion applies.

### Acceptance criteria

- A forbidden or malformed Job cannot block other Jobs.
- A failing namespace cannot block healthy namespaces.
- KubeQueue's migration Job never appears in product inventory.
- A stale record cannot consume concurrency.
- A removed Job appears as missing instead of user-cancelled.
- Observed state cannot regress from an older resource version.

## Milestone 2: discovery and RBAC

### Selected mode

Selected mode remains the secure default:

```yaml
watch:
  mode: selected
  namespaces:
    - default
    - batch-jobs
```

- Generate one Role and RoleBinding per namespace.
- Normalize, deduplicate, and validate namespace names.
- Reject API submissions to unmanaged or inaccessible namespaces before persistence.
- Keep records from removed namespaces visible but unmanaged.

### Cluster mode

Cluster mode provides the low-configuration integration path:

```yaml
watch:
  mode: all
  excludedNamespaces:
    - kube-system
    - kube-public
    - kube-node-lease
    - kubequeue
```

- Require explicit ClusterRole consent.
- Display a prominent permission warning in Helm notes.
- Apply namespace and workload exclusions defensively in both listing and reconciliation.

### Helm configuration

- Replace comma-separated namespace configuration with a validated array.
- Separate `rbac.create` from `serviceAccount.create`.
- Add `imagePullSecrets`.
- Put effective worker configuration in a ConfigMap.
- Add a ConfigMap checksum to the worker Pod template so Helm changes trigger an automatic rollout.
- Do not require or document manual `kubectl rollout restart` steps.

### Acceptance criteria

- A user can choose selected or cluster mode during installation.
- Effective scope and RBAC health are visible through the API.
- Namespace changes through Helm automatically roll out the worker.
- Submissions outside effective scope return an actionable error.

## Milestone 3: operational status and convergence

Update OpenAPI before implementing these public behaviors.

### System status

Add `GET /api/v1/system/status` returning:

- API and database readiness;
- worker state: `ready`, `degraded`, or `unavailable`;
- heartbeat and last successful reconciliation;
- watch mode and effective namespaces;
- per-namespace informer and authorization status;
- global and per-namespace concurrency;
- release version; and
- sanitized active errors.

Worker readiness becomes true only after database connectivity, Kubernetes authorization, and
informer cache synchronization.

### Job synchronization fields

Extend Job responses with:

- `managementMode`;
- `syncStatus`;
- `actionPending`;
- `observedReason`;
- `observedMessage`;
- `observedAt`; and
- `lastError`.

Lifecycle commands communicate accepted desired intent separately from Kubernetes convergence.
Keep lifecycle state separate from synchronization state to avoid breaking existing clients with
additional lifecycle enum values.

### Acceptance criteria

- The API can distinguish a healthy empty queue from an unavailable worker.
- Every requested action is visibly pending until observed state converges.
- Kubernetes failure reasons are retained and exposed without leaking manifests or credentials.
- Stalled actions provide a stable error code and remediation.

## Milestone 4: dashboard workflows

### Inventory

- Replace the unconditional “Live inventory” indicator with actual system status.
- Keep global summary counts independent of active filters.
- Populate namespace and team selectors from API facets.
- Distinguish:
  - no Jobs;
  - no filter matches;
  - incomplete inventory;
  - unmanaged namespace; and
  - worker unavailable.
- Show `MANAGED`, `OBSERVED`, and `CONFLICTED` badges.
- Debounce filter requests and prevent stale responses from replacing newer results.

### Queue

Replace the `/queue` redirect with a dedicated global queue page:

- always operate on the complete queued set;
- reject incomplete or stale reorder requests;
- retain keyboard-accessible up/down controls;
- disable concurrent reorder operations;
- announce saved positions and conflicts; and
- expose priority and delayed-start editing with explicit feedback.

### Lifecycle actions

- Apply the command response to local state immediately.
- Show `Pausing`, `Resuming`, and `Terminating` during desired/observed divergence.
- Disable repeated and conflicting actions.
- Resolve pending state through SSE or refresh.
- Show a timeout with a link to operational status instead of suggesting a restart.
- Use an accessible confirmation dialog only for destructive termination.

### Submission

- Select from ready, managed namespaces instead of accepting arbitrary free text.
- Disable submission when no namespace is ready.
- Keep raw Job JSON for the MVP, with field-level and manifest validation.
- Explain whether the resulting Job will be managed or observed.

### Settings

Add a read-only `/settings` page showing:

- API, database, and worker health;
- watch mode and namespace readiness;
- concurrency configuration;
- release version;
- last reconciliation; and
- sanitized errors with copyable Helm remediation.

Do not expose Secrets or add mutable cluster settings in this phase.

## Milestone 5: delivery and operational hardening

### Helm

- Mark migration Jobs `kubequeue.io/ignore=true`.
- Add migration connection and active deadlines.
- Retain failed migration Pods for diagnosis and remove successful hooks.
- Add worker liveness and readiness probes.
- Enable ingress isolation by default for new installations.
- Keep egress policy opt-in until portable database and Kubernetes API rules are defined.
- Keep PostgreSQL external.
- Add a Helm test covering API, web, worker, database schema, and RBAC readiness.
- Add concise installation, migration, RBAC, and upgrade diagnostics to Helm notes.

### Migrations

- Introduce a dedicated migration command.
- Make API and worker verify schema compatibility instead of applying migrations at startup.
- Add migration checksums and dirty-state detection.
- Require expand/contract compatibility across rolling upgrades.
- Add a repair migration for previously adopted internal Jobs and stale cancellation records.

### Automated validation

Add Nx targets for:

- Helm validation across selected and cluster modes;
- kind installation using the packaged chart;
- previous-release-to-candidate upgrade;
- Helm tests; and
- browser lifecycle tests against the kind deployment.

The kind suite must verify:

1. migration and internal Job exclusion;
2. multi-namespace selected discovery;
3. explicit cluster discovery and system exclusions;
4. RBAC failure isolation;
5. namespace removal and stale records;
6. worker restart and missed-event recovery;
7. pause, resume, terminate, retry, and reorder convergence;
8. PostgreSQL data preservation across upgrade; and
9. external PostgreSQL and manually created Secret retention after uninstall.

Release validation must run the worker and packaged Helm chart in Kubernetes rather than only
smoke-testing API and web containers.

## Delivery order

1. Architecture decisions and OpenAPI changes.
2. Reconciliation isolation and synchronization persistence.
3. Workload classification and internal exclusions.
4. Namespace/RBAC modes and submission validation.
5. Worker status and readiness.
6. Inventory, queue, action, and settings UX.
7. Helm install/upgrade tests and release enforcement.

Each milestone should be independently releasable and must include its lowest-level tests plus a
kind integration test for changed Kubernetes boundaries.

Enterprise identity, authorization, audit, high availability, and general-availability requirements
are planned separately in [`phase-3-plan.md`](phase-3-plan.md).
