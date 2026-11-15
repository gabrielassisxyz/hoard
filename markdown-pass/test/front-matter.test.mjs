import assert from 'node:assert/strict';
import { test } from 'node:test';
import { Defuddle } from 'defuddle/node';
import { parseHTML } from 'linkedom';
import { frontMatterOf } from '../lib/front-matter.mjs';
import { outputNameOf } from '../lib/output.mjs';
import {
  articleHtml,
  defaultParagraphs,
  deriveInto,
  filesUnder,
  linesOf,
  listedPage,
  scratchDirectory,
} from '../test-support/harness.mjs';

const BLOCK_KEYS = [
  'address', 'final_address', 'title', 'author', 'published', 'site', 'fetched_at', 'body_sha256', 'word_count',
];

// Splits a derived Markdown file into the lines of its block and everything after the one
// blank line that follows it.
function splitMarkdownFile(text) {
  assert.ok(text.startsWith('---\n'), 'the file opens with ---');
  const closing = text.indexOf('\n---\n\n');
  assert.notEqual(closing, -1, 'the block is closed by --- and one blank line');
  const blockLines = text.slice('---\n'.length, closing).split('\n');
  return { blockLines, markdown: text.slice(closing + '\n---\n\n'.length) };
}

function readBlock(blockLines) {
  return blockLines.map((line) => {
    const separator = line.indexOf(': ');
    return [line.slice(0, separator), JSON.parse(line.slice(separator + 2))];
  });
}

async function deriveToFiles(t, html, fields) {
  const directory = await scratchDirectory(t);
  const outDir = `${directory}/out`;
  const page = await listedPage(directory, { body: html, ...fields });
  const result = await deriveInto(outDir, linesOf([page]));
  assert.equal(result.status, 0, result.stderr);
  const files = await filesUnder(outDir);
  const name = outputNameOf(page.address);
  return {
    page,
    markdownFile: files.get(`markdown/${name.slice(0, 2)}/${name}.md`).toString('utf8'),
    cleanFile: files.get(`clean/${name.slice(0, 2)}/${name}.html`).toString('utf8'),
  };
}

test('a Markdown file opens with the nine keys, in order, each as a JSON value on its own line', async (t) => {
  const html = articleHtml({
    title: 'Meta page',
    head: [
      '<meta name="author" content="A. Writer">',
      '<meta property="article:published_time" content="2026-11-01T10:00:00Z">',
      '<meta property="og:site_name" content="Synthetic Site">',
    ].join(''),
  });
  const { markdownFile } = await deriveToFiles(t, html, {
    address: 'https://old.test/a',
    final_address: 'https://new.test/a-final',
    fetched_at: '2026-11-14T22:41:02Z',
    // Not the body's real hash on purpose: the block has to carry what the line says.
    sha256: 'ab'.repeat(32),
  });

  const { blockLines, markdown } = splitMarkdownFile(markdownFile);
  const entries = readBlock(blockLines);

  assert.deepEqual(entries.map(([key]) => key), BLOCK_KEYS);
  const values = Object.fromEntries(entries);
  assert.equal(values.address, 'https://old.test/a');
  assert.equal(values.final_address, 'https://new.test/a-final');
  assert.equal(values.title, 'Meta page');
  assert.equal(values.author, 'A. Writer');
  assert.equal(values.published, '2026-11-01T10:00:00Z');
  assert.equal(values.site, 'Synthetic Site');
  assert.equal(values.fetched_at, '2026-11-14T22:41:02Z');
  assert.equal(values.body_sha256, 'ab'.repeat(32));
  assert.ok(Number.isInteger(values.word_count) && values.word_count > 40, `word_count ${values.word_count}`);
  assert.ok(markdown.length > 0);
});

test('the Markdown after the block, the clean HTML, and the word count are what defuddle returned, untouched', async (t) => {
  const html = articleHtml({
    title: 'Untouched',
    tail: '<h2>A list</h2><ul><li>first <em>item</em></li><li>second <strong>item</strong></li></ul>',
  });
  const { markdownFile, cleanFile } = await deriveToFiles(t, html, { final_address: 'https://new.test/guide/page' });

  // The oracle is the library called directly with the documented options.
  const expected = await Defuddle(parseHTML(html).document, 'https://new.test/guide/page', {
    separateMarkdown: true,
    useAsync: false,
  });

  const { blockLines, markdown } = splitMarkdownFile(markdownFile);
  assert.equal(markdown, expected.contentMarkdown, 'Markdown is result.contentMarkdown, byte for byte');
  assert.equal(cleanFile, expected.content, 'clean HTML is result.content, byte for byte');
  assert.equal(Object.fromEntries(readBlock(blockLines)).word_count, expected.wordCount);
  assert.ok(expected.contentMarkdown.length > 0 && expected.content.startsWith('<'), 'the oracle itself has content to compare');
});

test('what the library did not give is null in the block', async (t) => {
  // No <title>, no heading, no author or date metadata anywhere in the page.
  const paragraphs = defaultParagraphs.map((text) => `<p>${text}</p>`).join('\n');
  const html = `<html><body><article>${paragraphs}</article></body></html>`;
  const { markdownFile } = await deriveToFiles(t, html, {});

  const values = Object.fromEntries(readBlock(splitMarkdownFile(markdownFile).blockLines));

  assert.equal(values.title, null);
  assert.equal(values.author, null);
  assert.equal(values.published, null);
});

test('relative links are resolved against final_address, the address the page was served from', async (t) => {
  const html = articleHtml({ tail: '<p>See <a href="../next/page">the next page</a> and <img src="/img/a.png" alt="pic"></p>' });
  const { markdownFile } = await deriveToFiles(t, html, {
    address: 'https://old.test/requested',
    final_address: 'https://new.test/guide/first/',
  });

  const { markdown } = splitMarkdownFile(markdownFile);

  assert.match(markdown, /\(https:\/\/new\.test\/guide\/next\/page\)/);
  assert.match(markdown, /\(https:\/\/new\.test\/img\/a\.png\)/);
  assert.doesNotMatch(markdown, /old\.test/);
});

// ---- the block, from values the library might or might not give ----------------------------

const listedFields = {
  address: 'https://site.test/a',
  final_address: 'https://site.test/b',
  fetched_at: '2026-11-14T19:41:02Z',
  sha256: 'cd'.repeat(32),
};

test('an empty string, null or undefined from the library is null in the block; a zero is not', () => {
  const absent = { title: '', author: undefined, published: null, site: '', wordCount: undefined };
  const entries = readBlock(frontMatterOf(listedFields, absent).split('\n').slice(1, -1));
  const values = Object.fromEntries(entries);
  assert.deepEqual(
    [values.title, values.author, values.published, values.site, values.word_count],
    [null, null, null, null, null],
  );

  const zero = readBlock(frontMatterOf(listedFields, { ...absent, wordCount: 0 }).split('\n').slice(1, -1));
  assert.equal(Object.fromEntries(zero).word_count, 0);
});

test('a value with quotes, backslashes or line breaks still takes exactly one line', () => {
  const awkward = 'He said "no"\\ and\nthen\r\nleft\u2028for\u2029good';
  const block = frontMatterOf(listedFields, { title: awkward, author: 'A', published: 'P', site: 'S', wordCount: 3 });

  const lines = block.split(/\n/);
  assert.equal(lines.length, 11, 'two --- lines and nine keys');
  assert.ok(!/[\r\u2028\u2029]/.test(block), 'no other kind of line break in it');
  const title = readBlock(lines.slice(1, -1)).find(([key]) => key === 'title')[1];
  assert.equal(title, awkward, 'and the value reads back exactly');
});
