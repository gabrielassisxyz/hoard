import assert from 'node:assert/strict';
import { existsSync } from 'node:fs';
import { chmod, copyFile, mkdir, readFile, writeFile } from 'node:fs/promises';
import { test } from 'node:test';
import { repositoryRoot, runCommand, scratchDirectory } from '../test-support/harness.mjs';

// bin/ci is run for real, in a directory of its own, with stand-ins for the tools it calls.
// The stand-ins write down what they were asked to do and where, and fail when told to, so
// the order of the gate's steps and what a failure does to the steps after it can be read
// off. Running the real tools here would run the gate inside its own test.
const STAND_IN = [
  '#!/usr/bin/env bash',
  'name="$(basename "$0")"',
  'printf \'%s|%s|%s\\n\' "$name" "$*" "${PWD#"$GATE_ROOT"}" >> "$GATE_LOG"',
  '[ "${GATE_FAIL:-}" = "$name ${1:-}" ] && exit 1',
  'exit 0',
].join('\n');

async function runGateWithStandIns(t, { failing = '' } = {}) {
  const root = await scratchDirectory(t);
  for (const directory of ['bin', 'stubs', 'markdown-pass']) {
    await mkdir(`${root}/${directory}`);
  }
  await copyFile(`${repositoryRoot}bin/ci`, `${root}/bin/ci`);
  await chmod(`${root}/bin/ci`, 0o755);
  await writeFile(`${root}/go.mod`, 'module example.test/gate\n');
  for (const tool of ['gitleaks', 'gofmt', 'go', 'npm']) {
    await writeFile(`${root}/stubs/${tool}`, STAND_IN);
    await chmod(`${root}/stubs/${tool}`, 0o755);
  }
  const log = `${root}/calls.log`;
  await writeFile(log, '');

  const result = await runCommand('bash', [`${root}/bin/ci`], {
    cwd: root,
    env: { PATH: `${root}/stubs:${process.env.PATH}`, GATE_ROOT: root, GATE_LOG: log, GATE_FAIL: failing },
  });
  const calls = (await readFile(log, 'utf8')).split('\n').filter(Boolean);
  return { result, calls };
}

const GO_STEPS = [
  'gitleaks|git --no-banner --redact|',
  'gofmt|-l .|',
  'go|vet ./...|',
  'go|test ./...|',
  'go|build ./...|',
];
const NODE_STEPS = ['npm|ci|/markdown-pass', 'npm|test|/markdown-pass'];

test('the gate runs its Go steps unchanged, then npm ci and npm test inside markdown-pass', async (t) => {
  const { result, calls } = await runGateWithStandIns(t);

  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(calls, [...GO_STEPS, ...NODE_STEPS]);
});

test('the gate fails when npm ci fails, and does not go on to npm test', async (t) => {
  const { result, calls } = await runGateWithStandIns(t, { failing: 'npm ci' });

  assert.notEqual(result.status, 0);
  assert.deepEqual(calls, [...GO_STEPS, 'npm|ci|/markdown-pass']);
});

test('the gate fails when npm test fails', async (t) => {
  const { result, calls } = await runGateWithStandIns(t, { failing: 'npm test' });

  assert.notEqual(result.status, 0);
  assert.deepEqual(calls, [...GO_STEPS, ...NODE_STEPS]);
});

test('a failing Go step stops the gate before any Node step', async (t) => {
  const { result, calls } = await runGateWithStandIns(t, { failing: 'go test' });

  assert.notEqual(result.status, 0);
  assert.deepEqual(calls, ['gitleaks|git --no-banner --redact|', 'gofmt|-l .|', 'go|vet ./...|', 'go|test ./...|']);
});

test('the Go steps pass with markdown-pass/node_modules in the tree', async () => {
  assert.ok(existsSync(`${repositoryRoot}markdown-pass/node_modules`), 'run npm ci in markdown-pass first');

  // gofmt -l . walks every directory, node_modules included, and lists any .go file in it
  // that is not formatted; go list ./... is the set of packages go vet and go test cover.
  const formatting = await runCommand('gofmt', ['-l', '.'], { cwd: repositoryRoot });
  assert.equal(formatting.status, 0, formatting.stderr);
  assert.equal(formatting.stdout, '', 'gofmt lists files that are not formatted');

  const packages = await runCommand('go', ['list', './...'], { cwd: repositoryRoot });
  assert.equal(packages.status, 0, packages.stderr);
  assert.ok(!packages.stdout.includes('node_modules'), 'no package from node_modules is vetted or tested');
});

test('the workflow installs Node 24 before it runs bin/ci', async () => {
  const workflow = await readFile(`${repositoryRoot}.github/workflows/ci.yml`, 'utf8');

  const setupNode = workflow.search(/uses:\s*actions\/setup-node@/);
  const version = workflow.search(/node-version:\s*["']?24(?:\.x)?["']?/);
  const gate = workflow.search(/run:\s*bin\/ci\b/);

  assert.notEqual(setupNode, -1, 'the workflow sets up Node');
  assert.notEqual(version, -1, 'and asks for Node 24');
  assert.notEqual(gate, -1, 'and runs bin/ci');
  assert.ok(setupNode < version, 'the version belongs to the setup step');
  assert.ok(version < gate, 'Node is installed before bin/ci runs');
});
