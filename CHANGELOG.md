# Changelog

All notable changes to KubeQueue are documented in this file.

The project follows [Semantic Versioning](https://semver.org/). Releases before `1.0.0` may change
configuration, storage, and APIs without backward compatibility.

## [Unreleased]

## [0.1.3] - 2026-07-18

## [0.1.0] - 2026-07-18

First packaged developer preview.

### Added

- Queueing and lifecycle control for standard Kubernetes Jobs.
- Priority, delayed execution, concurrency limits, retries, and immutable attempt history.
- PostgreSQL-backed API and worker processes with Kubernetes reconciliation.
- Next.js administrative dashboard and generated TypeScript API client.
- Versioned API, worker, and web images published to GitHub Container Registry.
- OCI and downloadable Helm chart for cluster-private preview installations.

### Preview limitations

- The dashboard is a single-administrator surface and must not be exposed directly to the internet.
- PostgreSQL backup, restore, upgrade, and rollback compatibility are not yet guaranteed.
- Multi-user identity, team RBAC, high-availability guidance, and production support are deferred.

[Unreleased]: https://github.com/AtulFalle/KubeQueue/compare/v0.1.3...HEAD
[0.1.3]: https://github.com/AtulFalle/KubeQueue/releases/tag/v0.1.3
[0.1.0]: https://github.com/AtulFalle/KubeQueue/releases/tag/v0.1.0
