import { spawnSync } from 'node:child_process';

const result = spawnSync(
  'docker',
  [
    'run',
    '--rm',
    '--mount',
    `type=bind,source=${process.cwd()},target=/repo`,
    '--workdir',
    '/repo',
    'rhysd/actionlint:1.7.7',
  ],
  { stdio: 'inherit' },
);

if (result.error) {
  throw result.error;
}

process.exit(result.status ?? 1);
