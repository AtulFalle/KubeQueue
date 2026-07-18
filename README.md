# KubeQueue

KubeQueue is a lightweight control plane for standard Kubernetes batch Jobs. It adds queueing,
priority, delayed execution, lifecycle control, history, and a focused dashboard without replacing
Kubernetes or requiring a custom resource.

## Repository layout

```text
apps/
  web/                 Next.js dashboard
  control-plane/       Go API and worker processes
packages/
  api-contract/        OpenAPI source of truth
  api-client/          TypeScript client boundary
deploy/
  helm/kubequeue/      Cluster packaging
  kind/                Local Kubernetes cluster
docs/                  Architecture decisions and engineering documentation
.cursor/
  rules/               Persistent project conventions
  skills/              KubeQueue implementation workflow
```

Nx orchestrates both the TypeScript and Go projects. The Go module remains independently usable.

## Start the complete development stack

Docker with Compose is the only local prerequisite for the containerized workflow:

```bash
docker compose up --build --watch
```

The first run builds the development images and downloads dependencies inside containers. Compose
then watches source files; Next.js and Air reload the web, API, and worker processes.

- Web: <http://localhost:3000>
- API health: <http://localhost:8080/healthz>
- Swagger UI: <http://localhost:8081>
- Database browser: <http://localhost:8082>
- PostgreSQL: `localhost:5432`

Use PostgreSQL credentials `kubequeue` / `kubequeue` locally. Override `POSTGRES_PASSWORD` in an
untracked `.env` file when needed. In Adminer, choose PostgreSQL and use server `postgres`, database
`kubequeue`, and the same credentials. Press Ctrl+C to stop attached services, then run
`docker compose down` to remove containers. The database volume is preserved.

After Node dependencies are installed locally, `pnpm dev` runs the same Compose workflow through
Nx. Use `pnpm go:tidy` to generate Go module checksums inside Docker without installing Go on the
host. Use `pnpm dev:down` and `pnpm dev:logs` for lifecycle and logs.

## Optional native toolchain

- Node.js 24.18.0 LTS
- pnpm 11.14.0 through Corepack
- Go 1.26.5
- Docker
- kubectl
- kind
- Helm
- Tilt
- golangci-lint v2

For native linting, tests, builds, or kind/Tilt development, install those tools and run:

```bash
corepack enable
pnpm install
go work sync
go -C apps/control-plane mod tidy
pnpm check
```

Commit `pnpm-lock.yaml` and `apps/control-plane/go.sum` for reproducible CI.

## Common tasks

```bash
pnpm dev
pnpm dev:down
pnpm dev:logs
pnpm dev:web
pnpm go:tidy
pnpm nx run control-plane:serve-api
pnpm nx run control-plane:serve-worker
pnpm nx run api-contract:lint
pnpm nx run deploy:kind-create
pnpm nx run deploy:tilt
pnpm check
```

See [docs/architecture.md](docs/architecture.md) and
[CONTRIBUTING.md](CONTRIBUTING.md) before implementing product behavior.

## Phase 1 behavior

- Submit standard Kubernetes Job templates or automatically adopt Jobs in watched namespaces.
- Filter live state by status, priority, namespace, team label, and name with shareable URLs.
- Reorder and reprioritize queued work, with global and per-namespace concurrency controls.
- Pause, resume, terminate, and retry while preserving immutable attempt history.
- Delay execution until a one-time scheduled instant.
- Consume changes through the REST API, generated TypeScript client, or live SSE stream.

Kubernetes remains authoritative for observed execution state. KubeQueue persists desired state and
history in PostgreSQL; SQLite is supported only for a single-process local loop. See
[docs/architecture.md](docs/architecture.md) for lifecycle and adoption semantics.

The worker watches Jobs and Pods through client-go informers. New Jobs remain suspended until their
Kubernetes identity is persisted, and PostgreSQL leases plus expiring row claims prevent duplicate
admission across worker replicas.

## Continuous integration

Pull requests and pushes to `master` run the GitHub Actions quality gate in
`.github/workflows/ci.yml`. It validates Go formatting, generated-client drift, Docker Compose,
OpenAPI, and Helm; runs Go, PostgreSQL, React accessibility, and TypeScript checks; builds every Nx
project with a build target; and exercises scheduling, adoption, and lifecycle flows on kind.
