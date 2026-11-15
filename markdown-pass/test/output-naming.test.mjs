import assert from 'node:assert/strict';
import { test } from 'node:test';
import { outputNameOf, outputPathsOf } from '../lib/output.mjs';
import {
  articleHtml,
  deriveInto,
  filesUnder,
  linesOf,
  listedPage,
  scratchDirectory,
} from '../test-support/harness.mjs';

// The expected names are SHA-256 digests worked out outside this code, of the UTF-8 bytes
// of the address, so that they pin the algorithm and not just agree with themselves:
//   printf %s 'https://site.test/a'    | sha256sum
//   printf %s 'https://site.test/café' | sha256sum
const NAME_OF_PLAIN_ADDRESS = 'aaa15320cd4cd11aa5846e7b13632f5086fbf90ab5279c00b0cdf71f669e5c18';
const NAME_OF_ACCENTED_ADDRESS = 'a74f9304ea650bff296a89f583ea584cfc10fdedf86e196999d68e8bf0a2fd7c';

test('a name is the lowercase hex SHA-256 of the address as UTF-8', () => {
  assert.equal(outputNameOf('https://site.test/a'), NAME_OF_PLAIN_ADDRESS);
  // The é is two bytes in UTF-8; hashing it as Latin-1 would give a different name.
  assert.equal(outputNameOf('https://site.test/café'), NAME_OF_ACCENTED_ADDRESS);
});

test('a page lands under markdown/ and clean/, in a directory of the name\'s first two digits', () => {
  const paths = outputPathsOf('/out', 'https://site.test/a');
  assert.equal(paths.markdown, `/out/markdown/aa/${NAME_OF_PLAIN_ADDRESS}.md`);
  assert.equal(paths.cleanHtml, `/out/clean/aa/${NAME_OF_PLAIN_ADDRESS}.html`);
});

// Derives one page whose address and final address differ, and returns the files it left.
async function filesDerivedFromOnePage(t, extraArgs) {
  const directory = await scratchDirectory(t);
  const outDir = `${directory}/out`;
  const page = await listedPage(directory, {
    body: articleHtml(),
    address: 'https://site.test/café',
    final_address: 'https://site.test/café/final',
  });
  const result = await deriveInto(outDir, linesOf([page]), extraArgs);
  assert.equal(result.status, 0, result.stderr);
  return [...(await filesUnder(outDir)).keys()];
}

test('the pass writes one Markdown file named after the address, and no clean HTML unless asked', async (t) => {
  assert.deepEqual(
    await filesDerivedFromOnePage(t, []),
    [`markdown/a7/${NAME_OF_ACCENTED_ADDRESS}.md`],
    'exactly this one file, named from address and not from final_address or the body',
  );
});

test('with --clean-html the pass also writes the clean HTML, under the same name', async (t) => {
  assert.deepEqual(
    await filesDerivedFromOnePage(t, ['--clean-html']),
    [
      `clean/a7/${NAME_OF_ACCENTED_ADDRESS}.html`,
      `markdown/a7/${NAME_OF_ACCENTED_ADDRESS}.md`,
    ],
    'exactly these two files',
  );
});

test('two addresses that share one body are two derived pages, each under its own name', async (t) => {
  const directory = await scratchDirectory(t);
  const outDir = `${directory}/out`;
  const body = articleHtml();
  const first = await listedPage(directory, { body, address: 'https://site.test/one' });
  const second = await listedPage(directory, { body, address: 'https://site.test/two' });
  assert.equal(first.sha256, second.sha256);

  const result = await deriveInto(outDir, linesOf([first, second]));

  assert.equal(result.status, 0, result.stderr);
  assert.equal((await filesUnder(outDir)).size, 2);
});

test('only HTML content types are derived: by media type, ignoring parameters and case, or none at all', async (t) => {
  const directory = await scratchDirectory(t);
  const outDir = `${directory}/out`;
  const body = articleHtml();
  const derivedTypes = ['text/html', 'TEXT/HTML; charset=utf-8', 'application/xhtml+xml', ''];
  const skippedTypes = ['text/plain', 'application/json', 'image/png', 'text/htmlx'];
  const pages = [];
  for (const [index, contentType] of [...derivedTypes, ...skippedTypes].entries()) {
    pages.push(await listedPage(directory, {
      body,
      address: `https://site.test/type-${index}`,
      content_type: contentType,
    }));
  }

  const result = await deriveInto(outDir, linesOf(pages));

  assert.equal(result.status, 0, result.stderr);
  const lastLine = result.stdout.trimEnd().split('\n').at(-1);
  assert.equal(lastLine, '4 derived, 0 empty, 4 skipped, 0 failed');
  const files = await filesUnder(outDir);
  assert.equal(files.size, derivedTypes.length);
  for (const [index, contentType] of derivedTypes.entries()) {
    const name = outputNameOf(`https://site.test/type-${index}`);
    assert.ok(files.has(`markdown/${name.slice(0, 2)}/${name}.md`), `${JSON.stringify(contentType)} was derived`);
  }
  for (const [index, contentType] of skippedTypes.entries()) {
    const name = outputNameOf(`https://site.test/type-${derivedTypes.length + index}`);
    assert.ok(!files.has(`markdown/${name.slice(0, 2)}/${name}.md`), `${JSON.stringify(contentType)} was skipped`);
  }
});
