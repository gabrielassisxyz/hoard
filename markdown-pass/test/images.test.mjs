import assert from 'node:assert/strict';
import { mkdir, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { test } from 'node:test';
import {
  imageReferencesOf,
  isListableAddress,
  listableAddressesOf,
  parseAssetLines,
  pointImagesAt,
  relativeImagePath,
} from '../lib/images.mjs';
import { outputNameOf } from '../lib/output.mjs';
import {
  articleHtml,
  deriveInto,
  filesUnder,
  linesOf,
  listedPage,
  runCommand,
  runDerive,
  scratchDirectory,
  startSite,
} from '../test-support/harness.mjs';

const everyAddress = (markdown) => imageReferencesOf(markdown).map((reference) => reference.address);

// What is found in the Markdown

test('a Markdown image is found with its title, an angle-bracket destination and parentheses in the address', () => {
  const markdown = [
    '![plain](https://a.test/plain.png)',
    '![titled](https://a.test/titled.png "A title (with) parts")',
    "![single](https://a.test/single.png 'one')",
    '![paren](https://a.test/paren.png (the title))',
    '![angle](<https://a.test/with space.png> "t")',
    '![balanced](https://a.test/p(a)r.png)',
    '![escaped](https://a.test/p\\(a\\)r.png)',
    '![nested \\[brackets\\] [in] alt](https://a.test/nested.png)',
    '[![linked](https://a.test/linked.png)](https://a.test/target)',
  ].join('\n\n');

  assert.deepEqual(everyAddress(markdown), [
    'https://a.test/plain.png',
    'https://a.test/titled.png',
    'https://a.test/single.png',
    'https://a.test/paren.png',
    'https://a.test/with space.png',
    'https://a.test/p(a)r.png',
    'https://a.test/p(a)r.png',
    'https://a.test/nested.png',
    'https://a.test/linked.png',
  ]);
});

test('text that only looks like an image is not one', () => {
  const markdown = [
    '![no destination] and ![](  ',
    '![escaped bang]\\(https://a.test/x.png)',
    '\\![not](https://a.test/escaped.png)',
    '[a link](https://a.test/link.png)',
    '![bad title](https://a.test/bad.png "never closed)',
    'An inline `![code](https://a.test/inline.png)` span.',
    '````',
    '![fenced](https://a.test/fenced.png)',
    '```',
    '![still fenced](https://a.test/still.png)',
    '````',
    '~~~',
    '![tilde](https://a.test/tilde.png)',
    '~~~',
    '![real](https://a.test/real.png)',
  ].join('\n');

  assert.deepEqual(everyAddress(markdown), ['https://a.test/real.png']);
});

test('an <img src> is found however the value is quoted, and data-src is not src', () => {
  const html = [
    '<td><img src="https://a.test/double.png"></td>',
    "<td><img alt='x > y' src='https://a.test/single.png'/></td>",
    '<td><img src=https://a.test/bare.png></td>',
    '<td><img data-src="https://a.test/lazy.png"></td>',
    '<td><img alt="no source"></td>',
    '<td><img src="https://a.test/a.png?x=1&amp;y=2"></td>',
    '<td><imgx src="https://a.test/other.png"></td>',
  ].join('\n');

  assert.deepEqual(everyAddress(html), [
    'https://a.test/double.png',
    'https://a.test/single.png',
    'https://a.test/bare.png',
    'https://a.test/a.png?x=1&y=2',
  ]);
});

test('the hosts of embedded videos are left out by name or subdomain, other hosts are not', () => {
  for (const address of [
    'https://www.youtube.com/watch?v=abc',
    'https://m.youtube.com/watch?v=abc',
    'https://YOUTUBE.com/watch?v=abc',
    'https://youtu.be/abc',
    'https://vimeo.com/123',
    'https://player.vimeo.com/video/123',
    'data:image/gif;base64,R0lGOD',
    'ftp://a.test/x.png',
    '/relative.png',
  ]) {
    assert.equal(isListableAddress(address), false, address);
  }
  for (const address of [
    'https://a.test/x.png',
    'http://a.test/x.png',
    'https://notyoutube.com/x.png',
    'https://youtube.com.a.test/x.png',
    'https://a.test/youtube.com/x.png',
  ]) {
    assert.equal(isListableAddress(address), true, address);
  }
});

test('an address is listed once, in the order first met', () => {
  const markdown = '![b](https://a.test/b.png)\n\n![a](https://a.test/a.png)\n\n![b again](https://a.test/b.png "t")';

  assert.deepEqual(listableAddressesOf(markdown), ['https://a.test/b.png', 'https://a.test/a.png']);
});

// Pointing an image at a file

test('only the address inside an image changes, alt text and title stay as they were', () => {
  const markdown = [
    '![One \\[x\\]](https://a.test/one.png "T one")',
    '![](<https://a.test/two.png>)',
    '<td><img alt="kept" src="https://a.test/three.png?a=1&amp;b=2" width="5"></td>',
    '![other](https://a.test/other.png)',
  ].join('\n');
  const paths = new Map([
    ['https://a.test/one.png', '../x/1.png'],
    ['https://a.test/two.png', '../x/2.png'],
    ['https://a.test/three.png?a=1&b=2', '../x/3.png'],
  ]);

  assert.equal(
    pointImagesAt(markdown, (address) => paths.get(address)),
    [
      '![One \\[x\\]](../x/1.png "T one")',
      '![](<../x/2.png>)',
      '<td><img alt="kept" src="../x/3.png" width="5"></td>',
      '![other](https://a.test/other.png)',
    ].join('\n'),
  );
});

test('a path is relative to the Markdown file and cannot end the image early', () => {
  assert.equal(
    relativeImagePath('/out/markdown/ab/name.md', '/store/assets/cd/hash.png'),
    '../../../store/assets/cd/hash.png',
  );
  assert.equal(
    relativeImagePath('/out/markdown/ab/name.md', '/my store (1)/it\'s & <here>/x y.png'),
    '../../../my%20store%20%281%29/it%27s%20%26%20%3Chere%3E/x%20y.png',
  );
});

const GOOD_ASSET = {
  address: 'https://a.test/a.png',
  final_address: 'https://a.test/a.png',
  content_type: 'image/png',
  sha256: 'ab'.repeat(32),
  size_bytes: 3,
  file_path: '/store/assets/ab/x.png',
};

test('an assets list is read whole, and every bad line is named', () => {
  const good = parseAssetLines(`${JSON.stringify(GOOD_ASSET)}\n`);
  assert.deepEqual([...good.files], [['https://a.test/a.png', '/store/assets/ab/x.png']]);
  assert.deepEqual(good.problems, []);

  const { file_path: _omitted, ...withoutPath } = GOOD_ASSET;
  const bad = parseAssetLines(
    [JSON.stringify(GOOD_ASSET), 'not json', '[1]', JSON.stringify(withoutPath), JSON.stringify({ ...GOOD_ASSET, size_bytes: '3' })].join('\n'),
  );
  assert.deepEqual(
    bad.problems.map((problem) => problem.replace(/:.*/, '')),
    ['line 2', 'line 3', 'line 4', 'line 5'],
  );
  assert.match(bad.problems[2], /file_path/);
  assert.match(bad.problems[3], /size_bytes/);
});

// The pass as a process

const SHARED = 'https://img.test/shared.png';
const KEPT = 'https://img.test/kept.png';
const MISSING = 'https://img.test/missing.png';
const IN_TABLE = 'https://img.test/in-table.png';

function contentWith(...tail) {
  return articleHtml({ tail: tail.join('\n') });
}

const TABLE = (address) =>
  `<table><thead><tr><th colspan="2">Head</th></tr></thead><tbody><tr><td><img src="${address}"></td><td>cell</td></tr></tbody></table>`;

async function pagesIn(directory, bodies) {
  const pages = [];
  for (const [index, body] of bodies.entries()) {
    pages.push(await listedPage(directory, { body, address: `https://site.test/page-${index}` }));
  }
  return pages;
}

async function markdownOf(outDirectory, page) {
  const name = outputNameOf(page.address);
  const file = `markdown/${name.slice(0, 2)}/${name}.md`;
  return { file, text: (await filesUnder(outDirectory)).get(file)?.toString('utf8') };
}

function assetLine(address, filePath) {
  return { ...GOOD_ASSET, address, final_address: address, file_path: filePath };
}

async function assetsFile(directory, items) {
  const file = path.join(directory, 'assets.jsonl');
  await writeFile(file, linesOf(items));
  return file;
}

test('--list-images prints the three addresses of two Markdown images and one <img> in a kept table, and writes no file', async (t) => {
  const directory = await scratchDirectory(t);
  const workDirectory = path.join(directory, 'work');
  await mkdir(workDirectory);
  const [page] = await pagesIn(directory, [
    contentWith(`<p><img src="${SHARED}" alt="first"></p>`, `<p><img src="${KEPT}" alt="second" title="Second"></p>`, TABLE(IN_TABLE)),
  ]);
  const filesBefore = [...(await filesUnder(directory)).keys()];

  const result = await runCommand(process.execPath, [new URL('../derive.mjs', import.meta.url).pathname, '--list-images'], {
    cwd: workDirectory,
    input: linesOf([page]),
  });

  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stdout, `${SHARED}\n${KEPT}\n${IN_TABLE}\n`);
  assert.deepEqual([...(await filesUnder(directory)).keys()], filesBefore, 'nothing was written anywhere');
});

