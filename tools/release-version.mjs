import { readFileSync, writeFileSync } from 'node:fs';

const [, , command, requestedVersion] = process.argv;
const semverPattern =
  /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$/;

if (!['set', 'check'].includes(command) || !requestedVersion) {
  throw new Error('Usage: node tools/release-version.mjs <set|check> <version>');
}
if (!semverPattern.test(requestedVersion)) {
  throw new Error(`Invalid semantic version: ${requestedVersion}`);
}

const jsonManifests = ['package.json', 'apps/web/package.json', 'packages/api-client/package.json'];
const chartPath = 'deploy/helm/kubequeue/Chart.yaml';
const valuesPath = 'deploy/helm/kubequeue/values.yaml';
const versionedDocs = ['README.md', 'deploy/helm/kubequeue/README.md'];
const changelogPath = 'CHANGELOG.md';

function read(path) {
  return readFileSync(path, 'utf8');
}

function write(path, content) {
  writeFileSync(path, content);
}

function replaceRequired(content, pattern, replacement, description) {
  if (!pattern.test(content)) {
    throw new Error(`Could not find ${description}`);
  }
  return content.replace(pattern, replacement);
}

function manifestVersion(path) {
  return JSON.parse(read(path)).version;
}

function setVersion() {
  const previousVersion = manifestVersion('package.json');

  for (const path of jsonManifests) {
    const manifest = JSON.parse(read(path));
    manifest.version = requestedVersion;
    write(path, `${JSON.stringify(manifest, null, 2)}\n`);
  }

  let chart = read(chartPath);
  chart = replaceRequired(
    chart,
    /^version:\s*'?[^'\s]+'?$/m,
    `version: ${requestedVersion}`,
    `${chartPath} version`,
  );
  chart = replaceRequired(
    chart,
    /^appVersion:\s*'?[^'\s]+'?$/m,
    `appVersion: '${requestedVersion}'`,
    `${chartPath} appVersion`,
  );
  write(chartPath, chart);

  const values = read(valuesPath);
  const imageTagPattern = /^    tag:\s*.+$/gm;
  const imageTags = [...values.matchAll(imageTagPattern)];
  if (imageTags.length !== 3) {
    throw new Error(`Expected 3 image tags in ${valuesPath}; found ${imageTags.length}`);
  }
  write(valuesPath, values.replace(imageTagPattern, `    tag: ${requestedVersion}`));

  if (previousVersion !== requestedVersion) {
    for (const path of versionedDocs) {
      const content = read(path);
      if (!content.includes(previousVersion)) {
        throw new Error(`Could not find ${previousVersion} in ${path}`);
      }
      write(path, content.replaceAll(previousVersion, requestedVersion));
    }

    let changelog = read(changelogPath);
    if (!changelog.includes(`## [${requestedVersion}]`)) {
      changelog = replaceRequired(
        changelog,
        /## \[Unreleased\]\r?\n/,
        `## [Unreleased]\n\n## [${requestedVersion}] - ${new Date().toISOString().slice(0, 10)}\n`,
        `${changelogPath} Unreleased heading`,
      );
      changelog = replaceRequired(
        changelog,
        /^\[Unreleased\]: .+$/m,
        `[Unreleased]: https://github.com/AtulFalle/KubeQueue/compare/v${requestedVersion}...HEAD`,
        `${changelogPath} Unreleased link`,
      );
      const firstReleaseLink = /^\[(\d+\.\d+\.\d+(?:-[^\]]+)?)\]: .+$/m;
      changelog = replaceRequired(
        changelog,
        firstReleaseLink,
        `[${requestedVersion}]: https://github.com/AtulFalle/KubeQueue/releases/tag/v${requestedVersion}\n$&`,
        `${changelogPath} release links`,
      );
      write(changelogPath, changelog);
    }
  }
}

function checkVersion() {
  const mismatches = [];

  for (const path of jsonManifests) {
    const actual = manifestVersion(path);
    if (actual !== requestedVersion) {
      mismatches.push(`${path} is ${actual}; expected ${requestedVersion}`);
    }
  }

  const chart = read(chartPath);
  for (const [field, pattern] of [
    ['version', /^version:\s*'?([^'\s]+)'?$/m],
    ['appVersion', /^appVersion:\s*'?([^'\s]+)'?$/m],
  ]) {
    const actual = chart.match(pattern)?.[1];
    if (actual !== requestedVersion) {
      mismatches.push(
        `${chartPath} ${field} is ${actual ?? 'missing'}; expected ${requestedVersion}`,
      );
    }
  }

  const values = read(valuesPath);
  const imageTags = [...values.matchAll(/^    tag:\s*(.+)$/gm)].map((match) => match[1]);
  if (imageTags.length !== 3) {
    mismatches.push(`${valuesPath} has ${imageTags.length} image tags; expected 3`);
  }
  imageTags.forEach((actual, index) => {
    if (actual !== requestedVersion) {
      mismatches.push(
        `${valuesPath} image tag ${index + 1} is ${actual}; expected ${requestedVersion}`,
      );
    }
  });

  for (const path of versionedDocs) {
    if (!read(path).includes(requestedVersion)) {
      mismatches.push(`${path} does not reference ${requestedVersion}`);
    }
  }

  const changelog = read(changelogPath);
  if (!changelog.includes(`## [${requestedVersion}]`)) {
    mismatches.push(`${changelogPath} has no ${requestedVersion} release heading`);
  }
  if (
    !changelog.includes(
      `[Unreleased]: https://github.com/AtulFalle/KubeQueue/compare/v${requestedVersion}...HEAD`,
    )
  ) {
    mismatches.push(`${changelogPath} Unreleased link does not start at v${requestedVersion}`);
  }

  if (mismatches.length > 0) {
    throw new Error(mismatches.join('\n'));
  }
}

if (command === 'set') {
  setVersion();
}
checkVersion();
