# ADR 0009: Installation identity and authorization

- Status: Accepted
- Date: 2026-07-19

## Context

Phase 3 replaces the preview deployment-wide administrator token with attributable human and
workload identity. Authorization must match the on-premise installation boundary, delegate safely
to projects, and provide recovery without creating a second general-purpose identity system.

## Decision

The installation is the top-level identity, policy, and audit boundary. A project is the delegated
workload, quota, and administration boundary inside an installation. Each Kubernetes namespace may
be bound to at most one project. Teams group principals but do not own workloads or grant authority
by themselves; Job labels and legacy free-text team values never grant access.

Every installation starts with one local human installation owner. First-run setup asks for an
editable username (defaulting to `admin`) and an operator-chosen password, then creates the
installation, local owner, initial project and namespace bindings, and initial policy and quota in
one transaction. Production has no default password. An `admin`/`admin` seed is permitted only
behind an explicit development-only setting and must be rejected outside development mode.

OpenID Connect is an additive human login method configured later from Settings by an installation
owner. It uses Authorization Code flow with PKCE. The API accepts access tokens, not ID tokens, and
identifies an OIDC user by issuer plus immutable subject. SAML is supported only through a
customer-operated OIDC broker. Just-in-time provisioning requires an explicit group or domain
mapping; authentication alone does not create access.

Authorization uses a stable permission catalog enforced for every API operation and event stream in
the application layer, with scoped repository access as defense in depth. Role bindings assign
built-in or installation-defined roles to principals or teams at installation or project scope.
Built-in roles cover installation owner and administrator, project administrator, operator,
submitter, viewer, and auditor responsibilities. Custom-role creators may delegate only permissions
they hold. Bootstrap, local-owner credential recovery, and identity-provider administration are
installation-owner-only, and the final installation owner cannot be removed.

Effective authority is the intersection of the principal's grants, binding scope, project and
namespace ownership, Helm-managed Kubernetes authority, and workload management mode. Application
authorization cannot expand Kubernetes RBAC. Cross-project lookups may return `404` to avoid
identifier enumeration.

Service accounts are non-interactive installation- or project-owned principals. Native credentials
are opaque random values shown once and stored as a safe prefix plus keyed digest. They have bounded
scope, expiry by default, creator and last-used attribution, immediate revocation, and rotation with
a short overlap. OIDC client-credentials access tokens may be accepted for customer-managed
workload identity.

First-time setup is available only while no installation owner exists. A claim creates the local
human owner and all initial installation resources atomically; concurrent claims have exactly one
winner, and failure creates none of them. Local passwords are stored only as an approved
memory-hard password hash. The current local user may change their password only by supplying the
current password. An installation owner may reset another local account's password through an
audited owner-only operation, but plaintext passwords are never returned.

Identity-provider configuration, tests, and enable/disable transitions are secret-safe, audited,
and protected by optimistic concurrency. Client secrets are write-only and responses expose only
whether one is configured. KubeQueue refuses any transition that would remove the final usable
login path or leave the installation without a usable installation-owner login. Break-glass access
is a separate, normally disabled, Helm-enabled, time-limited, rate-limited, Secret-backed, and
fully audited recovery path.

The preview administrator token is migrated through one compatibility release as an attributable
`legacy_admin` principal. Its use is audited and visibly deprecated; setup requires a verified local
owner before the credential is revoked. It is removed from normal operation before `1.0.0` and is
never converted silently into a personal identity or browser session.

## Consequences

- Every action can be evaluated against one installation-scoped authorization model.
- A new installation is usable without external identity infrastructure, while OIDC remains
  available for enterprise federation.
- KubeQueue owns a deliberately narrow local human password system: login, current-password change,
  and owner-authorized reset. It does not provide password hints, password disclosure, or a public
  self-service reset channel.
- Namespace assignment requires both a product binding and sufficient Helm-managed Kubernetes
  authority.
- Permission, binding, principal, and credential changes must invalidate affected access promptly.
- Setup, delegation, token lifecycle, project isolation, and owner protection require adversarial
  and concurrency testing before GA.
