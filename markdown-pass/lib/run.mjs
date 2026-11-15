import { isHtmlContentType } from './content-type.mjs';
import { derivePage, extractStoredPage } from './derive-page.mjs';
import { listableAddressesOf } from './images.mjs';
import { parsePageLine } from './page-line.mjs';

// A message that reaches standard error is one line, whatever the error it carries.
function oneLine(text) {
  return text.replace(/\s*[\r\n]+\s*/g, ' ').trim();
}

// Runs one input line's work on its page and says what became of it: 'skipped' when the page
// is not HTML, 'failed' when the line or the work went wrong, else what the work returns. A
// bad line or a bad page is reported and counted, never thrown, so the rest of the list is
// still done. The work is called with the page and a function to report a note about it.
async function outcomeOfLine(line, lineNumber, work) {
  const { page, problem } = parsePageLine(line);
  if (problem) {
    console.error(oneLine(`derive: line ${lineNumber}: ${problem}`));
    return 'failed';
  }
  if (!isHtmlContentType(page.content_type)) {
    return 'skipped';
  }
  try {
    return await work(page, (message) => {
      console.error(oneLine(`derive: ${page.address}: ${message}`));
    });
  } catch (error) {
    console.error(oneLine(`derive: ${page.address}: ${error?.message ?? error}`));
    return 'failed';
  }
}

// Derives every line, one after the other: the extractor holds a whole page's DOM in memory,
// and one at a time keeps the peak at one page. Every line is counted exactly once, so the
// four numbers add up to the lines read.
//
// options.cleanHtml asks for the cleaned HTML of each page beside its Markdown, and
// options.assets, a map from an image's address to its file, for the images the store holds
// to be pointed at in the Markdown.
export async function deriveEveryLine(lines, outDir, options = {}) {
  const counts = { derived: 0, empty: 0, skipped: 0, failed: 0 };
  let lineNumber = 0;
  for await (const line of lines) {
    lineNumber += 1;
    counts[await outcomeOfLine(line, lineNumber, (page, note) => derivePage(page, outDir, note, options))] += 1;
  }
  return counts;
}

// The image addresses of every page's content, each once in the order first met, and how
// many lines failed. It writes nothing, and goes through the same extraction a derivation
// does, so the list names exactly the images a derived page would show.
export async function listImagesOfEveryLine(lines) {
  const addresses = new Set();
  let failed = 0;
  let lineNumber = 0;
  for await (const line of lines) {
    lineNumber += 1;
    const outcome = await outcomeOfLine(line, lineNumber, async (page, note) => {
      const { markdown } = await extractStoredPage(page, note);
      listableAddressesOf(markdown).forEach((address) => addresses.add(address));
      return 'listed';
    });
    failed += outcome === 'failed' ? 1 : 0;
  }
  return { addresses: [...addresses], failed };
}

export function summaryLineOf(counts) {
  return `${counts.derived} derived, ${counts.empty} empty, ${counts.skipped} skipped, ${counts.failed} failed`;
}