test('--list-images prints an address two pages share once and leaves out data:, youtube.com, youtu.be and vimeo.com', async (t) => {
  const directory = await scratchDirectory(t);
  const pages = await pagesIn(directory, [
    contentWith(`<p><img src="${SHARED}" alt="a"></p>`, '<p><img src="data:image/gif;base64,R0lGODlhAQABAAAAACw=" alt="d"></p>'),
    contentWith(
      `<p><img src="${SHARED}" alt="a"></p>`,
      `<p><img src="${KEPT}" alt="b"></p>`,
      '<p><iframe src="https://www.youtube.com/embed/abc"></iframe></p>',
      TABLE('https://youtu.be/abc'),
      TABLE('https://vimeo.com/123'),
    ),
  ]);

  const result = await runDerive({ input: linesOf(pages), args: ['--list-images'] });

  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stdout, `${SHARED}\n${KEPT}\n`);
});

test('--list-images refuses the options that shape a file it does not write', async () => {
  for (const extra of [['--out', '/nowhere'], ['--clean-html'], ['--assets', '/nowhere']]) {
    const result = await runDerive({ args: ['--list-images', ...extra] });
    assert.equal(result.status, 2, extra.join(' '));
    assert.match(result.stderr, /--list-images writes no file/);
  }
});

