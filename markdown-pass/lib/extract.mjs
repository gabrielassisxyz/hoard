import { Defuddle } from 'defuddle/node';
import { parseHTML } from 'linkedom';

// These are the options the extractors were measured with. useAsync: false is the one that
// matters for the rule that this pass never touches the network: with it on, the library
// fetches transcripts, threads and the like from third-party hosts on its own.
export const EXTRACTION_OPTIONS = Object.freeze({ separateMarkdown: true, useAsync: false });

// Runs the original defuddle over one page's HTML. finalAddress is the base for every
// relative link in the result, so it has to be where the page was actually served from.
export async function extractPage(html, finalAddress) {
  const { document } = parseHTML(html);
  const result = await Defuddle(document, finalAddress, { ...EXTRACTION_OPTIONS });
  return {
    cleanHtml: result.content ?? '',
    markdown: result.contentMarkdown ?? '',
    title: result.title,
    author: result.author,
    published: result.published,
    site: result.site,
    wordCount: result.wordCount,
  };
}
