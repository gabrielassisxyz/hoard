import assert from 'node:assert/strict';
import { writeFile } from 'node:fs/promises';
import { test } from 'node:test';
import { outcomeOfMarkdown } from '../lib/derive-page.mjs';
import {
  articleHtml,
  deriveInto,
  filesUnder,
  linesOf,
  listedPage,
  runDerive,
  scratchDirectory,
} from '../test-support/harness.mjs';

const lastLineOf = (text) => text.trimEnd().split('\n').at(-1);
const linesOfText = (text) => text.split('\n').filter((line) => line !== '');

async function fixture(t) {
  const directory = await scratchDirectory(t);
  return { directory, outDir: `${directory}/out` };
}

// ---- lines that are not HTML ---------------------------------------------------------------

test('a line that is not HTML writes no file and is counted as skipped, without its body being read', async (t) => {
  const { directory, outDir } = await fixture(t);
  const html = await listedPage(directory, { body: articleHtml(), address: 'https://site.test/page' });
  const json = await listedPage(directory, { body: '{"not":"a page"}', address: 'https://site.test/data', content_type: 'application/json' });
  const image = await listedPage(directory, {
    body: 'GIF89a',
    address: 'https://site.test/image',
    content_type: 'image/gif',
    // A skipped line is never opened, so a body that is not there is not a failure either.
    body_path: `${directory}/raw/no/such-body.zst`,
  });

  const result = await deriveInto(outDir, linesOf([json, html, image]));

  assert.equal(result.status, 0, result.stderr);
  assert.equal(lastLineOf(result.stdout), '1 derived, 0 empty, 2 skipped, 0 failed');
  assert.equal(result.stderr, '');
  assert.equal((await filesUnder(outDir)).size, 2, 'only the HTML page has files');
});

// ---- pages and lines that fail -------------------------------------------------------------

test('a page that cannot be read, decompressed or extracted is failed, named on standard error, and the run goes on', async (t) => {
  const { directory, outDir } = await fixture(t);
  const missingBody = await listedPage(directory, { body: articleHtml({ title: 'one' }), address: 'https://site.test/missing-body' });
  missingBody.body_path = `${directory}/raw/no/such-body.zst`;

  const notZstd = await listedPage(directory, { body: articleHtml({ title: 'two' }), address: 'https://site.test/not-zstd' });
  await writeFile(notZstd.body_path, 'this is not a zstd file');

  // linkedom builds no document at all from an empty body, and defuddle then throws.
  const emptyBody = await listedPage(directory, { body: '', address: 'https://site.test/empty-body' });

  const fine = await listedPage(directory, { body: articleHtml({ title: 'three' }), address: 'https://site.test/fine' });

  const result = await deriveInto(outDir, linesOf([missingBody, notZstd, emptyBody, fine]));

  assert.equal(result.status, 1, result.stderr);
  assert.equal(lastLineOf(result.stdout), '1 derived, 0 empty, 0 skipped, 3 failed');
  const complaints = linesOfText(result.stderr).filter((line) => line.startsWith('derive: https://'));
  assert.equal(complaints.length, 3, `one line per failed page, got:\n${result.stderr}`);
  assert.match(complaints[0], /^derive: https:\/\/site\.test\/missing-body: cannot read the body: ENOENT/);
  assert.match(complaints[1], /^derive: https:\/\/site\.test\/not-zstd: cannot decompress the body/);
  assert.match(complaints[2], /^derive: https:\/\/site\.test\/empty-body: extraction failed/);
  assert.equal((await filesUnder(outDir)).size, 2, 'the failed pages left no file, and the page after them was still derived');
});

test('a line that is not a JSON object with the eight keys is failed and named by its line number', async (t) => {
  const { directory, outDir } = await fixture(t);
  const first = await listedPage(directory, { body: articleHtml({ title: 'first' }), address: 'https://site.test/first' });
  const last = await listedPage(directory, { body: articleHtml({ title: 'last' }), address: 'https://site.test/last' });
  const withoutKey = { ...first, address: 'https://site.test/no-body-path' };
  delete withoutKey.body_path;
  const input = [
    first, //                                                  line 1
    'not json at all', //                                       line 2
    '[1, 2, 3]', //                                             line 3
    'null', //                                                  line 4
    '"a string"', //                                            line 5
    '{"address": "https://site.test/only-an-address"}', //      line 6
    withoutKey, //                                              line 7
    { ...first, size_bytes: '10', address: 'https://site.test/size-as-text' }, // line 8
    '', //                                                      line 9
    last, //                                                    line 10
  ];

  const result = await deriveInto(outDir, linesOf(input));

  assert.equal(result.status, 1, result.stderr);
  assert.equal(lastLineOf(result.stdout), '2 derived, 0 empty, 0 skipped, 8 failed');
  const complaints = linesOfText(result.stderr);
  assert.equal(complaints.length, 8, `one line per bad line, got:\n${result.stderr}`);
  for (const [index, lineNumber] of [2, 3, 4, 5, 6, 7, 8, 9].entries()) {
    assert.match(complaints[index], new RegExp(`^derive: line ${lineNumber}: `), complaints[index]);
  }
  assert.match(complaints[5], /body_path/, 'a line missing a key says which');
  assert.match(complaints[6], /size_bytes/, 'and so does one with a key of the wrong type');
  assert.equal((await filesUnder(outDir)).size, 4, 'the lines before and after the bad ones were derived');
});

