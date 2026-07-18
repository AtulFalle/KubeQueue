---
name: implement-kubequeue-change
description: Implements KubeQueue product changes across OpenAPI, Go, React, and Kubernetes boundaries. Use when adding or changing KubeQueue behavior, endpoints, scheduling, reconciliation, persistence, UI workflows, or deployment configuration.
---

# Implement a KubeQueue Change

## Workflow

1. Read `docs/architecture.md` and the applicable `.cursor/rules` files.
2. Identify the owning layer and keep the change within the documented dependency direction.
3. For public behavior, update `packages/api-contract/openapi/openapi.yaml` first.
4. Add pure domain rules and application orchestration before transport or infrastructure code.
5. Implement adapters at the boundary:
   - Gin handlers translate HTTP to application commands.
   - Persistence adapters translate records to domain values.
   - Kubernetes adapters translate API objects to observed state.
6. Regenerate clients only through the repository generation target once it exists.
7. Add tests at the lowest useful layer and an integration test for changed boundaries.
8. Run focused Nx targets, then `pnpm check` before handoff.

## Required properties

- Commands and reconciliation are idempotent.
- Desired and observed state remain separate.
- Queue ordering changes use optimistic concurrency.
- Retries create attempts; they do not erase history.
- Errors retain causes internally and expose stable API codes externally.
- Logs contain identifiers and operation names, never credentials or full manifests.

## Stop conditions

Create an ADR before changing a major boundary, persistence model strategy, public API compatibility, workload ownership model, or scheduler consistency model.
