# ADR 0001: Monorepo and process boundaries

- Status: Accepted
- Date: 2026-07-18

## Context

KubeQueue needs one web application, one public API, and asynchronous Kubernetes reconciliation
without introducing an external queue or a heavy workflow engine.

## Decision

Use an Nx/pnpm monorepo with a Next.js web application and one Go module. Build the Go module into
separate API and worker processes from one source tree. Use an OpenAPI contract between web and API,
and a database-backed coordination model between API and worker.

## Consequences

- Frontend and backend checks have one task graph while retaining native toolchains.
- API and worker can scale and fail independently.
- Shared Go code remains internal to one module.
- Database consistency and idempotent reconciliation are mandatory.
- Adding another runtime or service requires an explicit architectural decision.
