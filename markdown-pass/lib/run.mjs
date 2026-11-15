import { isHtmlContentType } from './content-type.mjs';
import { derivePage } from './derive-page.mjs';
import { parsePageLine } from './page-line.mjs';

// A message that reaches standard error is one line, whatever the error it carries.
function oneLine(text) {
  return text.replace(/\s*[\r\n]+\s*/g, ' ').trim();
}

// What became of one input line: 'derived', 'empty', 'skipped' or 'failed'. A bad line or a
// bad page is reported and counted, never thrown, so the rest of the list is still done.
async function outcomeOfLine(line, lineNumber, outDir) {
  const { page, problem } = parsePageLine(line);
  if (problem) {
    console.error(oneLine(`derive: line ${lineNumber}: ${problem}`));
    return 'failed';
  }
  if (!isHtmlContentType(page.content_type)) {
    return 'skipped';
  }
  try {
    return await derivePage(page, outDir);
  } catch (error) {
    console.error(oneLine(`derive: ${page.address}: ${error?.message ?? error}`));
    return 'failed';
  }
}

// Derives every line, one after the other: the extractor holds a whole page's DOM in memory,
// and one at a time keeps the peak at one page. Every line is counted exactly once, so the
// four numbers add up to the lines read.
export async function deriveEveryLine(lines, outDir) {
  const counts = { derived: 0, empty: 0, skipped: 0, failed: 0 };
  let lineNumber = 0;
  for await (const line of lines) {
    lineNumber += 1;
    counts[await outcomeOfLine(line, lineNumber, outDir)] += 1;
  }
  return counts;
}

export function summaryLineOf(counts) {
  return `${counts.derived} derived, ${counts.empty} empty, ${counts.skipped} skipped, ${counts.failed} failed`;
}
