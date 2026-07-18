import { mkdirSync, writeFileSync } from 'node:fs';
import { spawnSync } from 'node:child_process';

const [, , command] = process.argv;
const version = process.env.KUBEQUEUE_RELEASE_VERSION;
const semverPattern =
  /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$/;

if (!['validate', 'package'].includes(command)) {
  throw new Error('Usage: node tools/release-helm.mjs <validate|package>');
}
if (!version || !semverPattern.test(version)) {
  throw new Error('KUBEQUEUE_RELEASE_VERSION must be a semantic version without a v prefix');
}

const chart = 'deploy/helm/kubequeue';

function runHelm(args, captureOutput = false) {
  let result = spawnSync('helm', args, {
    encoding: captureOutput ? 'utf8' : undefined,
    stdio: captureOutput ? ['ignore', 'pipe', 'inherit'] : 'inherit',
  });

  if (result.error?.code === 'ENOENT') {
    result = spawnSync('docker', ['compose', 'run', '--rm', '--no-deps', 'helm', 'helm', ...args], {
      encoding: captureOutput ? 'utf8' : undefined,
      stdio: captureOutput ? ['ignore', 'pipe', 'inherit'] : 'inherit',
    });
  }

  if (result.error) {
    throw result.error;
  }
  if (result.status !== 0) {
    process.exit(result.status ?? 1);
  }

  return result.stdout;
}

function releaseArgs() {
  return ['--version', version, '--app-version', version];
}

if (command === 'validate') {
  const outputDirectory = 'dist/release';
  mkdirSync(outputDirectory, { recursive: true });

  runHelm([
    'lint',
    chart,
    '--set-string',
    'database.url=postgres://example',
    '--set-string',
    'config.adminToken=release-validation-token',
  ]);

  const rendered = runHelm(
    [
      'template',
      'kubequeue',
      chart,
      '--set-string',
      'database.url=postgres://example',
      '--set-string',
      'config.adminToken=release-validation-token',
      '--set-string',
      `api.image.tag=${version}`,
      '--set-string',
      `worker.image.tag=${version}`,
      '--set-string',
      `web.image.tag=${version}`,
    ],
    true,
  );
  writeFileSync(`${outputDirectory}/kubequeue-rendered.yaml`, rendered);

  runHelm(['package', chart, ...releaseArgs(), '--destination', outputDirectory]);
} else {
  mkdirSync('dist', { recursive: true });
  runHelm(['package', chart, ...releaseArgs(), '--destination', 'dist']);
}
