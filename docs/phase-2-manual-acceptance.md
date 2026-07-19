# Phase 2 manual acceptance

Use this checklist before beginning Phase 3. Run it from a clean working tree with Docker, kind,
kubectl, Helm, Node.js, and pnpm available.

## 1. Clean packaged-chart installation

Run the automated acceptance target first:

```bash
pnpm nx run deploy:chart-acceptance
```

It must complete without errors. This packages the chart, builds and loads the three application
images, validates selected-namespace and all-namespace RBAC, runs the Helm readiness test, restarts
the worker, exercises lifecycle convergence through Playwright, verifies migration exclusion, and
checks that a manually created Secret survives Helm uninstall.

## 2. Inspect a running installation

Keep the final all-namespace installation for manual inspection:

```bash
KUBEQUEUE_ACCEPTANCE_KEEP_CLUSTER=true pnpm nx run deploy:chart-acceptance
kubectl --context kind-kubequeue-acceptance -n kubequeue-all get pods,deployments,services
helm --kube-context kind-kubequeue-acceptance -n kubequeue-all test all --logs
```

All three Deployments must be available, the PostgreSQL Pod must be ready, and the Helm test must
pass. The migration hook must not appear in KubeQueue inventory.

Port-forward the dashboard:

```bash
kubectl --context kind-kubequeue-acceptance -n kubequeue-all \
  port-forward service/all-kubequeue-web 3000:3000
```

Open <http://127.0.0.1:3000> and verify:

1. System status reports the API, database, worker, informer, and Kubernetes authorization as ready.
2. The effective mode is `all`; system and release namespaces are excluded.
3. Submit a short Job in `default` and wait for it to complete.
4. Submit a long-running Job and pause, resume, then terminate it. Each action must remain pending
   until Kubernetes convergence is observed.
5. Retry the terminated Job and confirm a new attempt is created without erasing the first attempt.
6. Submit two delayed Jobs, reorder them, refresh the page, and confirm the order persists.
7. Create an unmarked Kubernetes Job manually and confirm it appears as `OBSERVED` with lifecycle
   controls disabled.
8. Delete that Kubernetes Job and confirm its record becomes `MISSING`, not `CANCELLED`.
9. Review Settings and confirm no resource versions, credentials, database URLs, or full manifests
   appear in operational errors.

## 3. Previous-release upgrade

Provide a trusted previous release chart package:

```bash
KUBEQUEUE_PREVIOUS_CHART=path/to/kubequeue-previous.tgz \
  pnpm nx run deploy:chart-upgrade-acceptance
```

The migration hook and candidate rollout must succeed. Existing PostgreSQL state must remain
readable after the upgrade, and API and worker Pods must reject an incompatible or dirty schema
instead of modifying it during startup.

## 4. Uninstall retention

After manual inspection:

```bash
helm --kube-context kind-kubequeue-acceptance -n kubequeue-all uninstall all --wait
kubectl --context kind-kubequeue-acceptance -n kubequeue-all get secret acceptance-config
kubectl --context kind-kubequeue-acceptance -n kubequeue-all get deployment postgres
pnpm nx run deploy:chart-acceptance-teardown
```

The manually created Secret and external PostgreSQL Deployment must still exist after Helm
uninstall. The final teardown command removes the disposable kind cluster.

Phase 3 can begin only after the clean-install and upgrade targets pass and the manual workflow
shows no unresolved Phase 2 acceptance failure.
