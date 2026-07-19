# KubeQueue Helm chart

This chart installs a KubeQueue developer preview. It deploys the API, worker, web
dashboard, migration hook, namespace-scoped RBAC, and internal Services. PostgreSQL is not bundled.

## Security and support

Preview releases are not production-supported and do not guarantee upgrades or rollback compatibility. The
dashboard is a single-administrator interface. Keep the web Service cluster-private and access it
through authenticated Kubernetes port-forwarding. Do not add a public Ingress without an external
authentication layer.

The chart requires a non-empty administrator token. Prefer an existing Secret so credentials are
not stored in Helm release values.

## Prerequisites

- Kubernetes 1.31 or later
- Helm with OCI registry support
- A PostgreSQL database reachable from the target namespace

## Install

Create a namespace and Secrets:

```bash
kubectl create namespace kubequeue
kubectl -n kubequeue create secret generic kubequeue-database \
  --from-literal=database-url='postgres://USER:PASSWORD@HOST:5432/kubequeue?sslmode=require'
kubectl -n kubequeue create secret generic kubequeue-admin \
  --from-literal=admin-token="$(openssl rand -hex 32)"
```

Install the published chart:

```bash
helm install kubequeue oci://ghcr.io/atulfalle/charts/kubequeue \
  --version <release-version> \
  --namespace kubequeue \
  --set 'watch.namespaces={default,batch-jobs}' \
  --set-string database.existingSecret=kubequeue-database \
  --set-string config.adminTokenExistingSecret=kubequeue-admin
```

For private GHCR packages, create an image-pull Secret and configure the workload service account
before installation. Public preview packages do not require registry credentials.

## Access

```bash
kubectl -n kubequeue port-forward service/kubequeue-kubequeue-web 3000:3000
```

Open <http://127.0.0.1:3000>. The web process injects the deployment-wide administrator token when
proxying API requests, so anyone who can reach the dashboard has administrative access.

## Configuration

| Value                                | Purpose                                                                     | Default           |
| ------------------------------------ | --------------------------------------------------------------------------- | ----------------- |
| `watch.mode`                         | `selected` for namespace Roles or `all` for explicit cluster-wide authority | `selected`        |
| `watch.namespaces`                   | Namespaces managed in selected mode; empty uses the release namespace       | `[]`              |
| `watch.excludedNamespaces`           | Namespaces excluded defensively in all mode                                 | System namespaces |
| `rbac.create`                        | Create worker RBAC independently from the ServiceAccount                    | `true`            |
| `rbac.allowClusterWide`              | Explicit consent required when `watch.mode=all`                             | `false`           |
| `imagePullSecrets`                   | Image pull Secret references applied to every workload                      | `[]`              |
| `config.globalConcurrency`           | Maximum globally admitted Jobs                                              | `10`              |
| `config.namespaceConcurrency`        | Maximum admitted Jobs per namespace                                         | `5`               |
| `config.adminToken`                  | Inline administrator token; existing Secret is preferred                    | `""`              |
| `config.adminTokenExistingSecret`    | Secret containing the administrator token                                   | `""`              |
| `config.adminTokenExistingSecretKey` | Administrator-token key                                                     | `admin-token`     |
| `database.url`                       | Inline PostgreSQL URL; existing Secret is preferred                         | `""`              |
| `database.existingSecret`            | Secret containing the PostgreSQL URL                                        | `""`              |
| `database.existingSecretKey`         | PostgreSQL URL key                                                          | `database-url`    |
| `networkPolicy.enabled`              | Restrict ingress to same-release pods                                       | `false`           |

Image repositories default to KubeQueue GHCR packages. Empty image-tag values resolve to the
packaged chart's `appVersion`, so all workloads use the matching release by default. Override
individual tags only when testing a custom build.

Cluster-wide discovery requires explicit consent:

```bash
helm upgrade --install kubequeue oci://ghcr.io/atulfalle/charts/kubequeue \
  --version <release-version> \
  --namespace kubequeue \
  --set-string watch.mode=all \
  --set rbac.allowClusterWide=true \
  --set-string database.existingSecret=kubequeue-database \
  --set-string config.adminTokenExistingSecret=kubequeue-admin
```

Cluster-wide mode grants the worker cluster-scoped Job mutation authority. KubeQueue always excludes
the release namespace and the Kubernetes system namespaces even if they are omitted from values.

## Upgrade and rollback

The pre-upgrade hook applies forward-only database migrations before workloads roll out. v0.1.x
does not guarantee that an older binary can use a migrated database. Back up PostgreSQL before any
upgrade. If an upgrade fails, restore the database backup before running `helm rollback`.

## Uninstall

```bash
helm uninstall kubequeue --namespace kubequeue
```

The external PostgreSQL database and manually created Secrets are not deleted.
