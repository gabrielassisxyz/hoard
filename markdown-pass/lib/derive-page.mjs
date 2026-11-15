import { readFile } from 'node:fs/promises';
import { zstdDecompressSync } from 'node:zlib';
import { decodeBody } from './charset.mjs';
import { extractPage } from './extract.mjs';
import { markdownFileOf } from './front-matter.mjs';
import { outputPathsOf, writeFilesAtomically } from './output.mjs';

// Each step says which one it was in, so the one line a failed page leaves on standard
// error is enough to tell a missing file from a corrupt one from a page the extractor
// could not make sense of.
async function readStoredBody(bodyPath) {
  let compressed;
  try {
    compressed = await readFile(bodyPath);
  } catch (error) {
    throw new Error(`cannot read the body: ${error.message}`, { cause: error });
  }
  try {
    return zstdDecompressSync(compressed);
  } catch (error) {
    throw new Error(`cannot decompress the body: ${error.message}`, { cause: error });
  }
}

async function extractFrom(html, finalAddress) {
  try {
    return await extractPage(html, finalAddress);
  } catch (error) {
    throw new Error(`extraction failed: ${error.message}`, { cause: error });
  }
}

// The Markdown is the reading copy. The cleaned HTML it was made from is written only on
// request: nothing reads it, it can be made again from the stored page at any time, and on
// a real capture it took nearly as much room as the compressed pages themselves.
async function writeDerivedFiles(outDir, page, extracted, { cleanHtml = false }) {
  const paths = outputPathsOf(outDir, page.address);
  const files = [{ path: paths.markdown, content: markdownFileOf(page, extracted) }];
  if (cleanHtml) {
    files.push({ path: paths.cleanHtml, content: extracted.cleanHtml });
  }
  try {
    await writeFilesAtomically(files);
  } catch (error) {
    throw new Error(`cannot write the output: ${error.message}`, { cause: error });
  }
}

// A page is empty when its Markdown holds nothing a person could read. White space alone
// counts as nothing: the library trims what it returns today, and this keeps a page of
// line breaks from being counted as derived if it ever stops.
export function outcomeOfMarkdown(markdown) {
  return markdown.trim() === '' ? 'empty' : 'derived';
}

// Derives one listed page into outDir and says what came of it: 'derived', or 'empty' when
// the Markdown is blank (its files are written all the same, so the page is not lost from
// the output). Anything that goes wrong throws, and nothing has been left under a final name.
//
// note is called with anything worth saying about a page that was derived all the same.
export async function derivePage(page, outDir, note = () => {}, options = {}) {
  const bytes = await readStoredBody(page.body_path);
  const html = decodeBody(bytes, page.content_type, (charset) => {
    note(`no charset is declared and the bytes are not valid UTF-8: read as ${charset}`);
  });
  const extracted = await extractFrom(html, page.final_address);
  await writeDerivedFiles(outDir, page, extracted, options);
  return outcomeOfMarkdown(extracted.markdown);
}
