import { spawnSync } from 'node:child_process';
import { createRequire } from 'node:module';

const args = process.argv.slice(2);
const staged = args.includes('--staged');

function option(name) {
  const inline = args.find((argument) => argument.startsWith(`${name}=`));
  if (inline) {
    return inline.slice(name.length + 1);
  }

  const index = args.indexOf(name);
  return index >= 0 ? args[index + 1] : undefined;
}

const base = option('--base');
const head = option('--head');

if (staged === Boolean(base || head) || (!staged && (!base || !head))) {
  console.error('Usage: node tools/check-changed.mjs --staged | --base <sha> --head <sha>');
  process.exit(2);
}

const diffArgs = staged
  ? ['diff', '--cached', '--name-only', '--diff-filter=ACMR', '-z']
  : ['diff', '--name-only', '--diff-filter=ACMR', '-z', base, head];
const changed = spawnSync('git', diffArgs, { encoding: 'buffer' });

if (changed.error) {
  throw changed.error;
}

if (changed.status !== 0) {
  process.stderr.write(changed.stderr);
  process.exit(changed.status ?? 1);
}

const files = changed.stdout.toString('utf8').split('\0').filter(Boolean);

if (files.length === 0) {
  console.log('No changed files to check.');
  process.exit(0);
}

const nxCli = createRequire(import.meta.url).resolve('nx/bin/nx.js');
const fileOption = `--files=${files.join(',')}`;

function runNx(nxArgs, extraEnv = {}) {
  const result = spawnSync(process.execPath, [nxCli, ...nxArgs], {
    env: { ...process.env, ...extraEnv },
    stdio: 'inherit',
  });

  if (result.error) {
    throw result.error;
  }

  if (result.status !== 0) {
    process.exit(result.status ?? 1);
  }
}

runNx(['format:check', fileOption]);
runNx(['affected', '-t', 'lint,format-check', fileOption], {
  KUBEQUEUE_FORMAT_FILES: JSON.stringify(files.filter((file) => file.endsWith('.go'))),
});
