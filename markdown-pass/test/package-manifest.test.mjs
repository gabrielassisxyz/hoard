import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { test } from 'node:test';
import { repositoryRoot, runCommand } from '../test-support/harness.mjs';

const readJson = async (relativePath) => JSON.parse(await readFile(new URL(relativePath, import.meta.url), 'utf8'));

test('package.json is private, an ES module, needs Node 22.15, and runs the tests with node --test', async () => {
  const manifest = await readJson('../package.json');

  assert.equal(manifest.private, true);
  assert.equal(manifest.type, 'module');
  assert.equal(manifest.engines.node, '>=22.15');
  assert.equal(manifest.scripts.test, 'node --test');
});

test('it declares exactly two dependencies, each pinned to one version and no range', async () => {
  const manifest = await readJson('../package.json');

  assert.deepEqual(manifest.dependencies, { defuddle: '0.19.6', linkedom: '0.18.12' });
  for (const other of ['devDependencies', 'optionalDependencies', 'peerDependencies']) {
    assert.equal(manifest[other], undefined, `no ${other}`);
  }
});

test('package-lock.json agrees with package.json and resolves the two pinned versions', async () => {
  const manifest = await readJson('../package.json');
  const lock = await readJson('../package-lock.json');

  assert.deepEqual(lock.packages[''].dependencies, manifest.dependencies);
  assert.equal(lock.packages['node_modules/defuddle'].version, '0.19.6');
  assert.equal(lock.packages['node_modules/linkedom'].version, '0.18.12');
});

test('the lockfile is tracked by git and node_modules is ignored by it', async () => {
  const tracked = await runCommand('git', ['ls-files', '--error-unmatch', 'markdown-pass/package-lock.json'], { cwd: repositoryRoot });
  assert.equal(tracked.status, 0, `package-lock.json is not tracked: ${tracked.stderr}`);

  const lockIgnored = await runCommand('git', ['check-ignore', '-q', 'markdown-pass/package-lock.json'], { cwd: repositoryRoot });
  assert.equal(lockIgnored.status, 1, 'package-lock.json must not be ignored');

  for (const path of ['markdown-pass/node_modules', 'markdown-pass/node_modules/defuddle/package.json']) {
    const ignored = await runCommand('git', ['check-ignore', '-q', path], { cwd: repositoryRoot });
    assert.equal(ignored.status, 0, `${path} must be ignored`);
  }
});
