# ADR 0011: Audit and observability contract

- Status: Accepted
- Date: 2026-07-19

## Context

Enterprise operation requires security-relevant actions to be attributable and operational failures
to be diagnosable without exporting customer data by default. Audit history and telemetry serve
different purposes and need separate durability, access, and retention rules.

## Decision

Security audit events are append-only records separate from workload lifecycle events and ordinary
logs. Each event identifies time, event and correlation IDs, authenticated principal and credential,
authentication method, action, target, installation and project scope, authorization decision,
result, stable reason, trustworthy request origin, and redacted change summaries.

An administrative mutation and its audit event commit in the same PostgreSQL transaction.
Authentication failures and denied requests, which have no business transaction, use an independent
bounded writer whose overload is visible and cannot exhaust the control plane. Product APIs cannot
update audit records.

Audit access is separately authorized. Cursor pagination, configured retention, legal hold, and
ordered export operate over the durable local record. Retention cannot remove held records.
Customer-configured SIEM or object-storage delivery is an optional export of this record, not its
source of truth.

Operational telemetry consists of bounded-cardinality Prometheus/OpenMetrics metrics, OpenTelemetry
traces, and structured JSON logs with request and trace correlation. It covers API, PostgreSQL,
reconciliation, queues and quotas, leadership, informer health, desired/observed divergence,
migrations, and schema compatibility. Credentials, complete manifests, and unbounded customer
identifiers are excluded. KubeQueue exposes telemetry to customer-operated backends and sends
nothing to a KubeQueue-hosted service.

GA service objectives are:

- API availability of 99.9% monthly;
- 99% of read API requests below one second;
- 99% of accepted actions converged below 30 seconds, excluding Kubernetes-declared blockers; and
- 99% of eligible Jobs admitted below 60 seconds.

Reference dashboards, runbooks, and fast- and slow-burn error-budget alerts accompany these
objectives. Error-budget exhaustion triggers release-risk review and reliability work; it does not
silently weaken authorization, consistency, or audit controls.

## Consequences

- Administrative success cannot exist durably without its corresponding audit record.
- Denial and authentication-failure audit loss or overload is a visible security degradation with
  bounded resource use.
- Audit retention capacity must be planned independently from workload history and log retention.
- SLO exclusions must be machine-identifiable and limited to declared external blockers.
- Customers operate retention and access controls for exported telemetry and audit copies.
