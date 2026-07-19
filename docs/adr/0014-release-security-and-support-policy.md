# ADR 0014: Release security and support policy

- Status: Accepted
- Date: 2026-07-19

## Context

General availability requires customers to verify what they install, understand unresolved
vulnerability risk, and operate within explicit support and lifecycle commitments. These controls
must fit an on-premise product without a hosted license, update, or telemetry service.

## Decision

Release artifacts preserve build-once promotion: commit-addressed images and the OCI Helm chart are
built once, accompanied by provenance and software bills of materials, scanned, and promoted without
rebuilding. Published images and charts are signed keylessly. Deployment guidance verifies the
signature, expected source repository, and provenance before installation.

Release gates scan source, dependencies, images, deployment manifests, licenses, and leaked secrets.
A fixable critical or high finding blocks release unless the designated security owner approves a
documented exception containing the affected artifact, exposure, compensating controls, owner,
remediation plan, and expiry. Exceptions are release evidence, are visible in security notes, and
cannot be carried past expiry without a new review. Dependency and license notices ship with each
release.

KubeQueue publishes:

- supported release and upgrade lifecycles;
- security-report acknowledgement and severity-based remediation targets;
- API, Kubernetes, PostgreSQL, Helm, and schema compatibility and deprecation policies;
- release cadence and rollback requirements;
- backup, restore, and disaster-recovery runbooks;
- measured capacity limits and service objectives; and
- a locally generated, structurally redacted diagnostic bundle.

Support evidence is generated and retained inside the customer environment. Diagnostic bundles are
shared only through an explicit administrator action. KubeQueue has no required cloud account,
license callback, hosted update control plane, or default telemetry path.

Security ownership is explicit for identity, migrations, the OpenAPI contract, Helm, and release
workflows. `1.0.0` requires the Phase 3 security, compatibility, load, recovery, accessibility, and
artifact-verification gates. KubeQueue describes implemented controls and evidence but makes no SOC
2, ISO 27001, HIPAA, or equivalent certification claim.

## Consequences

- Customers can verify release identity and provenance without trusting a KubeQueue-hosted runtime
  service.
- High-severity exceptions are exceptional, time-bounded release decisions rather than silent
  backlog items.
- Supportability requires maintained matrices, policies, runbooks, notices, and redaction tests for
  every release.
- Offline installations can operate normally but must arrange their own artifact distribution and
  vulnerability-advisory intake.
