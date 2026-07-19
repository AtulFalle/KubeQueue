# Threat model

## Scope and assumptions

This threat model covers one on-premise KubeQueue installation: its web, API, worker, and migration
processes; PostgreSQL product state; Kubernetes authority; browser and API clients; configured OIDC
provider; and optional customer-owned telemetry and secret integrations.

The installation is the highest KubeQueue identity, policy, and audit boundary. Projects provide
delegated isolation inside it; they are not isolation from a customer cluster administrator,
database administrator, identity-provider administrator, or host administrator. KubeQueue has no
hosted control plane, required cloud account, license callback, or default external telemetry path.

The customer is assumed to secure and operate the Kubernetes cluster, Ingress or Gateway,
PostgreSQL, DNS, OIDC provider, registry, backup storage, and any telemetry or secret systems.
Compromise of those authorities can compromise KubeQueue. KubeQueue still minimizes granted
authority, validates inputs at its boundaries, fails closed where it can, and records attributable
security events.

## Assets

- Human and service-account identities, group mappings, roles, bindings, and effective permissions.
- Browser sessions, OAuth credentials, native API credentials, and break-glass material,
  database credentials, encryption keys, and Kubernetes service-account credentials.
- Projects, namespace bindings, policies, quota reservations, queue order, and admission decisions.
- Job intent, templates, manifests, lifecycle history, Kubernetes object identity, and observed
  state.
- Immutable product audit history and its actor, scope, ordering, retention, and legal-hold
  metadata.
- PostgreSQL schema, migration checksums, leases, fencing generations, claims, and backup metadata.
- Release images, Helm charts, provenance, signatures, software bills of materials, and
  compatibility declarations.
- Availability of the API, web UI, scheduler, reconciliation, and recovery procedures.
- Customer infrastructure metadata exposed through health, telemetry, errors, or diagnostic
  bundles.

## Trust boundaries

### Browser to web BFF

The browser is untrusted input and cannot hold OAuth or API credentials. The boundary uses a
host-scoped secure session cookie, session-bound CSRF protection, strict origin checking, bounded
request input, and server-side session revocation.

### Web BFF and non-browser clients to API

The API treats the BFF as a caller, not as an authorization authority. It authenticates each
request, resolves current principal and scope, authorizes the operation, and bounds object lookup
and response data. Direct API clients use OIDC access tokens or scoped native service-account
credentials.

### Control-plane processes to PostgreSQL

PostgreSQL is the durable product-state and coordination boundary. Connections authenticate with
process-appropriate credentials and support certificate and hostname verification. Administrative
mutations and their audit events are transactional. Runtime processes verify schema compatibility;
only the migration process writes schema changes.

### Worker to Kubernetes API

Kubernetes is authoritative for observed execution state. The worker alone receives Job and Pod
authority, limited by Helm to selected namespaces unless an administrator explicitly accepts
cluster-wide authority. Product authorization cannot expand Kubernetes RBAC. Durable desired state
and observed Kubernetes state remain separate.

### Installation to OIDC provider

The customer-operated OIDC provider is authoritative for authentication claims and keys, while
KubeQueue remains authoritative for local provisioning and authorization. Issuer, audience,
signature algorithm, signature, time claims, authorized party, state, nonce, PKCE, and redirect
configuration are validated. SAML reaches KubeQueue only through a customer-operated OIDC broker.

### Installation to customer integrations

Registries, secret stores, telemetry collectors, SIEM or object storage, and backup systems are
outside the installation's product boundary. They are disabled or locally contained by default and
use explicitly configured customer endpoints. Data sent to them inherits the customer's access,
retention, and transport controls.

### Helm and release administration to runtime

Helm and GitOps own deployment configuration, Kubernetes RBAC, Secret references, upgrades, and
rollback ordering. Signed release artifacts and provenance establish what is installed. Runtime UI
administration cannot grant Kubernetes authority or silently change this deployment boundary.

## Actors

- **Unauthenticated remote user:** can reach the public login, callback, setup when unclaimed, and
  any endpoints intentionally exposed without a session.
- **Authenticated human:** has only permissions granted through current role bindings and scope.
- **Service account or automation:** uses a scoped native credential or customer OIDC
  client-credentials token and cannot create an interactive browser session.
- **Project administrator or operator:** is trusted only within delegated project permissions and
  may attempt to cross project or namespace boundaries.
- **Installation owner or administrator:** is highly privileged and can alter local access and
  policy, but remains attributable and cannot expand Helm-managed Kubernetes RBAC.
- **Break-glass operator:** temporarily holds explicit recovery authority and is subject to expiry,
  rate limits, and audit.
- **Kubernetes, database, identity, or platform administrator:** controls an external authority that
  can undermine KubeQueue controls; this is a customer-trusted role, not a KubeQueue tenant.
- **Compromised workload or namespace user:** may create misleading Jobs, labels, events, or resource
  pressure within Kubernetes authority visible to KubeQueue.
- **Compromised dependency or artifact publisher:** attempts to introduce malicious code or
  dependencies into a release.
