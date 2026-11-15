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

async function writeDerivedFiles(outDir, page, extracted) {
  const paths = outputPathsOf(outDir, page.address);
  try {
    await writeFilesAtomically([
      { path: paths.markdown, content: markdownFileOf(page, extracted) },
      { path: paths.cleanHtml, content: extracted.cleanHtml },
    ]);
  } catch (error) {
    throw new Error(`cannot write the output: ${error.message}`, { cause: error });
  }
}

// Derives one listed page into outDir and says what came of it: 'derived', or 'empty' when
// the Markdown is blank (its files are written all the same, so the page is not lost from
// the output). Anything that goes wrong throws, and nothing has been left under a final name.
export async function derivePage(page, outDir) {
  const bytes = await readStoredBody(page.body_path);
  const html = decodeBody(bytes, page.content_type);
  const extracted = await extractFrom(html, page.final_address);
  await writeDerivedFiles(outDir, page, extracted);
  return extracted.markdown.trim() === '' ? 'empty' : 'derived';
}