test('a line with a key beyond the eight is still a page', async (t) => {
  const { directory, outDir } = await fixture(t);
  const page = await listedPage(directory, { body: articleHtml(), address: 'https://site.test/extra', added_later: 'ignored' });

  const result = await deriveInto(outDir, linesOf([page]));

  assert.equal(result.status, 0, result.stderr);
  assert.equal(lastLineOf(result.stdout), '1 derived, 0 empty, 0 skipped, 0 failed');
});

test('what a failed page leaves on standard error is one line, even when its address or error spans several', async (t) => {
  const { directory, outDir } = await fixture(t);
  const page = await listedPage(directory, { body: articleHtml(), address: 'https://site.test/two\nlines' });
  page.body_path = `${directory}/raw/no/such\nbody.zst`;

  const result = await deriveInto(outDir, linesOf([page]));

  assert.equal(result.status, 1);
  assert.equal(linesOfText(result.stderr).length, 1, result.stderr);
  assert.match(result.stderr, /two lines: cannot read the body/);
});

// ---- empty pages and the summary -----------------------------------------------------------

// No synthetic page makes the library return white space only, so the rule is asked directly.
test('Markdown of white space alone is empty, and white space around text is not', () => {
  for (const blank of ['', ' ', '\n', '\n\n \t\n']) {
    assert.equal(outcomeOfMarkdown(blank), 'empty', `${JSON.stringify(blank)} is blank`);
  }
  assert.equal(outcomeOfMarkdown('\n text \n'), 'derived');
});

test('a page whose Markdown is blank is counted as empty, and its files are still written', async (t) => {
  const { directory, outDir } = await fixture(t);
  const empty = await listedPage(directory, {
    body: '<html><head><title>Nothing here</title></head><body></body></html>',
    address: 'https://site.test/empty',
  });
  const fine = await listedPage(directory, { body: articleHtml(), address: 'https://site.test/fine' });

  const result = await deriveInto(outDir, linesOf([empty, fine]));

  assert.equal(result.status, 0, result.stderr);
  assert.equal(lastLineOf(result.stdout), '1 derived, 1 empty, 0 skipped, 0 failed');
  const files = await filesUnder(outDir);
  assert.equal(files.size, 4, 'both pages have both files');
  const emptyMarkdown = [...files].find(([key, content]) => key.endsWith('.md') && content.toString().includes('/empty"'));
  assert.ok(emptyMarkdown, 'the empty page has a Markdown file');
  assert.ok(emptyMarkdown[1].toString().endsWith('\n---\n\n'), 'that is the block and nothing after it');
});

test('every line is counted once, so the four numbers add up to the lines read', async (t) => {
  const { directory, outDir } = await fixture(t);
  const html = await listedPage(directory, { body: articleHtml(), address: 'https://site.test/derived' });
  const empty = await listedPage(directory, { body: '<html><body></body></html>', address: 'https://site.test/empty' });
  const skipped = await listedPage(directory, { body: 'x', address: 'https://site.test/skipped', content_type: 'text/plain' });
  const unreadable = await listedPage(directory, { body: articleHtml({ title: 'u' }), address: 'https://site.test/unreadable' });
  unreadable.body_path = `${directory}/raw/no/such-body.zst`;

  const result = await deriveInto(outDir, linesOf([html, empty, skipped, unreadable, 'not json']));

  assert.equal(lastLineOf(result.stdout), '1 derived, 1 empty, 1 skipped, 2 failed');
});

test('a run with nothing to do says so and succeeds', async (t) => {
  const { outDir } = await fixture(t);

  const result = await deriveInto(outDir, '');

  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stdout, '0 derived, 0 empty, 0 skipped, 0 failed\n');
  assert.equal(result.stderr, '');
});

// ---- exit status ---------------------------------------------------------------------------

test('the run exits 0 unless a page or a line failed: skipped and empty pages are not failures', async (t) => {
  const { directory, outDir } = await fixture(t);
  const empty = await listedPage(directory, { body: '<html><body></body></html>', address: 'https://site.test/empty' });
  const skipped = await listedPage(directory, { body: 'x', address: 'https://site.test/skipped', content_type: 'text/plain' });

  const result = await deriveInto(outDir, linesOf([empty, skipped]));

  assert.equal(result.status, 0);
  assert.equal(lastLineOf(result.stdout), '0 derived, 1 empty, 1 skipped, 0 failed');
});

test('a command line that cannot be read exits 2 with a usage message, before any input is touched', async (t) => {
  const { directory, outDir } = await fixture(t);
  const page = await listedPage(directory, { body: articleHtml(), address: 'https://site.test/never-derived' });
  const input = linesOf([page]);
  const cases = {
    'no --out': [],
    'an empty --out': ['--out='],
    '--out without a value': ['--out'],
    'an unknown flag': ['--out', outDir, '--no-such-flag'],
    'a stray argument': ['--out', outDir, 'extra'],
  };

  for (const [name, args] of Object.entries(cases)) {
    const result = await runDerive({ input, args });
    assert.equal(result.status, 2, `${name}: exit ${result.status}, stderr ${result.stderr}`);
    assert.match(result.stderr, /usage: node derive\.mjs --out <dir>/, name);
    assert.equal(result.stdout, '', `${name}: nothing on standard output`);
  }
  assert.equal((await filesUnder(outDir)).size, 0, 'no run derived anything');
});

test('--help prints the usage and exits 0', async () => {
  const result = await runDerive({ args: ['--help'] });

  assert.equal(result.status, 0);
  assert.match(result.stdout, /usage: node derive\.mjs --out <dir>/);
});
