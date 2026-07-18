# KubeQueue

KubeQueue is a lightweight control plane for standard Kubernetes batch Jobs. It adds queueing,
priority, delayed execution, lifecycle control, history, and a focused dashboard without replacing
Kubernetes or requiring a custom resource.

> [!WARNING]
> KubeQueue v0.1 is an experimental developer preview. It does not provide production support,
> upgrade compatibility, or multi-user authorization. The dashboard is an administrative surface:
> keep it cluster-private and access it through authenticated Kubernetes port-forwarding.

## Install the v0.1.3 preview

Prerequisites:

- Kubernetes 1.31 or later
- Helm 3.14 or later with OCI support
- A PostgreSQL database reachable from the cluster
- Permission to create namespace-scoped Roles, Deployments, Services, Secrets, and Jobs

Create the namespace and secrets without placing credentials in Helm release values:

```bash
kubectl create namespace kubequeue
kubectl -n kubequeue create secret generic kubequeue-database \
  --from-literal=database-url='postgres://USER:PASSWORD@HOST:5432/kubequeue?sslmode=require'
kubectl -n kubequeue create secret generic kubequeue-admin \
  --from-literal=admin-token="$(openssl rand -hex 32)"
```

Install the OCI chart:

```bash
helm install kubequeue oci://ghcr.io/atulfalle/charts/kubequeue \
  --version 0.1.3 \
  --namespace kubequeue \
  --set-string database.existingSecret=kubequeue-database \
  --set-string config.adminTokenExistingSecret=kubequeue-admin
```

Access the cluster-private dashboard:

```bash
kubectl -n kubequeue port-forward service/kubequeue-kubequeue-web 3000:3000
```

Open <http://127.0.0.1:3000>. Do not expose this preview dashboard through a public Service or
Ingress: every dashboard user receives administrative API access.

Uninstall with `helm uninstall kubequeue --namespace kubequeue`. The external PostgreSQL database
and manually created Secrets are retained. Review the chart-specific guidance in
[`deploy/helm/kubequeue/README.md`](deploy/helm/kubequeue/README.md) before installing or upgrading.

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
`.github/workflows/ci.yml`. It uses Nx to format, lint, test, and build affected projects. Deployment
configuration is linted only when `deploy/**` or `compose.yaml` changes. Tag-driven release checks
validate the complete tree, PostgreSQL integration tests, production images, and the packaged Helm
chart before publishing.

## Release status and support

Release notes are maintained in [`CHANGELOG.md`](CHANGELOG.md). v0.1.x is a developer preview and
does not carry a production support or backward-compatibility guarantee. Report security issues
privately as described in [`SECURITY.md`](SECURITY.md); use GitHub issues for reproducible bugs and
feature requests. Maintainers publish immutable artifacts using
[`docs/releasing.md`](docs/releasing.md).

KubeQueue is licensed under the [Apache License 2.0](LICENSE).
