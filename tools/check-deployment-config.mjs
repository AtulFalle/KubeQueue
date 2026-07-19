import { spawnSync } from 'node:child_process';

const chart = 'deploy/helm/kubequeue';
const baseArgs = [
  'compose',
  'run',
  '--rm',
  '--no-deps',
  'helm',
  'helm',
  'template',
  'kubequeue',
  chart,
  '--set-string',
  'database.url=postgres://example',
  '--set-string',
  'security.existingSecret=test-security',
  '--set-string',
  'browser.publicURL=http://localhost:3000',
  '--set-string',
  'browser.origin=http://localhost:3000',
];

function render(extra = [], allowFailure = false) {
  const result = spawnSync('docker', [...baseArgs, ...extra], {
    encoding: 'utf8',
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  if (result.error) throw result.error;
  if (!allowFailure && result.status !== 0) {
    throw new Error(result.stderr || `helm template exited with ${result.status}`);
  }
  return result;
}

const rendered = render().stdout;
for (const required of [
  'name: KUBEQUEUE_SESSION_DIGEST_KEY',
  'name: KUBEQUEUE_SESSION_ENCRYPTION_KEY',
  'name: KUBEQUEUE_BFF_INTERNAL_KEY',
  'name: KUBEQUEUE_SERVICE_ACCOUNT_DIGEST_KEY',
  'name: KUBEQUEUE_PUBLIC_URL',
  'checksum/security-reference:',
]) {
  if (!rendered.includes(required)) {
    throw new Error(`Rendered chart is missing ${required}`);
  }
}
for (const forbidden of ['KUBEQUEUE_ADMIN_TOKEN', 'KUBEQUEUE_OIDC_']) {
  if (rendered.includes(forbidden)) {
    throw new Error(`Rendered chart contains legacy static configuration: ${forbidden}`);
  }
}

const documents = rendered.split(/\n---\s*\n/);
const deploymentSections = Object.fromEntries(
  ['api', 'web', 'worker'].map((component) => {
    const document = documents.find(
      (candidate) =>
        candidate.includes('kind: Deployment') &&
        candidate.includes(`name: kubequeue-kubequeue-${component}`),
    );
    if (!document) throw new Error(`Rendered chart is missing ${component} Deployment`);
    return [component, document];
  }),
);
for (const component of ['api', 'web']) {
  if (!deploymentSections[component].includes('automountServiceAccountToken: false')) {
    throw new Error(`${component} must not mount a Kubernetes service-account token`);
  }
}
if (!deploymentSections.worker.includes('automountServiceAccountToken: true')) {
  throw new Error('worker must retain its Kubernetes service-account token');
}

const rejectedSeed = render(['--set', 'development.localAdminSeed=true'], true);
if (rejectedSeed.status === 0) {
  throw new Error('Production chart accepted the development local-admin seed');
}
