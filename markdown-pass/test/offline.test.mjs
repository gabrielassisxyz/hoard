import assert from 'node:assert/strict';
import { test } from 'node:test';
import { Defuddle } from 'defuddle/node';
import { parseHTML } from 'linkedom';
import { extractPage } from '../lib/extract.mjs';
import { outputNameOf } from '../lib/output.mjs';
import {
  articleHtml,
  deriveInto,
  filesUnder,
  linesOf,
  listedPage,
  scratchDirectory,
  startSite,
} from '../test-support/harness.mjs';

test('a page that references an image, a script, a stylesheet and an iframe is derived without one request', async (t) => {
  const site = await startSite(t, {
    '/pixel.png': { headers: { 'content-type': 'image/png' }, body: 'png' },
    '/relative.png': { headers: { 'content-type': 'image/png' }, body: 'png' },
    '/app.js': { headers: { 'content-type': 'text/javascript' }, body: 'window.x = 1' },
    '/site.css': { headers: { 'content-type': 'text/css' }, body: 'body {}' },
    '/frame.html': { headers: { 'content-type': 'text/html' }, body: '<p>frame</p>' },
    '/page': { headers: { 'content-type': 'text/html' }, body: '<p>the page itself</p>' },
  });
  const directory = await scratchDirectory(t);
  const outDir = `${directory}/out`;
  const html = articleHtml({
    head: `<link rel="stylesheet" href="${site.origin}/site.css"><script src="${site.origin}/app.js"></script>`,
    tail: [
      `<p><img src="${site.origin}/pixel.png" alt="absolute picture"></p>`,
      '<p><img src="/relative.png" alt="relative picture"></p>',
      `<iframe src="${site.origin}/frame.html"></iframe>`,
    ].join('\n'),
  });
  const page = await listedPage(directory, { body: html, address: `${site.origin}/page`, final_address: `${site.origin}/page` });

  const result = await deriveInto(outDir, linesOf([page]));

  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stdout.trimEnd().split('\n').at(-1), '1 derived, 0 empty, 0 skipped, 0 failed');
  const name = outputNameOf(page.address);
  const markdown = (await filesUnder(outDir)).get(`markdown/${name.slice(0, 2)}/${name}.md`).toString('utf8');
  // The references were in the page that went through the extractor; none of them was followed.
  assert.ok(markdown.includes(`${site.origin}/pixel.png`), 'the absolute image reached the extractor');
  assert.ok(markdown.includes(`${site.origin}/relative.png`), 'the relative image reached the extractor, resolved against the page');
  assert.deepEqual(site.requests, [], 'the pass made no request');
});

// The extractors defuddle ships for a few well-known sites fetch transcripts and threads on
// their own when asked to. The address below only has to look like one of them to the
// library; nothing is requested, because fetch is replaced for the length of the call.
const ASYNC_EXTRACTOR_ADDRESS = 'https://www.youtube.com.site.test/watch?v=synthetic';

async function fetchCallsDuring(work) {
  const calls = [];
  const realFetch = globalThis.fetch;
  globalThis.fetch = async (address) => {
    calls.push(String(address));
    throw new Error('no network in this test');
  };
  try {
    await work();
  } finally {
    globalThis.fetch = realFetch;
  }
  return calls;
}

test('extraction never asks the library to fetch anything of its own', async () => {
  const html = articleHtml({ title: 'A clip' });
  const quiet = () => {};
  const realConsoleError = console.error;
  console.error = quiet;
  try {
    // The control: with the library's own default, the address above does make it fetch. It
    // keeps this test honest: if a library update made that stop being true, the check below
    // would pass for the wrong reason, and this one says so.
    const controlCalls = await fetchCallsDuring(() => Defuddle(parseHTML(html).document, ASYNC_EXTRACTOR_ADDRESS, { separateMarkdown: true }));
    assert.ok(controlCalls.length > 0, 'the library fetches for this address when it is allowed to');

    const calls = await fetchCallsDuring(() => extractPage(html, ASYNC_EXTRACTOR_ADDRESS));
    assert.deepEqual(calls, [], 'the pass, with its options, fetched nothing');
  } finally {
    console.error = realConsoleError;
  }
});
