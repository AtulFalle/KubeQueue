import { existsSync, mkdirSync, rmSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';
import { spawn, spawnSync } from 'node:child_process';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const action = process.argv[2] ?? 'install';
const cluster = process.env.KUBEQUEUE_ACCEPTANCE_CLUSTER ?? 'kubequeue-acceptance';
const context = `kind-${cluster}`;
const outputDirectory = resolve(root, 'dist', 'chart-acceptance');
const candidateChart = resolve(outputDirectory, 'kubequeue-0.0.0-acceptance.tgz');
const keepCluster = process.env.KUBEQUEUE_ACCEPTANCE_KEEP_CLUSTER === 'true';
const pnpm = process.platform === 'win32' ? 'pnpm.cmd' : 'pnpm';
const images = {
  api: 'kubequeue-api:acceptance',
  worker: 'kubequeue-worker:acceptance',
  web: 'kubequeue-web:acceptance',
};

function execute(command, args, options = {}) {
  const result = spawnSync(command, args, {
    cwd: root,
    encoding: 'utf8',
    env: { ...process.env, ...options.env },
    stdio: options.capture ? ['ignore', 'pipe', 'pipe'] : 'inherit',
  });

  if (result.error) {
    throw new Error(`Unable to run ${command}: ${result.error.message}`);
  }
  if (!options.allowFailure && result.status !== 0) {
    const detail = options.capture ? `\n${result.stderr || result.stdout}` : '';
    throw new Error(`${command} exited with status ${result.status}${detail}`);
  }

  return result;
}

function requireTools() {
  for (const [command, args] of [
    ['docker', ['info']],
    ['kind', ['version']],
    ['kubectl', ['version', '--client']],
    ['helm', ['version']],
  ]) {
    execute(command, args);
  }
}

function deleteCluster() {
  execute('kind', ['delete', 'cluster', '--name', cluster], { allowFailure: true });
}

function buildCandidate() {
  rmSync(outputDirectory, { recursive: true, force: true });
  mkdirSync(outputDirectory, { recursive: true });
  execute('helm', [
    'package',
    'deploy/helm/kubequeue',
    '--version',
    '0.0.0-acceptance',
    '--app-version',
    'acceptance',
    '--destination',
    outputDirectory,
  ]);

  execute('docker', [
    'build',
    '--file',
    'apps/control-plane/Dockerfile',
    '--build-arg',
    'TARGET=api',
    '--tag',
    images.api,
    '.',
  ]);
  execute('docker', [
    'build',
    '--file',
    'apps/control-plane/Dockerfile',
    '--build-arg',
    'TARGET=worker',
    '--tag',
    images.worker,
    '.',
  ]);
  execute('docker', ['build', '--file', 'apps/web/Dockerfile', '--tag', images.web, '.']);
}

function createCluster() {
  deleteCluster();
  execute('kind', ['create', 'cluster', '--name', cluster, '--wait', '120s']);
  execute('kind', [
    'load',
    'docker-image',
    '--name',
    cluster,
    images.api,
    images.worker,
    images.web,
  ]);
}

function kubectl(args, options) {
  return execute('kubectl', ['--context', context, ...args], options);
}

function helm(args, options) {
  return execute('helm', ['--kube-context', context, ...args], options);
}

function prepareNamespace(namespace) {
  kubectl(['create', 'namespace', namespace]);
  kubectl(['apply', '--namespace', namespace, '--filename', 'deploy/kind/postgres.yaml']);
  kubectl(['rollout', 'status', '--namespace', namespace, 'deployment/postgres', '--timeout=180s']);
  kubectl([
    'create',
    'secret',
    'generic',
    'acceptance-config',
    '--namespace',
    namespace,
    '--from-literal=database-url=postgres://kubequeue:kubequeue@postgres:5432/kubequeue?sslmode=disable',
    '--from-literal=admin-token=acceptance-only-token',
  ]);
}

function imageArguments() {
  return [
    '--set-string',
    'api.image.repository=kubequeue-api',
    '--set-string',
    'api.image.tag=acceptance',
    '--set-string',
    'api.image.pullPolicy=Never',
    '--set-string',
    'worker.image.repository=kubequeue-worker',
    '--set-string',
    'worker.image.tag=acceptance',
    '--set-string',
    'worker.image.pullPolicy=Never',
    '--set-string',
    'web.image.repository=kubequeue-web',
    '--set-string',
    'web.image.tag=acceptance',
    '--set-string',
    'web.image.pullPolicy=Never',
  ];
}

function installArguments(release, chart, namespace) {
  return [
    'upgrade',
    '--install',
    release,
    chart,
    '--namespace',
    namespace,
    '--atomic',
    '--wait',
    '--wait-for-jobs',
    '--timeout',
    '5m',
    '--set-string',
    'database.existingSecret=acceptance-config',
    '--set-string',
    'config.adminTokenExistingSecret=acceptance-config',
  ];
}

function waitForWorkloads(release, namespace) {
  for (const component of ['api', 'web', 'worker']) {
    kubectl([
      'rollout',
      'status',
      '--namespace',
      namespace,
      `deployment/${release}-kubequeue-${component}`,
      '--timeout=300s',
    ]);
  }
}

function runHelmTests(release, namespace) {
  helm(['test', release, '--namespace', namespace, '--logs', '--timeout', '2m']);
}

function restartWorker(release, namespace) {
  kubectl([
    'rollout',
    'restart',
    '--namespace',
    namespace,
    `deployment/${release}-kubequeue-worker`,
  ]);
  kubectl([
    'rollout',
    'status',
    '--namespace',
    namespace,
    `deployment/${release}-kubequeue-worker`,
    '--timeout=300s',
  ]);
}

function runLifecycleTests(release, namespace, expectExistingJobs = false) {
  const port = '33000';
  const portForward = spawn(
    'kubectl',
    [
      '--context',
      context,
      '--namespace',
      namespace,
      'port-forward',
      `service/${release}-kubequeue-web`,
      `${port}:3000`,
    ],
    { cwd: root, stdio: 'inherit' },
  );
  try {
    let ready = false;
    for (let attempt = 0; attempt < 30; attempt += 1) {
      const result = execute(
        process.execPath,
        [
          '-e',
          `fetch('http://127.0.0.1:${port}').then(r=>process.exit(r.ok?0:1)).catch(()=>process.exit(1))`,
        ],
        { allowFailure: true, capture: true },
      );
      if (result.status === 0) {
        ready = true;
        break;
      }
      Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, 1000);
    }
    if (!ready) {
      throw new Error('Web port-forward did not become ready');
    }
    execute(process.execPath, [
      '-e',
      `fetch('http://127.0.0.1:${port}/api/v1/jobs').then(async r=>{if(!r.ok)process.exit(1);const body=await r.json();const items=body.items??[];process.exit(items.some(j=>j.name?.includes('migrate'))||(${expectExistingJobs}&&items.length===0)?1:0)}).catch(()=>process.exit(1))`,
    ]);
    execute(pnpm, ['nx', 'run', 'web:e2e', '--', 'e2e/lifecycle.spec.ts'], {
      env: { KUBEQUEUE_E2E_BASE_URL: `http://127.0.0.1:${port}` },
    });
  } finally {
    portForward.kill();
  }
}

