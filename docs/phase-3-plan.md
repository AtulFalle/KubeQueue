# Phase 3 plan: enterprise readiness and general availability

## Goal

Phase 3 turns KubeQueue from a trusted single-administrator preview into a secure, installation-
scoped, auditable, supportable enterprise control plane. It is the final planned development phase
before a `1.0.0` general-availability release, not the end of maintenance or product evolution.

An enterprise installation must provide:

- secure first-time setup without a shared browser administrator token;
- human and workload identity through standard protocols;
- deny-by-default authorization at installation and project scope;
- configurable roles, quotas, policy, and fair scheduling;
- immutable audit history attributable to a principal;
- high availability with bounded failover and no duplicate mutations;
- published capacity, compatibility, recovery, and support commitments; and
- evidence-backed security and release gates.

## Entry criteria

Phase 3 must not begin until the Phase 2 acceptance criteria in
[`phase-2-plan.md`](phase-2-plan.md) pass against the packaged Helm chart.

Required foundations include:

- failure-isolated reconciliation;
- explicit workload ownership and synchronization state;
- selected and cluster namespace authority modes;
- worker heartbeat, readiness, and system status;
- dedicated migration ownership and schema compatibility checks; and
- automated clean-install and previous-release upgrade tests in kind.

Enterprise policy cannot be layered safely over ambiguous workload ownership, stale observations,
or unbounded reconciliation failure.

## On-premise deployment boundary

KubeQueue is an on-premise control-plane wrapper installed into a customer's Kubernetes
environment. It is not a SaaS control plane and does not depend on a KubeQueue-hosted service.
Each installation manages its local Kubernetes authority and stores product state in the customer's
PostgreSQL database.

The installation is the top-level identity, policy, and audit boundary. Projects provide delegated
separation inside that installation; a tenant or organization abstraction is intentionally not
introduced.

Durable enterprise resources:

- **Project:** delegated administration, workload ownership, quota, and scheduling boundary.
- **Namespace binding:** maps one Kubernetes namespace to exactly one project.
- **Team:** installation-managed principal grouping, not a free-text Job label.
- **Principal:** a human user or service account.
- **Role definition:** a named set of stable permissions.
- **Role binding:** assigns a role to a principal or team at installation or project scope.
- **Policy and quota:** versioned rules enforced transactionally.
- **Audit event:** immutable security and administrative history.

Jobs gain immutable project, namespace-binding, creator, and submission-source references. Existing
`team` text is migrated as legacy display metadata and never grants access.

Effective authority is always the intersection of:

1. authenticated principal permissions;
2. role-binding resource scope;
3. project and namespace ownership;
4. Helm-managed Kubernetes authority; and
5. workload management mode.

Application authorization can never expand Kubernetes RBAC.

All users, groups, sessions, service accounts, credentials, policies, workload metadata, and audit
events remain inside the customer's PostgreSQL database and Kubernetes Secrets. KubeQueue sends no
telemetry or workload data outside the environment by default. Outbound access is limited to
explicitly configured identity-provider/JWKS endpoints, image registries, and customer-owned
telemetry exporters. Diagnostic bundles are generated locally and shared only by an administrator.

## Product operating model

KubeQueue follows the self-hosted infrastructure-product model used by tools such as Vault,
Prometheus, Grafana, Kibana, and Kueue:

- customers install and operate KubeQueue in their own Kubernetes environment;
- Helm and GitOps own deployment configuration, Kubernetes permissions, Secrets, upgrades, and
  rollback;
- the KubeQueue UI and API own workload operations, users, project access, queue policy, quota, and
  audit;
- all runtime dependencies use customer-controlled endpoints;
- product health and support information is available locally; and
- no KubeQueue cloud account, control plane, license callback, or hosted data path is required.

KubeQueue integrates with existing enterprise infrastructure instead of bundling it:

- Prometheus scrapes KubeQueue metrics;
- Grafana uses published dashboards;
- OpenTelemetry exports traces to the customer's collector;
- structured logs flow to Loki, Elasticsearch/Kibana, or another customer-selected backend;
- Vault, External Secrets, or a CSI driver supplies referenced Kubernetes Secrets;
- an external PostgreSQL service provides durable state; and
- the customer's OIDC provider supplies human and workload identity.

