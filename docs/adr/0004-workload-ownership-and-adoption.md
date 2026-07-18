# ADR 0004: Workload ownership and adoption

- Status: Accepted
- Date: 2026-07-19

## Context

Phase 1 automatically adopts every standard Job in a watched namespace. Live testing showed that
this also adopts Helm migration hooks and other operational workloads. In a broad discovery mode,
automatic adoption would expose unrelated Jobs to lifecycle mutations and would trust user-supplied
`kubequeue.io/job-id` labels without sufficiently verifying identity.

Visibility, queue admission, and lifecycle ownership are different capabilities and must not be
implied by the same adoption event.

## Decision

Classify every discovered workload using one explicit management mode:

- `MANAGED`: KubeQueue created or intercepted the Job and owns admission and lifecycle.
- `OBSERVED`: KubeQueue discovered the Job after creation and provides inventory and history, but
  does not silently assume mutation authority.
- `IGNORED`: policy excludes the Job before durable adoption.
- `CONFLICTED`: a claimed durable identity does not match the expected namespace, name, or
  Kubernetes UID.

KubeQueue-managed Jobs carry both the durable Job ID and a management marker. A discovered Job
becomes managed only through KubeQueue submission, admission interception, or an explicit
administrator take-control operation. Taking control of a running Job must explain that suspension
terminates active Pods.

Adoption applies these exclusions defensively at both the Kubernetes adapter and reconciliation
boundaries:

- `kubequeue.io/ignore: "true"`;
- Helm hooks;
- KubeQueue internal operational Jobs; and
- namespaces excluded by the effective discovery policy.

CronJob ownership alone is not an exclusion. A Job ID association is accepted only when its
namespace, name, and UID are compatible with the durable record. Conflicts are persisted and
surfaced for diagnosis rather than rebound silently.

This decision refines ADR 0002. KubeQueue continues to use standard `batch/v1` Jobs, but discovery
no longer implies full lifecycle ownership.

## Consequences

- Cluster-wide inventory does not automatically grant UI users control over every discovered Job.
- Existing applications gain visibility without changing their submission path.
- Full queue guarantees for external producers require explicit take-control or admission
  interception.
- Previously adopted internal Jobs require a repair migration that marks them ignored or archived.
- API responses and the dashboard must distinguish management mode from lifecycle and
  synchronization state.
- Retry of an observed Job requires explicit ownership because it creates a new managed attempt.