function assertCanI(namespace, serviceAccountNamespace, serviceAccount, expected) {
  const result = kubectl(
    [
      'auth',
      'can-i',
      'list',
      'jobs',
      '--namespace',
      namespace,
      `--as=system:serviceaccount:${serviceAccountNamespace}:${serviceAccount}`,
    ],
    { allowFailure: true, capture: true },
  );
  const actual = result.stdout.trim();
  if (actual !== expected) {
    throw new Error(
      `Expected service account ${serviceAccountNamespace}/${serviceAccount} can-i result in ${namespace} to be ${expected}, received ${actual}`,
    );
  }
}

function testSelectedMode(chart = candidateChart, upgrade = false) {
  const release = 'selected';
  const namespace = 'kubequeue-selected';
  const watchedNamespace = 'batch-selected';
  const deniedNamespace = 'batch-denied';

  for (const name of [namespace, watchedNamespace, deniedNamespace]) {
    if (name === namespace) {
      prepareNamespace(name);
    } else {
      kubectl(['create', 'namespace', name]);
    }
  }

  const baseArguments = installArguments(release, chart, namespace);
  helm([
    ...baseArguments,
    '--set-string',
    'watch.mode=selected',
    '--set-json',
    `watch.namespaces=["default","${watchedNamespace}"]`,
    ...(upgrade ? [] : imageArguments()),
  ]);

  if (upgrade) {
    waitForWorkloads(release, namespace);
    runLifecycleTests(release, namespace);
    helm([
      ...installArguments(release, candidateChart, namespace),
      '--set-string',
      'watch.mode=selected',
      '--set-json',
      `watch.namespaces=["default","${watchedNamespace}"]`,
      ...imageArguments(),
    ]);
  }

  waitForWorkloads(release, namespace);
  kubectl(['get', 'role', `${release}-kubequeue-worker`, '--namespace', watchedNamespace]);
  assertCanI(watchedNamespace, namespace, `${release}-kubequeue`, 'yes');
  assertCanI(deniedNamespace, namespace, `${release}-kubequeue`, 'no');
  runHelmTests(release, namespace);
  restartWorker(release, namespace);
  runLifecycleTests(release, namespace, upgrade);
  helm(['uninstall', release, '--namespace', namespace, '--wait']);
  kubectl(['get', 'secret', 'acceptance-config', '--namespace', namespace]);
  kubectl(['delete', 'namespace', namespace, watchedNamespace, deniedNamespace, '--wait=false']);
}