These integrations are optional adapters around a fully functional local installation. KubeQueue
does not install or operate Prometheus, Grafana, Kibana, Vault, an identity provider, or
PostgreSQL.

Kueue interoperability is a separate compatibility decision. KubeQueue must not attempt to control
the same Job concurrently with Kueue. A future adapter may observe or delegate admission to Kueue,
but each namespace or workload must have one authoritative queue controller.

## Milestone 1: identity and secure bootstrap

### OIDC-native authentication

Use OpenID Connect as the native enterprise identity protocol:

- Authorization Code flow with PKCE.
- API validates access tokens, never ID tokens.
- Strict issuer, audience, algorithm, signature, expiry, not-before, and authorized-party checks.
- Identity keys use issuer plus immutable subject; email remains mutable display data.
- Bounded JWKS caching and key-rotation support.
- Administrator-configured claim and group mappings.
- Just-in-time provisioning only when an explicit domain or group mapping grants access.

Do not implement a native SAML service provider. Enterprises needing SAML use an OIDC broker such
as Keycloak, Dex, Authentik, Okta, or Entra federation. SCIM provisioning is a post-GA integration;
OIDC group synchronization is sufficient for GA.

### Browser trust boundary

Turn the Next.js process into an authenticated backend-for-frontend:

- OAuth credentials and sessions remain server-side.
- Browser receives only a `Secure`, `HttpOnly`, `SameSite=Lax`,
  `__Host-kubequeue-session` cookie.
- Sessions are stored in PostgreSQL for multi-replica revocation.
- Session IDs rotate after login and privilege changes.
- Idle and absolute lifetimes are configurable.
- Refresh tokens rotate and are encrypted using a key outside PostgreSQL.
- Mutating browser requests require a session-bound CSRF token and strict `Origin` validation.
- API authenticates and authorizes every proxied request independently.

The web process no longer receives or injects a deployment-wide administrator token.

### First-time setup

Add a guarded `/setup` workflow:

1. Validate API, database, schema, worker, Kubernetes authority, release, and public URL.
2. Claim setup with a one-time 256-bit token supplied through an existing Kubernetes Secret.
3. Validate OIDC discovery, JWKS, redirect URI, audience, and an actual login round trip.
4. Create the installation identity and first owner transactionally.
5. Create the initial project and namespace bindings.
6. Configure initial admission policy, concurrency, and quota.
7. Require a successful owner login.
8. Revoke bootstrap permanently and display the recovery checklist.

Only a keyed token digest and safe prefix are stored. Bootstrap is available only while no owner
exists, and concurrent claims have exactly one winner.

### Break-glass access

Keep one local recovery mechanism instead of a local password system:

- disabled after setup;
- re-enabled only through an explicit Helm value and referenced Secret;
- time-limited, rate-limited, and fully audited;
- rotated with bounded credential overlap; and
- unavailable through an empty-token or implicit development bypass.

Development bypass requires an explicit development mode and loopback binding.

### Acceptance criteria

- Production startup fails closed when authentication is unconfigured.
- Invalid issuer, audience, signature, algorithm, expiry, or revoked credentials are rejected.
- Bootstrap claim is race-safe, single-use, and cannot be reactivated through the UI.
- OIDC state, nonce, PKCE, redirect, session fixation, logout, and expiry tests pass.
- Cross-origin cookie mutations fail CSRF validation.
- Role changes and principal disablement revoke active sessions promptly.

## Milestone 2: centralized authorization

### Permission model

Authorization uses stable permissions enforced in `internal/application`, with repository scope as
defense in depth. Examples include:

