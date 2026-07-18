# ADR 0005: Opt-in admission interception

- Status: Accepted
- Date: 2026-07-19

## Context

Informer-based adoption happens after Kubernetes accepts a Job. Pods may therefore start or finish
before KubeQueue can suspend and order the workload. Polling faster or patching immediately after
discovery cannot provide a queue-admission guarantee.

Existing applications and CronJobs should be able to retain the standard Kubernetes Job API
without routing every submission through the KubeQueue API.

## Decision

Provide an optional mutating admission webhook that intercepts Job creation before persistence.
Interception is enabled only for namespaces carrying a documented KubeQueue enrollment label.

For an eligible Job, the webhook:

- sets `spec.suspend=true`;
- adds the KubeQueue management marker needed for managed adoption; and
- leaves the standard `batch/v1` Job shape otherwise intact.

The webhook does not intercept:

- Jobs annotated `kubequeue.io/ignore: "true"`;
- Helm hooks;
- KubeQueue internal workloads;
- excluded system namespaces; or
- Jobs already associated with a valid KubeQueue submission.

Jobs created by CronJobs remain eligible. The webhook uses `failurePolicy: Fail` for enrolled
namespaces because fail-open behavior would silently execute work outside queue limits. Enrollment
therefore explicitly trades Job-submission availability for queue correctness. The timeout is kept
short, webhook readiness is observable, and unenrolled namespaces remain unaffected.

Webhook certificates and rotation are owned by the Helm deployment and must not require users to
copy certificate material manually. API-created Jobs continue to be created suspended and do not
depend on webhook availability.

This decision extends ADR 0002 for Phase 2; it does not introduce a custom workload resource.

## Consequences

- Existing Job and CronJob producers can gain queue admission without application-code changes.
- Enrolled namespaces cannot create eligible Jobs while the webhook is unavailable.
- Installation requires admission-registration permissions and certificate lifecycle management.
- Namespace enrollment and workload opt-out must be visible in operational status and
  documentation.
- Admission behavior needs upgrade ordering so a compatible webhook is ready before its
  configuration is activated.
- Unenrolled external Jobs remain observation-only unless an administrator explicitly takes
  control.
