// Shared by the tests. It lives outside test/ on purpose: `node --test` runs every file under
// a directory named test, and a helper module is not a test.
import { spawn } from 'node:child_process';
import { createHash } from 'node:crypto';
import http from 'node:http';
import { mkdir, mkdtemp, readdir, readFile, rm, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { zstdCompressSync } from 'node:zlib';

export const repositoryRoot = fileURLToPath(new URL('../../', import.meta.url));
export const deriveScript = fileURLToPath(new URL('../derive.mjs', import.meta.url));

export async function scratchDirectory(t) {
  const directory = await mkdtemp(path.join(os.tmpdir(), 'hoard-markdown-pass-'));
  t.after(() => rm(directory, { recursive: true, force: true }));
  return directory;
}

// Runs a program to completion with `input` on its standard input. It is asynchronous because
// some tests keep a web server in this very process, and a blocked event loop would leave
// the program under test waiting on a server that cannot answer.
export function runCommand(command, args, { cwd, input = '', env = {} } = {}) {
  return new Promise((resolve, reject) => {
    // A child of `node --test` inherits the runner's own variables; this one is not a test.
    const childEnvironment = { ...process.env, ...env };
    delete childEnvironment.NODE_TEST_CONTEXT;
    const child = spawn(command, args, { cwd, env: childEnvironment, stdio: ['pipe', 'pipe', 'pipe'] });
    const stdout = [];
    const stderr = [];
    child.stdout.on('data', (chunk) => stdout.push(chunk));
    child.stderr.on('data', (chunk) => stderr.push(chunk));
    child.on('error', (error) => reject(new Error(`could not run ${command}: ${error.message}`)));
    child.on('close', (status) => {
      resolve({
        status,
        stdout: Buffer.concat(stdout).toString('utf8'),
        stderr: Buffer.concat(stderr).toString('utf8'),
      });
    });
    // The program may exit before it reads everything; that is the test's finding to make.
    child.stdin.on('error', () => {});
    child.stdin.end(input);
  });
}

export function runDerive({ input = '', args = [] }) {
  return runCommand(process.execPath, [deriveScript, ...args], { input });
}

export function deriveInto(outDir, input) {
  return runDerive({ input, args: ['--out', outDir] });
}

// Writes a body the way the store does, zstd-compressed under the SHA-256 of its plain bytes,
// and returns the object `hoard list --json` would print for it. `fields` replace any key.
export async function listedPage(directory, { body, ...fields }) {
  const bytes = Buffer.isBuffer(body) ? body : Buffer.from(body, 'utf8');
  const sha256 = createHash('sha256').update(bytes).digest('hex');
  const bodyPath = path.join(directory, 'raw', sha256.slice(0, 2), `${sha256}.zst`);
  await mkdir(path.dirname(bodyPath), { recursive: true });
  await writeFile(bodyPath, zstdCompressSync(bytes));
  const page = {
    address: `https://site.test/${sha256.slice(0, 12)}`,
    http_status: 200,
    fetched_at: '2026-11-14T19:41:02Z',
    content_type: 'text/html; charset=utf-8',
    sha256,
    size_bytes: bytes.length,
    body_path: bodyPath,
    ...fields,
  };
  return { final_address: page.address, ...page };
}

// Objects become JSON lines; a string goes in as it is, so a test can feed a line that is
// not JSON at all.
export function linesOf(items) {
  return items.map((item) => (typeof item === 'string' ? item : JSON.stringify(item))).join('\n') + '\n';
}

// Every file under a directory, by path relative to it. A directory that is not there holds
// nothing.
export async function filesUnder(directory) {
  const files = new Map();
  async function visit(current) {
    let entries;
    try {
      entries = await readdir(current, { withFileTypes: true });
    } catch (error) {
      if (error.code === 'ENOENT') {
        return;
      }
      throw error;
    }
    for (const entry of entries) {
      const full = path.join(current, entry.name);
      if (entry.isDirectory()) {
        await visit(full);
      } else {
        files.set(path.relative(directory, full), await readFile(full));
      }
    }
  }
  await visit(directory);
  return new Map([...files].sort(([left], [right]) => (left < right ? -1 : 1)));
}

export const defaultParagraphs = [
  'Gardeners sharpen their tools through the long winter months so that the spring work goes quickly and cleanly.',
  'A dull blade crushes the stem instead of cutting it, and the plant spends a week recovering from a job that took a second.',
  'Oil keeps the metal from rusting, and a stone brings the edge back in a few minutes if it is used regularly.',
  'The handles deserve attention too, because a split handle in the middle of a morning is a long walk back to the shed.',
  'Most of this is habit rather than skill, which is why the people who do it well rarely think about it at all.',
  'By the first warm weekend every tool hangs ready on its nail, and nobody remembers the cold afternoons that did it.',
];

// A page with enough article text for the extractor to keep, wrapped in the navigation and
// footer it is meant to throw away. Everything in it is made up for the tests.
export function articleHtml({
  title = 'Synthetic article',
  charsetMeta = '<meta charset="utf-8">',
  head = '',
  paragraphs = defaultParagraphs,
  tail = '',
} = {}) {
  const body = paragraphs.map((text) => `<p>${text}</p>`).join('\n');
  return [
    '<!doctype html>',
    `<html><head>${charsetMeta}<title>${title}</title>${head}</head>`,
    '<body>',
    '<nav><a href="/">Home</a> <a href="/about">About</a></nav>',
    `<article>\n<h1>${title}</h1>\n${body}\n${tail}</article>`,
    '<footer>Footer text outside the article.</footer>',
    '</body></html>',
  ].join('\n');
}

// A web server on a local port that answers from a fixed table and remembers every request,
// so a test can say how many requests a program made and to what.
export async function startSite(t, routes) {
  const requests = [];
  const server = http.createServer((request, response) => {
    requests.push(request.url);
    const route = routes[request.url.split('?')[0]];
    if (!route) {
      response.writeHead(404, { 'content-type': 'text/plain' });
      response.end('missing');
      return;
    }
    const { status = 200, headers = {}, body = '' } = route;
    response.writeHead(status, headers);
    response.end(body);
  });
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  t.after(() => {
    server.closeAllConnections();
    return new Promise((resolve) => server.close(resolve));
  });
  return { origin: `http://127.0.0.1:${server.address().port}`, requests };
}