- `jobs.list`, `jobs.read`, `jobs.manifest.read`;
- `jobs.submit`, `jobs.pause`, `jobs.resume`, `jobs.terminate`, `jobs.retry`;
- `jobs.take-control`, `jobs.archive`;
- `queue.entry.update`, `queue.project.reorder`, `queue.global.reorder`;
- `projects.manage`, `namespace-bindings.manage`;
- `members.read`, `members.manage`;
- `roles.read`, `roles.assign`, `roles.define`;
- `service-accounts.manage`, `tokens.manage`;
- `policies.read`, `policies.manage`, `quotas.manage`;
- `audit.read`, `audit.export`; and
- `system.status.read`, `support.diagnostics.read`.

UI visibility is convenience only; every API, event stream, and object lookup enforces the same
policy. Cross-project object lookup returns `404` where needed to prevent enumeration.

### Built-in roles

- **Installation Owner:** identity configuration, owner recovery, and unrestricted installation
  administration. The final owner cannot be removed.
- **Installation Administrator:** projects, teams, principals, namespace bindings, policy, quota,
  and service-account administration.
- **Project Administrator:** membership, namespace requests, policies, quotas, and workloads inside
  assigned projects.
- **Operator:** submit and control managed workloads and reorder the project queue.
- **Submitter:** submit and view owned project workloads without lifecycle administration.
- **Viewer:** read permitted project inventory and status.
- **Auditor:** read audit and sanitized support diagnostics without workload mutation.

Global queue ordering remains installation-administrator authority. Project ordering can only change
the project's relative subsequence and must preserve other projects' entries.

### Custom roles

Support installation-defined roles built from the stable permission catalog:

- custom roles are installation or project scoped;
- creators cannot delegate permissions they do not hold;
- system bootstrap, owner recovery, and identity-provider permissions remain non-delegable;
- permission changes are versioned and invalidate affected sessions; and
- effective access shows inherited and direct grants.

### Service accounts and API credentials

Service accounts are non-interactive principals owned by the installation or a project. Native tokens
are opaque random credentials:

- shown once;
- stored as prefix plus keyed digest;
- expiry required by default;
- scoped no broader than the creator's delegable permissions;
- rotatable with short overlap;
- immediately revocable; and
- tracked with creator, last-used time, and audit identity.

Also accept OIDC client-credentials access tokens for enterprises with workload identity
infrastructure.

### Acceptance criteria

- An authorization matrix covers every route, permission, role, and scope.
- Guessed identifiers cannot cross project boundaries through REST or event streams.
- Kubernetes labels cannot create membership or grants.
- A principal cannot delegate authority it does not possess.
- The last installation owner is protected.
- Service-account tokens support one-time display, expiry, rotation, and revocation.

## Milestone 3: project policy, quota, and fair scheduling

### Namespace ownership

- A namespace belongs to at most one project.
- Namespace binding records desired scope separately from effective RBAC and informer readiness.
- UI configuration cannot claim success until Kubernetes authority is observed.
- Reassignment requires no active managed workload or an explicit migration workflow.

### GA policy set

Keep policy bounded and enforceable:

- global, project, and namespace concurrency;
- maximum queued and retained Jobs;
- permitted priority range and default;
- maximum delayed-start horizon;
- allowed lifecycle actions by role;
- maximum execution duration where Kubernetes can enforce it; and
- optional image-registry allowlist at admission time.

Do not build a general-purpose OPA replacement. Pod security, network, and broad Kubernetes policy
remain Kubernetes admission concerns.

### Quotas

Quota checks and reservations occur transactionally with intent creation and admission. Rejections
return:

- policy and scope;
- current usage;
- configured limit;
- stable reason code; and
- permitted remediation.

CPU, GPU, cost, and budget quotas are deferred unless KubeQueue can reserve and release those
resources transactionally.

### Fair scheduling

Replace purely global priority ordering with weighted fair sharing between projects:

- each project has a positive weight;
- capacity is shared without starvation;
- priority and aging order work within each project's share;
- global emergency priority is restricted to installation administrators; and
- decisions record the applied policy version.

### Acceptance criteria

- Concurrent submissions cannot exceed queue or concurrency quotas.
- A continuously busy project cannot starve another positive-weight project.
- Project queue operations never move another project's entries.
- Every admission decision is attributable to a policy version.
- Quota usage recovers correctly after failure, cancellation, and worker failover.

## Milestone 4: immutable audit and sensitive data