function testAllMode() {
  const release = 'all';
  const namespace = 'kubequeue-all';
  const observedNamespace = 'batch-all';
  prepareNamespace(namespace);
  kubectl(['create', 'namespace', observedNamespace]);

  helm([
    ...installArguments(release, candidateChart, namespace),
    '--set-string',
    'watch.mode=all',
    '--set',
    'rbac.allowClusterWide=true',
    ...imageArguments(),
  ]);

  waitForWorkloads(release, namespace);
  const roleResult = kubectl(
    [
      'get',
      'clusterrole',
      '--selector',
      `app.kubernetes.io/instance=${release}`,
      '--output',
      'name',
    ],
    { capture: true },
  );
  if (!roleResult.stdout.includes('clusterrole.rbac.authorization.k8s.io/')) {
    throw new Error(`No cluster-wide worker role was created for ${release}`);
  }
  assertCanI(observedNamespace, namespace, `${release}-kubequeue`, 'yes');

  const excluded = kubectl(
    [
      'get',
      'configmap',
      `${release}-kubequeue-runtime`,
      '--namespace',
      namespace,
      '--output',
      'jsonpath={.data.KUBEQUEUE_EXCLUDED_NAMESPACES}',
    ],
    { capture: true },
  ).stdout;
  for (const expected of [namespace, 'kube-system', 'kube-public', 'kube-node-lease']) {
    if (!excluded.split(',').includes(expected)) {
      throw new Error(`All-mode effective scope did not exclude ${expected}`);
    }
  }

  runHelmTests(release, namespace);
  if (!keepCluster) {
    helm(['uninstall', release, '--namespace', namespace, '--wait']);
    kubectl(['delete', 'namespace', namespace, observedNamespace, '--wait=false']);
  }
}

function previousChart() {
  const input = process.env.KUBEQUEUE_PREVIOUS_CHART;
  if (!input) {
    throw new Error(
      'KUBEQUEUE_PREVIOUS_CHART must point to a reliable previous-release .tgz for upgrade acceptance',
    );
  }
  const chart = resolve(root, input);
  if (!chart.endsWith('.tgz') || !existsSync(chart)) {
    throw new Error(`KUBEQUEUE_PREVIOUS_CHART does not reference an existing .tgz: ${chart}`);
  }
  return chart;
}

if (!['install', 'upgrade', 'teardown'].includes(action)) {
  throw new Error('Usage: node tools/deploy-acceptance.mjs <install|upgrade|teardown>');
}

if (action === 'teardown') {
  requireTools();
  deleteCluster();
  process.exit(0);
}

const upgradeChart = action === 'upgrade' ? previousChart() : undefined;
requireTools();

try {
  buildCandidate();
  createCluster();
  if (upgradeChart) {
    testSelectedMode(upgradeChart, true);
  } else {
    testSelectedMode();
    testAllMode();
  }
} finally {
  if (keepCluster) {
    console.log(`Keeping kind cluster ${cluster} for inspection.`);
  } else {
    deleteCluster();
  }
}
