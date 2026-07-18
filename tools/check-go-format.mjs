import { spawnSync } from 'node:child_process';

let files;

if (process.env.KUBEQUEUE_FORMAT_FILES) {
  files = JSON.parse(process.env.KUBEQUEUE_FORMAT_FILES).filter((file) =>
    file.startsWith('apps/control-plane/'),
  );
} else {
  const trackedFiles = spawnSync(
    'git',
    ['ls-files', '-z', '--', ':(glob)apps/control-plane/**/*.go'],
    { encoding: 'buffer' },
  );

  if (trackedFiles.error) {
    throw trackedFiles.error;
  }

  if (trackedFiles.status !== 0) {
    process.stderr.write(trackedFiles.stderr);
    process.exit(trackedFiles.status ?? 1);
  }

  files = trackedFiles.stdout.toString('utf8').split('\0').filter(Boolean);
}

if (files.length === 0) {
  process.exit(0);
}

const gofmt = spawnSync('gofmt', ['-l', ...files], { encoding: 'utf8' });

if (gofmt.error) {
  throw gofmt.error;
}

if (gofmt.status !== 0) {
  process.stderr.write(gofmt.stderr);
  process.exit(gofmt.status ?? 1);
}

if (gofmt.stdout.trim()) {
  process.stdout.write(gofmt.stdout);
  process.exit(1);
}