async function deriveTwiceOver(t, bodies, items) {
  const directory = await scratchDirectory(t);
  const pages = await pagesIn(directory, bodies);
  const assets = await assetsFile(directory, items(directory));
  return { directory, pages, assets, input: linesOf(pages) };
}

test('--assets points an image the list holds at its file, relative to the Markdown file', async (t) => {
  const { directory, pages, assets, input } = await deriveTwiceOver(t, [contentWith(`<p><img src="${KEPT}" alt="a kept one"></p>`)], (dir) => [
    assetLine(KEPT, `${dir}/store/assets/ab/${'ab'.repeat(32)}.png`),
  ]);
  const outDirectory = `${directory}/out`;

  const result = await deriveInto(outDirectory, input, ['--assets', assets]);

  assert.equal(result.status, 0, result.stderr);
  const { file, text } = await markdownOf(outDirectory, pages[0]);
  const written = /!\[a kept one\]\(([^)]*)\)/.exec(text)?.[1];
  assert.ok(written, text);
  assert.ok(!path.isAbsolute(written) && !written.includes('://'), `relative, not an address: ${written}`);
  assert.equal(path.resolve(path.dirname(path.join(outDirectory, file)), decodeURIComponent(written)), `${directory}/store/assets/ab/${'ab'.repeat(32)}.png`);
});

test('--assets leaves an image the list does not hold with its original address', async (t) => {
  const { directory, pages, assets, input } = await deriveTwiceOver(
    t,
    [contentWith(`<p><img src="${KEPT}" alt="kept"></p>`, `<p><img src="${MISSING}" alt="missing one"></p>`)],
    (dir) => [assetLine(KEPT, `${dir}/store/assets/ab/k.png`)],
  );

  await deriveInto(`${directory}/out`, input, ['--assets', assets]);

  const { text } = await markdownOf(`${directory}/out`, pages[0]);
  assert.ok(text.includes(`![missing one](${MISSING})`), text);
  assert.ok(!text.includes(KEPT), 'the held one no longer points at the network');
});

test('--assets applies the same rule to an <img src> inside kept HTML', async (t) => {
  const { directory, pages, assets, input } = await deriveTwiceOver(t, [contentWith(TABLE(IN_TABLE), TABLE(MISSING))], (dir) => [
    assetLine(IN_TABLE, `${dir}/store/assets/cd/t.png`),
  ]);
  const outDirectory = `${directory}/out`;

  await deriveInto(outDirectory, input, ['--assets', assets]);

  const { file, text } = await markdownOf(outDirectory, pages[0]);
  const written = /<img src="([^"]*)">/.exec(text)?.[1];
  assert.equal(path.resolve(path.dirname(path.join(outDirectory, file)), decodeURIComponent(written)), `${directory}/store/assets/cd/t.png`);
  assert.ok(text.includes(`<img src="${MISSING}">`), 'the one the list lacks keeps its address');
});

