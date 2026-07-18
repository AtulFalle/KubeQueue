# ADR 0006: Lifecycle and synchronization state

- Status: Accepted
- Date: 2026-07-19

## Context

Phase 1 stores desired and Kubernetes-observed lifecycle states, but uses lifecycle values to also
represent missing objects and command progress. A missing Kubernetes Job can appear cancelled, an
accepted pause can still look running, and stale observations have no timestamp or resource version.
The dashboard therefore cannot distinguish convergence delay, worker failure, lost authority, or a
genuine lifecycle transition.

Adding operational meanings to the lifecycle enum would mix product policy with synchronization
health and would break existing clients as more failure cases are discovered.

## Decision

Keep desired and observed lifecycle state as separate existing concepts. Add an independent
synchronization model with stable values:

- `SYNCED`: desired and observed state are consistent for the current operation.
- `PENDING`: durable intent is waiting for Kubernetes convergence.
- `MISSING`: the previously associated Kubernetes object is absent.
- `STALE`: the latest successful observation is older than the configured freshness threshold.
- `ERROR`: reconciliation failed and will be retried or requires intervention.
- `OUT_OF_SCOPE`: KubeQueue no longer has configured authority for the namespace.
- `CONFLICTED`: Kubernetes identity conflicts with the durable association.

Persist the Kubernetes resource version, `observedAt`, `lastSeenAt`, pending action, sanitized last
error, retry count, and next retry time. Observation updates use resource-version-aware
compare-and-set so older informer data cannot overwrite newer state.

A lifecycle command records desired intent and returns the updated Job without claiming Kubernetes
convergence. The API retains its current command response shape for compatibility and exposes
pending and synchronization fields on the Job. SSE and polling report subsequent convergence.

Kubernetes condition reason and message are stored in sanitized form for diagnosis. Missing objects
do not become `CANCELLED`; cancellation remains user intent.

## Consequences

- The dashboard can show “Pausing,” “Waiting for worker,” “Missing,” and actionable errors without
  inventing lifecycle states.
- Filters can distinguish desired lifecycle, observed lifecycle, and synchronization health.
- Existing lifecycle enum consumers remain compatible.
- Persistence requires new freshness, error, and resource-version columns plus deterministic
  backfill for stale Phase 1 cancellation records.
- Commands remain asynchronous and UI controls must disable conflicting actions while pending.
- Resource versions are opaque strings and cannot be compared numerically.
