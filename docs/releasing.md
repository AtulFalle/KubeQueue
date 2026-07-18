# Release process

KubeQueue releases are built from immutable tags by `.github/workflows/release.yml`. Do not upload
locally built images or charts to an existing release.

## v0.1.x preflight

1. Confirm the release commit is on `master` and the working tree is clean.
2. Confirm `package.json`, `apps/web/package.json`, `packages/api-client/package.json`, and both
   version fields in `deploy/helm/kubequeue/Chart.yaml` match the tag without its `v` prefix.
3. Update `CHANGELOG.md` and verify the README and chart installation commands use that version.
4. Run `pnpm format:check` and `pnpm check`.
5. Run PostgreSQL integration tests against an empty database with
   `KUBEQUEUE_TEST_POSTGRES_URL` configured and Nx caching disabled.
6. Validate `.github/workflows/ci.yml` and `.github/workflows/release.yml` with `actionlint`.
7. Review dependency and container scan results and resolve all critical vulnerabilities.
8. Merge the release-preparation pull request and wait for green `master` CI.

## Publish

Create an annotated tag from the verified `master` commit and push only that tag:

```bash
git switch master
git pull --ff-only
git tag -a v0.1.0 -m "KubeQueue v0.1.0"
git push origin v0.1.0
```

The tag workflow validates the complete repository, runs PostgreSQL tests, builds and smoke-tests
the production images, publishes versioned and commit-addressed images with provenance and SBOMs,
publishes the OCI chart, and creates a prerelease with the chart archive and checksum.

## Verify

1. Confirm the GitHub release is marked as a prerelease and contains the chart and checksum.
2. Confirm the API, worker, web, and chart packages are associated with this repository and visible
   publicly. New GHCR packages may require a one-time visibility change in GitHub package settings.
3. Pull all three versioned images and verify their provenance/SBOM attestations.
4. Install `oci://ghcr.io/atulfalle/charts/kubequeue` into a clean namespace using a fresh
   PostgreSQL database and existing Kubernetes Secrets.
5. Port-forward the dashboard, submit one Job, confirm it reaches a terminal state, and uninstall
   the chart.
6. Verify the database and manually created Secrets remain after uninstall.

If validation or publication fails, do not reuse the tag after changing source. Delete incomplete
preview artifacts if necessary, fix the release commit through a pull request, increment the
version, and publish a new tag.