test('--assets changes nothing but the address: alt, title and the opening block are as without it', async (t) => {
  const { directory, pages, assets, input } = await deriveTwiceOver(
    t,
    [contentWith(`<p><img src="${KEPT}" alt="Alt [x] text" title="A title"></p>`)],
    (dir) => [assetLine(KEPT, `${dir}/store/assets/ab/k.png`)],
  );

  await deriveInto(`${directory}/plain`, input);
  await deriveInto(`${directory}/local`, input, ['--assets', assets]);

  const plain = (await markdownOf(`${directory}/plain`, pages[0])).text;
  const local = (await markdownOf(`${directory}/local`, pages[0])).text;
  const blockOf = (text) => text.slice(0, text.indexOf('\n---\n', 4) + 5);
  assert.equal(blockOf(local), blockOf(plain), 'the opening block is byte-identical');
  assert.ok(blockOf(plain).startsWith('---\naddress: '));
  assert.match(plain, /!\[Alt \\\[x\\\] text\]\(https:\/\/img\.test\/kept\.png "A title"\)/);
  assert.equal(local, plain.replace(KEPT, /\]\(([^ ]*) "A title"\)/.exec(local)[1]), 'one address replaced, every other byte equal');
  assert.match(local, /!\[Alt \\\[x\\\] text\]\(\S+ "A title"\)/);
});

test('two runs with the same --assets file write byte-identical Markdown', async (t) => {
  const { directory, input, assets } = await deriveTwiceOver(
    t,
    [contentWith(`<p><img src="${KEPT}" alt="a"></p>`, TABLE(IN_TABLE)), contentWith(`<p><img src="${KEPT}" alt="b"></p>`)],
    (dir) => [assetLine(KEPT, `${dir}/store/assets/ab/k.png`), assetLine(IN_TABLE, `${dir}/store/assets/cd/t.png`)],
  );

  await deriveInto(`${directory}/first`, input, ['--assets', assets]);
  await deriveInto(`${directory}/second`, input, ['--assets', assets]);

  const first = await filesUnder(`${directory}/first`);
  assert.equal(first.size, 2);
  assert.deepEqual(first, await filesUnder(`${directory}/second`));
});

test('a malformed --assets line exits 2 before any Markdown is written', async (t) => {
  for (const bad of ['not json', '[1,2]', JSON.stringify({ address: KEPT, file_path: '/x.png' }), JSON.stringify({ ...GOOD_ASSET, size_bytes: 'big' })]) {
    const directory = await scratchDirectory(t);
    const pages = await pagesIn(directory, [contentWith(`<p><img src="${KEPT}" alt="a"></p>`)]);
    const assets = await assetsFile(directory, [assetLine(KEPT, '/x/ok.png'), bad]);

    const result = await deriveInto(`${directory}/out`, linesOf(pages), ['--assets', assets]);

    assert.equal(result.status, 2, `${bad}: ${result.stderr}`);
    assert.match(result.stderr, /--assets .*line 2: /);
    assert.equal((await filesUnder(`${directory}/out`)).size, 0, 'no Markdown was written');
  }
});

test('an --assets file that cannot be read exits 2 and writes nothing', async (t) => {
  const directory = await scratchDirectory(t);
  const pages = await pagesIn(directory, [contentWith()]);

  const result = await deriveInto(`${directory}/out`, linesOf(pages), ['--assets', `${directory}/absent.jsonl`]);

  assert.equal(result.status, 2);
  assert.match(result.stderr, /cannot read --assets/);
  assert.equal((await filesUnder(`${directory}/out`)).size, 0);
});

test('listing images and pointing at them make no request, even for the addresses they name', async (t) => {
  const site = await startSite(t, { '/pic.png': { headers: { 'content-type': 'image/png' }, body: 'png' } });
  const picture = `${site.origin}/pic.png`;
  const directory = await scratchDirectory(t);
  const pages = await pagesIn(directory, [contentWith(`<p><img src="${picture}" alt="local"></p>`, TABLE(`${site.origin}/pic.png?second`))]);
  const assets = await assetsFile(directory, [assetLine(picture, `${directory}/store/assets/ab/p.png`)]);

  const listed = await runDerive({ input: linesOf(pages), args: ['--list-images'] });
  const derived = await deriveInto(`${directory}/out`, linesOf(pages), ['--assets', assets]);

  assert.equal(listed.status, 0, listed.stderr);
  assert.equal(listed.stdout, `${picture}\n${picture}?second\n`);
  assert.equal(derived.status, 0, derived.stderr);
  assert.deepEqual(site.requests, [], 'neither option asked the site for anything');
});