### Audit contract

Create append-only audit events separate from workload lifecycle events. Record:

- timestamp, event ID, request ID, and trace ID;
- principal, effective groups, session or token identity, and authentication method;
- action, target type and ID;
- installation, project, team, and namespace scope;
- authorization allow or deny decision;
- result and stable reason code;
- trustworthy source address and user agent; and
- redacted before and after summaries.

Administrative mutations write their audit event in the same database transaction. Authentication
failures and denied requests use an independent bounded writer. Audit records cannot be updated
through product APIs.

Provide cursor pagination, retention policy, legal-hold protection, and export. External SIEM or
object-storage sinks may be added after the durable audit contract exists.

### Sensitive workload data

Treat stored Job templates as sensitive:

- require `jobs.manifest.read` independently from Job metadata access;
- reject or warn on likely inline credentials and prefer Kubernetes Secret references;
- redact manifests structurally in UI, logs, audit summaries, and support bundles;
- never record bearer tokens, cookies, refresh tokens, client secrets, database URLs, or full
  environment values; and
- encrypt recoverable OAuth credentials using envelope encryption.

### Acceptance criteria

- Every mutation and denied administrative action identifies its actor.
- Audit retention and export preserve ordering and integrity.
- Audit, logs, API errors, metrics, and support bundles pass secret-leak tests.
- Job metadata can be viewed without granting manifest access.

## Milestone 5: bounded enterprise APIs

Finalize `/api/v1` before GA:

- make intended breaking changes while the product remains pre-1.0;
- add cursor pagination with default and maximum page sizes;
- use stable allowlisted sorting;
- normalize and bound search input;
- index project, state, synchronization, namespace, and time filters;
- expose facets separately from paginated data;
- replace full-inventory SSE snapshots with scoped change events or invalidations;
- add request IDs and structured error details;
- use `ETag` and `If-Match` for mutable administration resources;
- support idempotency keys for creation and credential rotation;
- publish deprecation and sunset policy; and
- run automated OpenAPI breaking-change checks.

Planned resource groups:

- setup, session, and current principal;
- installation configuration, projects, teams, users, and groups;
- service accounts and token lifecycle;
- roles and bindings;
- cluster installation and namespace bindings;
- policies, quotas, and usage;
- project-scoped Jobs and queues;
- audit events; and
- system status and support diagnostics.

No request or reconciliation path may load unbounded history into memory.

### Supported scale envelope

The initial GA reference envelope is:

- 10,000 active or recently retained Jobs;
- 100,000 historical Jobs;
- 100 managed namespaces;
- 50 Kubernetes Job events per second; and
- at least 100 concurrent dashboard/API sessions.

Load tests may adjust these numbers before GA, but published limits must reflect measured results.
Initial performance objectives:

- list API p95 below 500 ms at the reference envelope;
- read API p99 below one second;
- reconciliation lag p99 below 60 seconds; and
- lifecycle convergence p99 below 30 seconds when Kubernetes is not blocking progress.

If one mutation leader cannot meet the measured envelope, deterministic project or namespace
sharding requires a separate ADR.

## Milestone 6: high availability and recovery

### Runtime availability

- API and web support at least two replicas.
- At least two worker replicas maintain warm informer caches.
- Mutation leadership uses a fenced generation so an expired leader cannot continue mutating
  Kubernetes.
- Leader failover completes within 15 seconds.
- Add PodDisruptionBudgets, topology spread, affinity, tolerations, graceful termination, and
  startup/readiness/liveness probes.
- Publish database pool, timeout, retry, and lock configuration.

PostgreSQL remains externally operated. Publish supported HA and TLS requirements rather than
shipping a database operator.

### Backup and disaster recovery

- Declare supported PostgreSQL versions.
- Define provider-neutral full-backup and WAL/PITR requirements.
- Provide backup preflight, schema inspection, and restore verification commands.
- Record release, schema version, migration checksums, and timestamp with backup metadata.
- Restore into an isolated database and verify Jobs, attempts, events, queue state, leases, claims,
  policies, identities, and audit before reconnecting workloads.
