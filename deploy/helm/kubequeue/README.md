# KubeQueue Helm chart

This chart installs a KubeQueue developer preview. It deploys the API, worker, web
dashboard, migration hook, namespace-scoped RBAC, and internal Services. PostgreSQL is not bundled.

## Security and support

Preview releases are not production-supported and do not guarantee upgrades or rollback
compatibility. Keep the web Service cluster-private unless TLS and the documented browser origin
are configured at a trusted ingress.

The chart never stores application credentials in Helm values. It requires references to a
customer-managed Secret for browser sessions, BFF authentication, service-account token digests,
and encrypted provider credentials. OIDC is optional and is configured dynamically from Settings
after local setup.

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
kubectl -n kubequeue create secret generic kubequeue-security \
  --from-literal=session-digest-key="$(openssl rand -base64 32 | tr -d '\r\n')" \
  --from-literal=credential-encryption-key="$(openssl rand -base64 32 | tr -d '\r\n')" \
  --from-literal=bff-internal-key="$(openssl rand -hex 32)" \
  --from-literal=service-account-digest-key="$(openssl rand -base64 32 | tr -d '\r\n')"
```

Install the published chart:

```bash
helm install kubequeue oci://ghcr.io/atulfalle/charts/kubequeue \
  --version <release-version> \
  --namespace kubequeue \
  --set 'watch.namespaces={default,batch-jobs}' \
  --set-string database.existingSecret=kubequeue-database \
  --set-string security.existingSecret=kubequeue-security \
  --set-string browser.publicURL=http://localhost:3000 \
  --set-string browser.origin=http://localhost:3000
```

For private GHCR packages, create an image-pull Secret and configure the workload service account
before installation. Public preview packages do not require registry credentials.

## Access

```bash
kubectl -n kubequeue port-forward service/kubequeue-kubequeue-web 3000:3000
```

Open <http://localhost:3000> and complete guarded local-owner setup. The browser receives only its
host-scoped session cookie; encryption keys remain server-side.

## Configuration

| Value                              | Purpose                                                                         | Default                      |
| ---------------------------------- | ------------------------------------------------------------------------------- | ---------------------------- |
| `watch.mode`                       | `selected` for namespace Roles or `all` for explicit cluster-wide authority     | `selected`                   |
| `watch.namespaces`                 | Namespaces managed in selected mode; empty uses the release namespace           | `[]`                         |
| `watch.excludedNamespaces`         | Namespaces excluded defensively in all mode                                     | System namespaces            |
| `rbac.create`                      | Create worker RBAC independently from the ServiceAccount                        | `true`                       |
| `rbac.allowClusterWide`            | Explicit consent required when `watch.mode=all`                                 | `false`                      |
| `imagePullSecrets`                 | Image pull Secret references applied to every workload                          | `[]`                         |
| `config.globalConcurrency`         | Maximum globally admitted Jobs                                                  | `10`                         |
| `config.namespaceConcurrency`      | Maximum admitted Jobs per namespace                                             | `5`                          |
| `runtime.environment`              | Runtime safety mode (`production`, `development`, or `test`)                    | `production`                 |
| `development.localAdminSeed`       | Explicit development/test-only `admin`/`admin` seed                             | `false`                      |
| `browser.publicURL`                | Browser-visible web origin used by redirects                                    | required                     |
| `browser.origin`                   | Exact browser origin accepted by API session and CORS checks                    | required                     |
| `security.existingSecret`          | Secret containing all application key material                                  | required                     |
| `security.sessionDigestKey`        | Session credential HMAC key in the Secret                                       | `session-digest-key`         |
| `security.credentialEncryptionKey` | 32-byte key encrypting stored OIDC/session credentials                          | `credential-encryption-key`  |
| `security.bffInternalKey`          | Internal web-to-API authentication key                                          | `bff-internal-key`           |
| `security.serviceAccountDigestKey` | Native service-account credential digest key                                    | `service-account-digest-key` |
| `database.url`                     | Inline PostgreSQL URL; existing Secret is preferred                             | `""`                         |
| `database.existingSecret`          | Secret containing the PostgreSQL URL                                            | `""`                         |
| `database.existingSecretKey`       | PostgreSQL URL key                                                              | `database-url`               |
| `networkPolicy.enabled`            | Deny workload ingress by default; allow web and same-release web-to-API traffic | `true`                       |

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
  --set-string security.existingSecret=kubequeue-security \
  --set-string browser.publicURL=https://queue.example.com \
  --set-string browser.origin=https://queue.example.com
```

Cluster-wide mode grants the worker cluster-scoped Job mutation authority. KubeQueue always excludes
the release namespace and the Kubernetes system namespaces even if they are omitted from values.

### Development-only local seed

For a disposable development or test installation only, set both
`runtime.environment=development` (or `test`) and `development.localAdminSeed=true`. This
idempotently creates or re-enables a local `admin` account with password `admin` and logs a
prominent warning. Production mode rejects the flag during chart validation and API startup.
Never enable it in a shared environment.

### Secret rotation

Changing a Secret key reference changes the pod-template checksum and rolls the affected workload.
Kubernetes does not expose external Secret contents to Helm, so after changing values inside the
same Secret, run `kubectl rollout restart` for the API and web Deployments. Keep old encryption keys
available for any migration procedure documented for the release; replacing the credential
encryption key without migration makes stored provider credentials unreadable.

## Upgrade and rollback

The pre-upgrade hook applies forward-only database migrations before workloads roll out. v0.1.x
does not guarantee that an older binary can use a migrated database. Back up PostgreSQL before any
upgrade. If an upgrade fails, restore the database backup before running `helm rollback`.

## Uninstall

```bash
helm uninstall kubequeue --namespace kubequeue
```

The external PostgreSQL database and manually created Secrets are not deleted.
