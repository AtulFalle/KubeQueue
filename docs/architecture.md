# Architecture

## System boundaries

KubeQueue is split into three runtime processes built from two applications:

- `web`: Next.js user interface. It consumes only the generated API client.
- `api`: Gin HTTP process. It validates commands and invokes application use cases.
- `worker`: Go process. It will schedule work and reconcile desired state with Kubernetes.

The API and worker share one Go module and domain model but have separate composition roots and
deployments. They communicate through durable state rather than in-memory calls.

## Dependency direction

```text
apps/web -> packages/api-client -> packages/api-contract

cmd -> platform -> application -> domain
                   application -> ports <- adapters
```

The domain is pure Go. It cannot import Gin, SQL drivers, Kubernetes clients, or generated
transport types. Interfaces are defined by the package that consumes them.

## Control-plane package ownership

- `internal/domain`: entities, value objects, lifecycle policy, domain errors.
- `internal/application`: use cases and transaction orchestration.
- `internal/ports`: narrow interfaces required by application code.
- `internal/adapters/persistence`: PostgreSQL and SQLite implementations.
- `internal/adapters/kubernetes`: Kubernetes reads, watches, and commands.
- `internal/scheduler`: queue admission and ordering.
- `internal/reconciler`: convergence of desired and observed state.
- `internal/platform`: process composition, HTTP server, configuration, and lifecycle.

## Source-of-truth rules

- PostgreSQL is the production control-plane store; SQLite is limited to single-process local use.
- Kubernetes is the source of truth for observed execution state.
- Durable control-plane records are the source of truth for user intent and history.
- Desired and observed state are stored separately and converged idempotently.
- Standard Kubernetes Jobs are managed directly; no custom resource is introduced in Phase 1.

## Contract and delivery

OpenAPI is the public contract. Go handlers and the TypeScript client follow it; Kubernetes API
objects are never exposed as the product API.

Helm is the deployment source of truth. kind and Tilt provide a local loop over the same images
and chart. Nx is the task entry point for local development and CI.

Docker Compose is the zero-install application loop. It runs PostgreSQL, API, worker, web, Swagger
UI, and Adminer together. Development Dockerfiles use Air and Next.js hot reload; Compose Watch
synchronizes source and rebuilds images only when dependency manifests change. The web proxies
same-origin `/api/*` requests to the API service so browser code does not depend on container DNS.

Significant changes to these decisions require an ADR under `docs/adr`.