- **Network attacker:** can observe, replay, redirect, or modify traffic where customer transport
  controls are absent or misconfigured.

## Threats and mitigations

### Identity spoofing and token confusion

Threats include forged or replayed OIDC tokens, accepting an ID token as an API credential, mutable
email reassignment, malicious key rotation, login CSRF, and permissive just-in-time provisioning.

Mitigations are strict OIDC validation, issuer-plus-subject identity, bounded JWKS caching and
rotation, Authorization Code with PKCE, state and nonce validation, access-token-only API
authentication, explicit provisioning mappings, short credential lifetimes, and prompt session
revocation after identity or privilege changes.

### Session theft, fixation, and cross-site mutation

Threats include browser script access to credentials, session fixation, cross-site requests, stale
sessions after role changes, and a compromised BFF attempting to bypass API policy.

Mitigations are server-side revocable sessions, secure host-scoped `HttpOnly` cookies, identifier
rotation after login and privilege changes, idle and absolute expiry, rotating encrypted refresh
tokens, session-bound CSRF tokens, strict origin checks, and independent API authentication and
authorization. Content security policy and dependency hygiene reduce but do not eliminate the
impact of browser code compromise.

### Authorization bypass and confused deputy behavior

Threats include guessed object identifiers, inconsistent checks across REST and event streams,
trusting Job labels or free-text teams, cross-project namespace use, overbroad custom roles, and an
administrator delegating permissions they do not possess.

Mitigations are centralized stable permissions in the application layer, scoped repository access,
project-aware event delivery, non-enumerating cross-scope responses, one-project namespace binding,
non-authoritative labels and legacy metadata, delegation ceilings, protected owner and recovery
permissions, and effective authority intersected with Kubernetes RBAC and workload management mode.

### Bootstrap, recovery, and credential abuse

Threats include concurrent or remote setup claims before the first owner exists, setup reactivation,
permanent recovery backdoors, stolen service-account credentials, and indefinite legacy-token use.

Mitigations are a cluster-private initial deployment, an infrastructure-readiness gate, one
transactionally locked setup winner, permanent closure after a verified local owner exists,
explicit and normally disabled break-glass configuration, time and rate bounds, credential one-time
display, keyed digest storage, expiry, scoped issuance, rotation overlap, immediate revocation, full
attribution, and removal of normal legacy-token operation before GA.

### Sensitive workload and credential disclosure

Threats include secrets embedded in Job templates, manifest access through metadata permission,
credentials in logs or audit summaries, unsafe diagnostic bundles, and database disclosure of
recoverable OAuth material.

Mitigations are a separate manifest-read permission, structural redaction at every output boundary,
inline-secret detection, Kubernetes Secret references, explicit prohibited fields, envelope
encryption with key material outside PostgreSQL, local generation and administrator-controlled
sharing of diagnostics, and leak tests across APIs, logs, audit, telemetry, and support artifacts.

### Audit tampering, repudiation, and audit denial of service

Threats include mutations without actor records, update or deletion through product APIs, forged
request origin, retention bypass, export reordering, and floods of denied requests exhausting the
control plane.

Mitigations are transactional audit writes for administrative mutations, append-only product
access, trustworthy proxy configuration, stable event and correlation IDs, captured authorization
decisions, ordered cursor export, retention with legal hold, separately authorized audit access, and
an independent bounded writer with visible degradation for events lacking a business transaction.

Product immutability does not protect against a privileged PostgreSQL administrator altering the
database or backups. Customers needing independent tamper evidence must export audit records to a
customer-controlled append-only or immutable system.

### Kubernetes authority abuse and workload identity conflict

Threats include automatic control of unrelated Jobs, forged KubeQueue labels, rebinding durable
records to different objects, operation outside configured namespaces, and compromise of a
cluster-wide worker credential.

Mitigations are explicit managed, observed, ignored, and conflicted modes; namespace, name, and UID
identity checks; exclusions for internal and Helm-hook Jobs; API scope checks before intent
creation; selected namespace authority by default; explicit consent and warnings for cluster-wide
authority; and no Kubernetes token mount in API or web processes.

### Split brain, stale observation, and duplicate mutation

Threats include an expired leader continuing to mutate Kubernetes, followers regressing observed
state, failover replaying commands, and one inaccessible workload blocking all reconciliation.

Mitigations are PostgreSQL leadership with monotonic fencing generations, generation-conditioned
claims and intent, leadership validation immediately before writes, mutation-readiness loss on
lease expiry, stable Kubernetes identities and supported resource preconditions, idempotent
commands, resource-version-aware observation compare-and-set, warm followers, and failure isolation
by namespace and Job.

Kubernetes acceptance immediately before leadership loss cannot be made atomic with PostgreSQL.
After failover, the new leader observes Kubernetes before retrying. KubeQueue does not promise
exactly-once Job execution.

### Quota races, starvation, and resource exhaustion

Threats include concurrent quota bypass, leaked reservations after failure, priority abuse,
cross-project queue manipulation, starvation, unbounded searches or pages, and full-inventory event
streams.

