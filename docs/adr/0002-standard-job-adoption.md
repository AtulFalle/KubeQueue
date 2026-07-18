# ADR 0002: Standard Job adoption

- Status: Accepted
- Date: 2026-07-18

## Context

KubeQueue must work with existing batch workloads and remain lighter than a workflow engine.
Introducing a custom resource would require users to change submission paths and would duplicate
the Kubernetes Job API.

## Decision

Phase 1 manages standard `batch/v1` Jobs directly. The worker watches configured namespaces and
automatically imports existing Jobs. KubeQueue-created Jobs carry a stable `kubequeue.io/job-id`
label; adopted Jobs use their Kubernetes UID as the durable external identity. A sanitized Job
template is retained for history and retry.

Desired state is stored separately from observed Kubernetes state. Existing running Jobs are
observed immediately but enter queue ordering only after they are paused or retried. Running pause
uses Kubernetes Job suspension and therefore terminates active Pods.

## Consequences

- Existing manifests continue to work without a CRD or admission webhook.
- The service account needs Job and Pod watch permissions in every configured namespace.
- Adoption and watch delivery must be idempotent.
- Retry creates a new Kubernetes Job rather than mutating a terminal Job.
- Kubernetes-native suspension behavior must be clear in the UI and documentation.