- Automate a quarterly restore drill.
- Publish a cold-region recovery runbook.

Reference recovery objectives:

- RPO no greater than 15 minutes.
- RTO no greater than two hours.

### Acceptance criteria

- Killing the leader produces no duplicate Job creation, admission, suspension, or deletion.
- One API or web Pod can be drained without service interruption.
- PostgreSQL loss fails mutation readiness without corrupting intent.
- Backup, upgrade, restore, and application rollback are tested for every release.
- Disaster recovery does not duplicate already-running Kubernetes Jobs.

## Milestone 7: observability, security, and support

### Observability and SLOs

Expose Prometheus/OpenMetrics metrics and OpenTelemetry traces for:

- API traffic, latency, errors, and in-flight requests;
- database latency, pool saturation, lock waits, and errors;
- reconciliation duration, lag, retries, and failures;
- queue depth, admission decisions, and quota pressure;
- leader lease, generation, and failover;
- informer sync and watch reconnects;
- desired/observed divergence and stalled actions; and
- migration and schema compatibility.

Logs are structured JSON with request and trace correlation. Labels and fields remain bounded and
never contain credentials or full manifests.

GA SLO targets:

- API availability: 99.9% monthly.
- Read API latency: 99% below one second.
- Accepted action convergence: 99% below 30 seconds, excluding Kubernetes-declared blockers.
- Eligible Job admission: 99% below 60 seconds.

Provide fast- and slow-burn error-budget alerts plus reference dashboards and runbooks. KubeQueue
exports telemetry but does not operate Prometheus, Grafana, or a tracing backend.

### Network and secret lifecycle

- Require TLS at the external Ingress or Gateway boundary.
- Support PostgreSQL `verify-full` with a configurable CA Secret.
- Use separate API, web, worker, and migration service accounts.
- Disable Kubernetes token mounting for API and web.
- Default-deny ingress with component-specific rules.
- Keep portable egress policy opt-in until DNS, PostgreSQL, Kubernetes API, OIDC, and telemetry
  destinations are configured.
- Support External Secrets and CSI-mounted credentials.
- Rotate OIDC, API, database, and session credentials without manual Pod deletion.

### Supply-chain and vulnerability policy

- Preserve immutable build-once release promotion, provenance, SBOMs, and scanning.
- Pin CI actions by commit SHA.
- Sign images and OCI charts keylessly.
- Verify signatures and expected source provenance before deployment.
- Scan source, dependencies, images, manifests, licenses, and leaked secrets.
- Block fixable critical and high findings unless a documented, expiring exception is approved.
- Publish dependency and license notices.
- Add ownership rules for identity, migrations, OpenAPI, Helm, and release workflows.

### Supportability

Publish:

- production support lifecycle;
- security acknowledgement and remediation targets;
- compatibility and deprecation policy;
- release cadence;
- backup, rollback, and disaster-recovery runbooks;
- capacity limits and SLOs; and
- a sanitized diagnostic bundle containing versions, health, schema, leadership, namespace
  authorization, and recent error classes.

Do not claim SOC 2, ISO 27001, HIPAA, or similar certification. Provide auditable controls and
release evidence that customers can map into their own on-premise programs.

## Enterprise UI

Required pages and workflows:

- `/setup`: guarded bootstrap and enterprise readiness checklist.
- `/login`, callback, logout, access denied, and session expired.
- Overview: health, quota pressure, namespace authority, and recent administration.
- Jobs: authorized, paginated inventory with project scope.
- Queue: complete authorized queue with safe project and global operations.
- Projects: overview, namespaces, members, policies, and quotas.
- Access: users, teams, service accounts, custom roles, and effective role assignments.
- Namespaces: detected, desired, and effective authorization status.
- Audit: immutable search, filters, details, and export.
- System and support: health, versions, schema, leadership, certificates, active errors, and
  redacted diagnostics.

All pages support keyboard-only use, visible focus, semantic status, focus restoration, accessible
dialogs and tables, 200% text zoom, non-color indicators, and automated axe coverage for every role
and error state.

## Migration from the preview administrator token

