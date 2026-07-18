# Release process

KubeQueue releases are built from immutable tags by `.github/workflows/release.yml`. Do not upload
locally built images or charts to an existing release.

## Prepare a release

Run the **Prepare KubeQueue Release** workflow and provide:

- `target_branch`: the branch to release from and receive the release pull request, such as
  `master` or a maintained hotfix branch.
- `release_tag`: the unused semantic version tag, including the `v` prefix, such as `v0.1.3`.

The workflow updates package manifests, Helm chart and image versions, installation documentation,
and changelog links. It then opens `release/<tag>` against the selected target branch and dispatches
CI for the generated commit. It refuses to reuse an existing tag or release branch.

Review the generated release pull request:

1. Confirm the target branch and version are correct.
2. Confirm the generated changelog section contains the notable changes collected under
   `Unreleased`.
3. Run `pnpm format:check` and `pnpm check`.
4. Run PostgreSQL integration tests against an empty database with
   `KUBEQUEUE_TEST_POSTGRES_URL` configured and Nx caching disabled.
5. Validate the changed workflows with `actionlint`.
6. Review dependency and container scan results and resolve all critical vulnerabilities.
7. Merge only after required checks pass.

## Publish

Merging a generated release pull request creates an annotated tag at its merge commit and
dispatches `.github/workflows/release.yml`. The publication workflow validates the complete tagged
repository, runs PostgreSQL tests, builds and smoke-tests the production images, publishes
versioned and commit-addressed images with provenance and SBOMs, publishes the OCI chart, and
creates a prerelease with the chart archive and checksum.

Do not create the tag manually before the release pull request merges. Tags are immutable release
inputs, so version changes made after tagging cannot become part of that release.

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

If validation or publication fails after the tag is created, do not reuse the tag after changing
source. Delete incomplete preview artifacts if necessary, fix the selected release branch through
a pull request, and prepare a new version.
