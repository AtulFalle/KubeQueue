# ADR 0010: Browser and sensitive-data boundaries

- Status: Accepted
- Date: 2026-07-19

## Context

The Phase 1 browser path relies on a shared administrator bearer token. Phase 3 introduces
multi-user identity, revocable sessions, and sensitive workload templates, so the browser, web
process, API, database, and Kubernetes Secrets need explicit trust boundaries.

## Decision

The Next.js web process is an authenticated backend-for-frontend (BFF). Local passwords are
submitted only to the BFF, and only a BFF-authenticated internal endpoint may exchange valid local
credentials for a browser session. OAuth credentials, access tokens, refresh tokens, local
passwords, and session state remain server-side. The browser receives only a host-scoped, `Secure`,
`HttpOnly`, `SameSite=Lax` session cookie. Revocable sessions are stored in PostgreSQL so web
replicas share one session authority; identifiers rotate after login, password changes, password
resets, and privilege changes, and idle and absolute lifetimes are bounded.

Mutating browser requests require a session-bound CSRF token and strict `Origin` validation. The BFF
forwards the authenticated principal and request context, but it is not an authorization boundary:
the API authenticates and authorizes every proxied request independently. The web process no longer
receives or injects a deployment-wide administrator token.

TLS is required at the customer-managed external Ingress or Gateway. Connections to PostgreSQL
support certificate and hostname verification with customer-supplied trust material. Portable
in-cluster encryption beyond these boundaries depends on the customer's network and service-mesh
policy and is not implied.

Long-lived credentials are supplied through referenced Kubernetes Secrets or compatible
customer-operated secret stores. Local passwords are accepted only on write, hashed with an
approved memory-hard password hash, and never logged, audited, or returned. Recoverable OAuth and
session material is encrypted with key material kept outside PostgreSQL. Identity-provider client
secrets are write-only; status and configuration responses expose only a configured/not-configured
indicator. Rotation must not require manual Pod deletion. API and web processes do not mount
Kubernetes service-account tokens; Kubernetes mutation authority remains with the worker, and
migration credentials remain isolated to the migration process.

Stored Job templates are sensitive data. Reading Job metadata does not imply permission to read its
manifest. Manifest disclosure requires a distinct permission and structural redaction applies to
the UI, logs, audit summaries, telemetry, errors, and diagnostic bundles. KubeQueue rejects or warns
on likely inline credentials and favors Kubernetes Secret references. Bearer tokens, cookies,
refresh tokens, client secrets, database URLs, and complete environment values are never recorded.

## Consequences

- Browser compromise does not directly reveal reusable OAuth or API bearer credentials, although an
  active browser session can still exercise its granted authority.
- Web and API replicas depend on PostgreSQL availability for session validation and revocation.
- Deployments must configure an accurate public URL, TLS termination, trusted proxy handling, and
  OIDC redirects when OIDC is enabled.
- Secret rotation and redaction become cross-cutting acceptance requirements.
- Customers remain responsible for transport protection outside KubeQueue's declared endpoints and
  for the security of their identity provider, secret store, and ingress implementation.