Mitigations are transactional checks and reservations, idempotent release and recovery, versioned
policy attribution, weighted fair project sharing, restricted emergency priority, project-relative
reordering, bounded cursor pagination and search, allowlisted sorting, scoped change events,
explicit retention, and a measured scale envelope. Inputs beyond supported limits fail predictably
rather than loading unbounded state.

### Database, backup, and restore compromise

Threats include database interception, unavailable or inconsistent state, unauthorized migrations,
stolen backups, restore of incompatible schema, and replay against Jobs already running in
Kubernetes.

Mitigations are authenticated connections with `verify-full` support, isolated credentials,
dedicated migration ownership, immutable checksummed migrations, schema compatibility checks,
customer-encrypted backup storage, full plus WAL/PITR guidance, release and schema metadata, isolated
restore verification, and workload-association checks before Kubernetes reconnection. Mutation
readiness fails closed when PostgreSQL is unavailable.

### Network and integration endpoint abuse

Threats include plaintext external traffic, spoofed forwarding headers, server-side requests to
malicious configured endpoints, and unintended data export.

Mitigations are required external TLS, explicit trusted-proxy configuration, administrator-only
endpoint configuration, strict OIDC issuer and redirect matching, bounded timeouts and responses,
no default external telemetry, and opt-in egress policy once required DNS, PostgreSQL, Kubernetes,
OIDC, registry, and telemetry destinations are known.

### Supply-chain compromise and unsupported combinations

Threats include artifact substitution, rebuild drift, vulnerable dependencies, leaked build
secrets, malicious CI dependencies, and unsafe version combinations.

Mitigations are immutable build-once promotion, signed images and OCI charts, expected-source
provenance verification, software bills of materials, source/dependency/image/manifest/license and
secret scanning, pinned CI actions, expiring high-severity exception review, release ownership, and
tested API, Kubernetes, PostgreSQL, Helm, and schema compatibility matrices.

## Security invariants

1. No production request is authorized solely by network location, UI visibility, a Job label, or
   legacy team text.
2. The API authenticates and authorizes every operation and event subscription; the BFF is not an
   authorization bypass.
3. Effective product authority never exceeds Helm-managed Kubernetes authority or workload
   management mode.
4. A namespace is bound to at most one project, and project-scoped principals cannot enumerate or
   mutate another project's resources.
5. Browser clients never receive OAuth credentials, refresh tokens, or deployment-wide
   administrator credentials.
6. Bootstrap has one transactional winner and cannot be re-enabled through product APIs.
7. Native credentials are never stored recoverably; recoverable OAuth and session secrets are
   encrypted with key material outside PostgreSQL.
8. Job metadata access does not imply manifest access, and secrets or full manifests never enter
   logs, audit summaries, telemetry, errors, or diagnostic bundles.
9. Every successful administrative mutation commits an attributable audit event in the same
   transaction.
10. Only the current fenced leader creates new durable mutation authority, and failover reconciles
    Kubernetes observation before retry.
11. Desired state, observed Kubernetes state, lifecycle, and synchronization health remain distinct.
12. Quota reservation and admission are transactional, and policy and scheduling decisions record
    their effective version.
13. Runtime processes never apply production schema migrations, and unsupported schemas fail
    readiness.
14. KubeQueue sends no workload data or telemetry to a KubeQueue-hosted service by default.
15. Release artifacts are promoted without rebuilding and are verifiable against signatures,
    provenance, and published compatibility.

## Deferred and accepted risks

- A customer cluster, database, identity-provider, ingress, node, or secret-store administrator can
  bypass controls within the authority they operate. KubeQueue does not create a security boundary
  against those administrators.
- Product audit records are append-only through KubeQueue, not cryptographically immutable against
  a PostgreSQL administrator. Independent immutable export is customer-operated.
- Internal service-to-service encryption depends on customer network or service-mesh policy; only
  the external ingress and declared PostgreSQL verification boundary are required by KubeQueue.
- Portable default-deny egress is opt-in until installation-specific DNS and dependency endpoints
  are configured. Component-specific ingress remains deny-by-default.
- Compromise of the worker in all-namespace mode has cluster-wide Job impact. Selected namespace
  mode remains the least-privilege default.
- KubeQueue provides no exactly-once Kubernetes Job execution guarantee and no multi-region
  active/active control plane.
- Native SAML, SCIM, hosted identity, automatic cloud backup orchestration, and operated
  observability infrastructure are outside GA scope.
- General-purpose admission policy, pod security, network policy, and secret scanning inside
  referenced Kubernetes Secrets remain Kubernetes or customer-tool responsibilities.
- CPU, GPU, cost, and budget quota enforcement is deferred until transactional reservation is
  possible.
- Kueue interoperability, preemption, gang scheduling, recurring schedules, and cross-cluster
  queues require separate future threat analysis and architecture decisions.
- Certification claims are excluded. Customers map KubeQueue's controls and release evidence into
  their own compliance programs.
