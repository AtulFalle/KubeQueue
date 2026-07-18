# KubeQueue

KubeQueue is a lightweight control plane for standard Kubernetes batch Jobs. This repository
currently contains the engineering foundation only; product features are intentionally absent.

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
Nx. Use `pnpm dev:down` and `pnpm dev:logs` for lifecycle and logs.

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
go mod tidy -C apps/control-plane
pnpm check
```

Commit the generated `pnpm-lock.yaml` and `apps/control-plane/go.sum` before enabling CI.

## Common tasks

```bash
pnpm dev
pnpm dev:down
pnpm dev:logs
pnpm dev:web
pnpm nx run control-plane:serve-api
pnpm nx run control-plane:serve-worker
pnpm nx run api-contract:lint
pnpm nx run deploy:kind-create
pnpm nx run deploy:tilt
pnpm check
```

See [docs/architecture.md](docs/architecture.md) and
[CONTRIBUTING.md](CONTRIBUTING.md) before implementing product behavior.

## Continuous integration

Pull requests and pushes to `master` run the GitHub Actions quality gate in
`.github/workflows/ci.yml`. It validates Go formatting, Docker Compose, OpenAPI, and Helm; runs Go
and TypeScript linting and type checks; executes tests with the Go race detector; and builds every
Nx project with a build target.