Use an expand/contract transition:

1. Add identity and scope tables plus nullable Job ownership columns.
2. Backfill a default project and namespace bindings from Helm scope.
3. Preserve free-text team values only as legacy metadata.
4. Introduce a `legacy_admin` migration principal backed by the existing Secret.
5. Support OIDC/API credentials and the legacy token for one compatibility release.
6. Audit every legacy-token use and display a persistent deprecation warning.
7. Complete setup and verify an OIDC installation owner.
8. Revoke the legacy credential.
9. Remove normal legacy mode before `1.0.0`; retain only explicit, time-bound break-glass recovery.

Never silently convert the shared token into a personal user or browser session.

## Required architecture decisions

Record these before implementation:

1. OIDC-native identity and SAML brokering.
2. Installation, project, team, and namespace authorization hierarchy.
3. Browser BFF, sessions, CSRF, and API trust boundary.
4. Built-in and custom roles, provisioning, and delegation limits.
5. Service accounts, token storage, rotation, and revocation.
6. Bootstrap, break-glass, and legacy-token migration.
7. Immutable audit, redaction, retention, and export.
8. Quota hierarchy and weighted fair scheduling.
9. HA leadership fencing and split-brain behavior, extending ADR 0007.
10. Scale envelope, pagination, event delivery, retention, and sharding threshold.
11. Observability contract, SLOs, and error-budget policy.
12. Backup, restore, and disaster-recovery ownership.
13. TLS, secret lifecycle, and sensitive template disclosure.
14. API, Kubernetes, PostgreSQL, Helm, and schema compatibility policy.
15. Release signing, vulnerability exceptions, and support commitments.

## Delivery order

1. Complete every Phase 2 release blocker.
2. Record Phase 3 ADRs and threat model.
3. Reset the pre-GA OpenAPI contract and add bounded API conventions.
4. Add project, identity, namespace, and scope persistence with deterministic
   backfill.
5. Implement centralized authorization.
6. Add OIDC, BFF sessions, CSRF, bootstrap, and break-glass.
7. Add service accounts, token lifecycle, roles, and access administration.
8. Add project policy, transactional quota, and fair scheduling.
9. Add immutable audit and sensitive-data controls.
10. Build enterprise setup, project, access, namespace, audit, and support workflows.
11. Add HA fencing, scale, telemetry, backup/restore, and compatibility automation.
12. Complete external security review, load test, recovery drill, and GA documentation.

## GA release gates

Do not publish `1.0.0` unless:

- all Phase 2 packaged-chart acceptance tests pass;
- OIDC, sessions, CSRF, service accounts, RBAC, and project isolation pass adversarial tests;
- bootstrap and break-glass recovery are race-safe and audited;
- every mutation and denied administrative action has an attributable audit event;
- quotas and fairness pass concurrent and starvation tests;
- HA failover and split-brain tests show no duplicate Kubernetes mutations;
- the published scale envelope and SLOs pass sustained load tests;
- backup, restore, disaster recovery, and previous-release upgrade drills meet RPO/RTO;
- the Kubernetes, PostgreSQL, and Helm compatibility matrix passes;
- accessibility tests cover every critical workflow and role;
- threat modeling and independent security review have no unresolved critical or high findings;
- images and charts are signed and provenance verification passes;
- no unapproved fixable critical or high vulnerability or license violation remains; and
- support, security response, compatibility, deprecation, rollback, and recovery policies are
  published.

## Explicit exclusions

To keep GA achievable, Phase 3 does not include:

- bundled or KubeQueue-operated PostgreSQL;
- native SAML or SCIM services;
- any hosted SaaS control plane or cross-customer data plane;
- multi-region active/active control plane;
- exactly-once Kubernetes Job execution guarantees;
- a workflow engine or custom workload CRD;
- full log storage or analytics;
- operating observability or identity-provider infrastructure;
- general-purpose policy-engine replacement;
- automatic cloud-provider backup orchestration;
- cost accounting and chargeback;
- cross-cluster global queues;
- Kueue integration, preemption, gang scheduling, or recurring schedules; or
- compliance certification claims.
