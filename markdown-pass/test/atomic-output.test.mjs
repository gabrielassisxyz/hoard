import assert from 'node:assert/strict';
import { mkdir, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { test } from 'node:test';
import { outputPathsOf, writeFilesAtomically } from '../lib/output.mjs';
import {
  articleHtml,
  deriveInto,
  filesUnder,
  linesOf,
  listedPage,
  scratchDirectory,
} from '../test-support/harness.mjs';

// A file system that only writes down what it was asked to do, and fails on request, so
// that the order of the steps and what is cleaned up after a failure can be read off.
function recordingFileSystem({ failsOn = () => false } = {}) {
  const operations = [];
  const check = (operation, ...argumentsOfCall) => {
    if (failsOn(operation, ...argumentsOfCall)) {
      throw new Error(`injected failure in ${operation}`);
    }
  };
  return {
    operations,
    fileSystem: {
      async mkdir(directory) {
        operations.push(['mkdir', directory]);
      },
      async writeFile(file, content) {
        operations.push(['writeFile', file, content]);
        check('writeFile', file);
      },
      async rename(from, to) {
        operations.push(['rename', from, to]);
        check('rename', from, to);
      },
      async rm(file) {
        operations.push(['rm', file]);
      },
    },
  };
}

const FINAL_MARKDOWN = '/out/markdown/aa/page.md';
const FINAL_HTML = '/out/clean/aa/page.html';
const FILES = [
  { path: FINAL_MARKDOWN, content: 'the markdown' },
  { path: FINAL_HTML, content: 'the html' },
];

const kindsOf = (operations, kind) => operations.filter(([operation]) => operation === kind);

test('every file is written under a temporary name beside its final one, and renamed into place after all are written', async () => {
  const { operations, fileSystem } = recordingFileSystem();

  await writeFilesAtomically(FILES, fileSystem);

  const writes = kindsOf(operations, 'writeFile');
  const renames = kindsOf(operations, 'rename');
  assert.equal(writes.length, 2);
  assert.equal(renames.length, 2);
  for (const [, temporary, content] of writes) {
    assert.ok(![FINAL_MARKDOWN, FINAL_HTML].includes(temporary), `${temporary} is not a final name`);
    assert.match(path.basename(temporary), /^\.incoming-/);
    assert.ok(FILES.some((file) => path.dirname(file.path) === path.dirname(temporary) && file.content === content),
      'the temporary file is in the directory its file is going to, so the rename cannot cross file systems');
  }
  for (const file of FILES) {
    const written = writes.find(([, , content]) => content === file.content);
    assert.deepEqual(
      renames.find(([, from]) => from === written[1]),
      ['rename', written[1], file.path],
      `${file.path} comes from its own temporary file`,
    );
  }
  const lastWrite = operations.findLastIndex(([operation]) => operation === 'writeFile');
  const firstRename = operations.findIndex(([operation]) => operation === 'rename');
  assert.ok(lastWrite < firstRename, 'no file is put in place until every file is written');
  assert.equal(kindsOf(operations, 'rm').length, 0, 'nothing is left to clean up after success');
});

test('a write that fails leaves no final file, no rename, and no temporary file', async () => {
  let writes = 0;
  const { operations, fileSystem } = recordingFileSystem({
    failsOn: (operation) => operation === 'writeFile' && ++writes === 2,
  });

  await assert.rejects(writeFilesAtomically(FILES, fileSystem), /injected failure in writeFile/);

  assert.equal(kindsOf(operations, 'rename').length, 0, 'nothing was put in place');
  const written = kindsOf(operations, 'writeFile').map(([, file]) => file);
  assert.equal(written.length, 2);
  assert.deepEqual(kindsOf(operations, 'rm').map(([, file]) => file).sort(), written.sort(),
    'both temporary files, the finished one and the half-written one, were removed');
});

test('a rename that fails removes the temporary files, and no final name was ever written to directly', async () => {
  const { operations, fileSystem } = recordingFileSystem({
    failsOn: (operation, from, to) => operation === 'rename' && to === FINAL_MARKDOWN,
  });

  await assert.rejects(writeFilesAtomically(FILES, fileSystem), /injected failure in rename/);

  for (const [, file] of kindsOf(operations, 'writeFile')) {
    assert.ok(![FINAL_MARKDOWN, FINAL_HTML].includes(file), `${file} was written directly`);
  }
  assert.equal(kindsOf(operations, 'rm').length, 2);
});

test('through the whole pass: a page whose output cannot be put in place leaves no temporary file behind', async (t) => {
  const directory = await scratchDirectory(t);
  const outDir = `${directory}/out`;
  const page = await listedPage(directory, { body: articleHtml(), address: 'https://site.test/blocked' });
  // A directory sits where the Markdown file belongs, so the rename onto it fails.
  const { markdown, cleanHtml } = outputPathsOf(outDir, page.address);
  await mkdir(markdown, { recursive: true });
  await writeFile(path.join(markdown, 'obstacle.txt'), 'in the way');

  const result = await deriveInto(outDir, linesOf([page]));

  assert.equal(result.status, 1, result.stderr);
  assert.match(result.stderr, /blocked: cannot write the output/);
  const left = [...(await filesUnder(outDir)).keys()];
  assert.deepEqual(left, [path.relative(outDir, path.join(markdown, 'obstacle.txt'))], `only the obstacle remains, not ${left}`);
  assert.equal((await filesUnder(path.dirname(cleanHtml))).size, 0, 'and the HTML was not put in place either');
});

test('two runs over the same input leave byte-identical files, in the same place or in another', async (t) => {
  const directory = await scratchDirectory(t);
  const withMetadata = articleHtml({
    title: 'Metadata',
    head: '<meta name="author" content="A. Writer"><meta property="article:published_time" content="2026-11-01T10:00:00Z">',
    tail: '<p>See <a href="/elsewhere">elsewhere</a>.</p>',
  });
  const pages = [
    await listedPage(directory, { body: articleHtml(), address: 'https://site.test/plain' }),
    await listedPage(directory, { body: withMetadata, address: 'https://site.test/meta', final_address: 'https://site.test/meta-final' }),
    await listedPage(directory, { body: '<html><body></body></html>', address: 'https://site.test/empty' }),
  ];
  const input = linesOf(pages);

  const first = await deriveInto(`${directory}/first`, input);
  const second = await deriveInto(`${directory}/second`, input);
  const again = await deriveInto(`${directory}/first`, input);

  for (const result of [first, second, again]) {
    assert.equal(result.status, 0, result.stderr);
  }
  const firstFiles = await filesUnder(`${directory}/first`);
  const secondFiles = await filesUnder(`${directory}/second`);
  assert.equal(firstFiles.size, 6);
  assert.deepEqual([...firstFiles.keys()], [...secondFiles.keys()]);
  for (const [name, content] of firstFiles) {
    assert.ok(content.equals(secondFiles.get(name)), `${name} differs between two runs`);
  }
  assert.equal((await filesUnder(`${directory}/first`)).size, 6, 'a third run over the same directory added nothing');
});
