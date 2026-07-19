# KubeQueue

KubeQueue is a lightweight control plane for standard Kubernetes batch Jobs. It adds queueing,
priority, delayed execution, lifecycle control, history, and a focused dashboard without replacing
Kubernetes or requiring a custom resource.

> [!WARNING]
> KubeQueue v0.1 is an experimental developer preview. It does not provide production support,
> guaranteed upgrade compatibility, or completed GA security and recovery gates. Keep first-time
> setup cluster-private and use authenticated, TLS-protected access for shared installations.

## Install a preview release

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
kubectl -n kubequeue create secret generic kubequeue-security \
  --from-literal=session-digest-key="$(openssl rand -base64 32 | tr -d '\r\n')" \
  --from-literal=credential-encryption-key="$(openssl rand -base64 32 | tr -d '\r\n')" \
  --from-literal=bff-internal-key="$(openssl rand -hex 32)" \
  --from-literal=service-account-digest-key="$(openssl rand -base64 32 | tr -d '\r\n')"
```

Install the OCI chart:

```bash
helm install kubequeue oci://ghcr.io/atulfalle/charts/kubequeue \
  --version <release-version> \
  --namespace kubequeue \
  --set-string database.existingSecret=kubequeue-database \
  --set-string security.existingSecret=kubequeue-security \
  --set-string browser.publicURL=http://localhost:3000 \
  --set-string browser.origin=http://localhost:3000
```

Access the cluster-private dashboard:

```bash
kubectl -n kubequeue port-forward service/kubequeue-kubequeue-web 3000:3000
```

Open <http://localhost:3000> and complete guarded local-owner setup. OIDC is optional and can be
configured dynamically from Settings later.

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

With Node.js, pnpm, and Docker available, the local workflow is one command:

```bash
pnpm dev
```

The first run builds the development images and downloads dependencies inside containers. Compose
then watches source files; Next.js and Air reload the web, API, and worker processes. The Nx target
applies pending migrations before startup, starts without OIDC, binds every published port to
loopback, and creates the development-only `admin` / `admin` login. Set
`KUBEQUEUE_DEV_SEED_LOCAL_ADMIN=false` only when specifically testing guarded first-time setup
against a Kubernetes-enabled stack.

- Web: <http://localhost:3000>
- API health: <http://localhost:8080/healthz>
- Swagger UI: <http://localhost:8081>
- Database browser: <http://localhost:8082>
- PostgreSQL: `localhost:5432`

Use PostgreSQL credentials `kubequeue` / `kubequeue` locally. Override `POSTGRES_PASSWORD` in an
untracked `.env` file when needed. In Adminer, choose PostgreSQL and use server `postgres`, database
`kubequeue`, and the same credentials. Press Ctrl+C to stop attached services, then run
`docker compose down` to remove containers. The database volume is preserved.

Use `pnpm go:tidy` to generate Go module checksums inside Docker without installing Go on the host.
Use `pnpm dev:down` and `pnpm dev:logs` for lifecycle and logs.

## Test the packaged production build

Run one acceptance target:

```bash
pnpm nx run deploy:chart-acceptance
```

It creates a disposable kind cluster, builds production images, installs the packaged Helm chart,
runs migrations and Helm readiness checks, and exercises the browser lifecycle workflow. It does
not require OIDC. Set `KUBEQUEUE_ACCEPTANCE_KEEP_CLUSTER=true` only when you want to inspect the
finished installation manually; otherwise the target cleans it up.

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
[CONTRIBUTING.md](CONTRIBUTING.md) before implementing product behavior. Before starting Phase 3,
run the [Phase 2 manual acceptance checklist](docs/phase-2-manual-acceptance.md).

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
`.github/workflows/ci.yml`. It uses Nx to format, lint, type-check, test, and build affected projects,
including PostgreSQL integration tests. Releases are allowed only from an exact successful
`master` SHA. Release automation builds each production image once, scans and smoke-tests those
images, then promotes the tested manifests and publishes the dynamically versioned Helm chart.

## Release status and support

Release notes are maintained in [`CHANGELOG.md`](CHANGELOG.md). v0.1.x is a developer preview and
does not carry a production support or backward-compatibility guarantee. Report security issues
privately as described in [`SECURITY.md`](SECURITY.md); use GitHub issues for reproducible bugs and
feature requests. Maintainers publish immutable artifacts using
[`docs/releasing.md`](docs/releasing.md).

KubeQueue is licensed under the [Apache License 2.0](LICENSE).
