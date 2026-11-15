import assert from 'node:assert/strict';
import { readFile, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { test } from 'node:test';
import { outputNameOf } from '../lib/output.mjs';
import {
  articleHtml,
  defaultParagraphs,
  filesUnder,
  repositoryRoot,
  runCommand,
  runDerive,
  scratchDirectory,
  startSite,
} from '../test-support/harness.mjs';

// The real chain, with nothing stubbed: a local web server, `hoard fetch` into a real store,
// `hoard list --json` over it, and `derive.mjs` reading the list. It runs the command through
// `go run`, which compiles it with cgo off like the documented build, and it fails, not skips,
// when that cannot run: a test that passes by not running proves nothing about the pipeline.
function hoard(...args) {
  return runCommand('go', ['run', './cmd/hoard', ...args], { cwd: repositoryRoot, env: { CGO_ENABLED: '0' } });
}

const HTML_HEADERS = { 'content-type': 'text/html; charset=utf-8' };

function pageWith(title, marker) {
  return articleHtml({
    title,
    paragraphs: [...defaultParagraphs.slice(0, 5), `${marker} is the sentence that only this page holds.`],
  });
}

test('fetch, list and derive turn a small site into Markdown, one file per HTML page', async (t) => {
  const site = await startSite(t, {
    '/guides/first': { headers: HTML_HEADERS, body: pageWith('First guide', 'The first marker') },
    '/guides/second': { headers: HTML_HEADERS, body: pageWith('Second guide', 'The second marker') },
    '/guides/old-name': { status: 301, headers: { location: '/guides/new-name' } },
    '/guides/new-name': { headers: HTML_HEADERS, body: pageWith('Renamed guide', 'The renamed marker') },
    '/data.json': { headers: { 'content-type': 'application/json' }, body: '{"this":"is not a page"}' },
  });
  const directory = await scratchDirectory(t);
  const storeDirectory = `${directory}/store`;
  const outDirectory = `${directory}/out`;
  const addresses = [
    `${site.origin}/guides/first`,
    `${site.origin}/guides/second`,
    `${site.origin}/guides/old-name`,
    `${site.origin}/data.json`,
  ];
  await writeFile(`${directory}/addresses.txt`, `${addresses.join('\n')}\n`);

  const fetched = await hoard('fetch', '--store', storeDirectory, '--list', `${directory}/addresses.txt`, '--rate', '1000');
  assert.equal(fetched.status, 0, fetched.stderr);
  // The summary is the first line. The second says how long the site took to answer, and its
  // figure depends on the machine, so only its shape is asserted.
  const fetchedLines = fetched.stdout.split('\n');
  assert.equal(fetchedLines.length, 3, 'two lines, each ended by a newline');
  assert.equal(fetchedLines[0], '4 listed, 4 held, 0 failed, 0 pending');
  assert.match(fetchedLines[1], /^median response \d+ ms over 4 pages$/);
  assert.equal(fetchedLines[2], '');

  const listing = await hoard('list', '--store', storeDirectory, '--json');
  assert.equal(listing.status, 0, listing.stderr);
  const listed = listing.stdout.trimEnd().split('\n').map((line) => JSON.parse(line));
  assert.equal(listed.length, 4);
  const redirected = listed.find((page) => page.address.endsWith('/guides/old-name'));
  assert.equal(redirected.final_address, `${site.origin}/guides/new-name`, 'the store kept where the redirect led');

  const requestsBeforeDeriving = site.requests.length;
  const derived = await runDerive({ input: listing.stdout, args: ['--out', outDirectory] });

  assert.equal(derived.status, 0, derived.stderr);
  assert.equal(derived.stdout.trimEnd().split('\n').at(-1), '3 derived, 0 empty, 1 skipped, 0 failed');
  assert.equal(site.requests.length, requestsBeforeDeriving, 'deriving asked the site for nothing');

  const files = await filesUnder(outDirectory);
  const markdownOf = (address) => {
    const name = outputNameOf(address);
    return files.get(`markdown/${name.slice(0, 2)}/${name}.md`)?.toString('utf8');
  };

  assert.match(markdownOf(addresses[0]), /The first marker is the sentence that only this page holds\./);
  assert.match(markdownOf(addresses[1]), /The second marker is the sentence that only this page holds\./);
  assert.match(markdownOf(addresses[2]), /The renamed marker is the sentence that only this page holds\./);

  const block = markdownOf(addresses[2]).split('\n').slice(0, 4);
  assert.deepEqual(block, [
    '---',
    `address: ${JSON.stringify(addresses[2])}`,
    `final_address: ${JSON.stringify(`${site.origin}/guides/new-name`)}`,
    'title: "Renamed guide"',
  ], 'the redirected page carries the address it was asked for and the one it came from');

  assert.equal(markdownOf(addresses[3]), undefined, 'the JSON response was skipped, not derived');
  assert.equal(files.size, 3, 'three pages, one file each, and nothing for the one that was skipped');
});

test('a page, its image, and the Markdown that points at the kept copy: the whole chain', async (t) => {
  // Bytes that are not text, so a copy that went through a string would not equal them.
  const picture = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0xff, 0xfe, 0x00, 0x80, 0x7f]);
  const site = await startSite(t, {
    '/post': {
      headers: HTML_HEADERS,
      body: articleHtml({ title: 'A post', tail: '<p><img src="/pictures/photo.png" alt="A photo" title="Seen once"></p>' }),
    },
    '/pictures/photo.png': { headers: { 'content-type': 'image/png' }, body: picture },
  });
  const directory = await scratchDirectory(t);
  const storeDirectory = `${directory}/store`;
  const outDirectory = `${directory}/out`;
  await writeFile(`${directory}/pages.txt`, `${site.origin}/post\n`);

  const fetched = await hoard('fetch', '--store', storeDirectory, '--list', `${directory}/pages.txt`, '--rate', '1000');
  assert.equal(fetched.status, 0, fetched.stderr);
  const pages = (await hoard('list', '--store', storeDirectory, '--json')).stdout;

  const listed = await runDerive({ input: pages, args: ['--list-images'] });
  assert.equal(listed.status, 0, listed.stderr);
  assert.equal(listed.stdout, `${site.origin}/pictures/photo.png\n`);

  await writeFile(`${directory}/images.txt`, listed.stdout);
  const kept = await hoard('assets', '--store', storeDirectory, '--list', `${directory}/images.txt`, '--rate', '1000');
  assert.equal(kept.status, 0, kept.stderr);
  const held = (await hoard('list', '--store', storeDirectory, '--assets', '--json')).stdout;
  await writeFile(`${directory}/assets.jsonl`, held);

  const requestsBeforeDeriving = site.requests.length;
  const derived = await runDerive({ input: pages, args: ['--out', outDirectory, '--assets', `${directory}/assets.jsonl`] });
  assert.equal(derived.status, 0, derived.stderr);
  assert.equal(site.requests.length, requestsBeforeDeriving, 'deriving with the images asked the site for nothing');

  const name = outputNameOf(`${site.origin}/post`);
  const markdownPath = `${outDirectory}/markdown/${name.slice(0, 2)}/${name}.md`;
  const markdown = (await filesUnder(outDirectory)).get(`markdown/${name.slice(0, 2)}/${name}.md`).toString('utf8');
  const written = /!\[A photo\]\(([^ )]+) "Seen once"\)/.exec(markdown)?.[1];
  assert.ok(written, markdown);
  assert.ok(!written.includes('://') && !path.isAbsolute(written), `a relative path, not an address: ${written}`);
  assert.deepEqual(await readFile(path.resolve(path.dirname(markdownPath), decodeURIComponent(written))), picture);
});
