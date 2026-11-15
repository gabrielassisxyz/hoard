// Finds the images of a page's Markdown and points them at local copies. Only the address
// inside an image is ever read or replaced: each reference is a span of the text holding
// that address and nothing around it, so alt text, a title and every other byte stay as the
// extractor wrote them.
//
// Two forms are handled, the two the extractor produces:
//   - Markdown images, `![alt](destination "title")`: alt text with nested and escaped
//     brackets, a destination in angle brackets or bare with balanced or backslash-escaped
//     parentheses (what turndown writes for an address that has them), and a title in
//     double quotes, single quotes or parentheses.
//   - `<img src=...>` in HTML the extractor kept as it was (tables, mostly), the value
//     quoted either way or bare.
// Not handled: reference-style images (`![alt][ref]`), `srcset`, `<picture>` sources, and an
// image whose alt text or title is split over lines. None of them is written by the
// extractor today. Fenced code blocks and inline code spans are left alone, so an example of
// image syntax in a post is not mistaken for an image.
import path from 'node:path';

// An embedded video is written by the extractor in image syntax. Matching the registered
// name or any subdomain of it covers www., m. and the like without matching a different
// site that merely ends in the same letters.
const VIDEO_HOSTS = ['youtube.com', 'youtu.be', 'vimeo.com'];

const FENCE_OPENER = /^ {0,3}(`{3,}|~{3,})/;
const HTML_IMG_OPENER = /^<img[\s/>]/i;
const SRC_ATTRIBUTE = /\ssrc\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>]+))/di;
const ASCII_PUNCTUATION_ESCAPE = /\\([!-/:-@[-`{-~])/g;

export function isListableAddress(address) {
  let url;
  try {
    url = new URL(address);
  } catch {
    return false;
  }
  if (url.protocol !== 'http:' && url.protocol !== 'https:') {
    return false;
  }
  const host = url.hostname.toLowerCase();
  return !VIDEO_HOSTS.some((video) => host === video || host.endsWith(`.${video}`));
}

// The text outside fenced code blocks, as [start, end) spans. A fence stays open to the end
// of the text when it is never closed, as CommonMark has it.
function proseRegions(markdown) {
  const regions = [];
  let regionStart = 0;
  let open = null;
  let offset = 0;
  for (const line of markdown.split('\n')) {
    const lineEnd = offset + line.length + 1;
    const marker = FENCE_OPENER.exec(line)?.[1];
    if (open === null && marker) {
      regions.push([regionStart, offset]);
      open = marker;
    } else if (open !== null && marker && marker[0] === open[0] && marker.length >= open.length) {
      open = null;
      regionStart = lineEnd;
    }
    offset = lineEnd;
  }
  if (open === null) {
    regions.push([regionStart, markdown.length]);
  }
  return regions;
}

function skipWhitespace(text, from, end) {
  let index = from;
  while (index < end && /\s/.test(text[index])) {
    index += 1;
  }
  return index;
}

// The closing bracket matching the one the text opens at `from - 1`, or -1.
function closingBracket(text, from, end) {
  let depth = 1;
  for (let index = from; index < end; index += 1) {
    if (text[index] === '\\') {
      index += 1;
    } else if (text[index] === '[') {
      depth += 1;
    } else if (text[index] === ']' && --depth === 0) {
      return index;
    }
  }
  return -1;
}

// { start, end } of the destination's own text, without the angle brackets, and where the
// text goes on after it; or null.
function destinationAt(text, from, end) {
  if (text[from] === '<') {
    for (let index = from + 1; index < end && text[index] !== '\n'; index += 1) {
      if (text[index] === '\\') {
        index += 1;
      } else if (text[index] === '>') {
        return { start: from + 1, end: index, next: index + 1 };
      }
    }
    return null;
  }
  let parentheses = 0;
  let index = from;
  for (; index < end && !/\s/.test(text[index]); index += 1) {
    if (text[index] === '\\') {
      index += 1;
    } else if (text[index] === '(') {
      parentheses += 1;
    } else if (text[index] === ')' && parentheses-- === 0) {
      parentheses = 0;
      break;
    }
  }
  return { start: from, end: index, next: index };
}

// Where the title that opens at `from` ends, just past its closing delimiter, or -1.
function titleEnd(text, from, end) {
  const closer = { '"': '"', "'": "'", '(': ')' }[text[from]];
  for (let index = from + 1; index < end; index += 1) {
    if (text[index] === '\\') {
      index += 1;
    } else if (text[index] === closer) {
      return index + 1;
    }
  }
  return -1;
}

function markdownImageAt(text, bang, end) {
  const close = closingBracket(text, bang + 2, end);
  if (close === -1 || text[close + 1] !== '(') {
    return null;
  }
  const destination = destinationAt(text, skipWhitespace(text, close + 2, end), end);
  if (destination === null) {
    return null;
  }
  let next = skipWhitespace(text, destination.next, end);
  if (next > destination.next && '"\'('.includes(text[next] ?? '')) {
    next = titleEnd(text, next, end);
    next = next === -1 ? -1 : skipWhitespace(text, next, end);
  }
  if (next === -1 || text[next] !== ')') {
    return null;
  }
  const raw = text.slice(destination.start, destination.end);
  return { start: destination.start, end: destination.end, address: raw.replace(ASCII_PUNCTUATION_ESCAPE, '$1'), after: next + 1 };
}

function decodeAttributeEntities(value) {
  const named = { amp: '&', quot: '"', apos: "'", lt: '<', gt: '>' };
  return value.replace(/&(?:#(\d+)|#x([0-9a-f]+)|(amp|quot|apos|lt|gt));/gi, (whole, decimal, hex, name) => {
    if (name) {
      return named[name.toLowerCase()];
    }
    const code = decimal ? Number(decimal) : parseInt(hex, 16);
    return code <= 0x10ffff ? String.fromCodePoint(code) : whole;
  });
}

// The end of the tag that opens at `from`: its `>`, outside any quoted attribute value.
function tagEnd(text, from, end) {
  let quote = null;
  for (let index = from; index < end; index += 1) {
    if (quote !== null) {
      quote = text[index] === quote ? null : quote;
    } else if (text[index] === '"' || text[index] === "'") {
      quote = text[index];
    } else if (text[index] === '>') {
      return index + 1;
    }
  }
  return -1;
}

function htmlImageAt(text, from, end) {
  const after = tagEnd(text, from, end);
  if (after === -1) {
    return null;
  }
  const match = SRC_ATTRIBUTE.exec(text.slice(from, after));
  if (match === null) {
    return { after };
  }
  const group = [1, 2, 3].find((number) => match[number] !== undefined);
  const [valueStart, valueEnd] = match.indices[group];
  return { start: from + valueStart, end: from + valueEnd, address: decodeAttributeEntities(match[group]), after };
}

// A backtick run closes at the next run of the same length; with none, it is plain text.
function afterCodeSpan(text, from, end) {
  let length = 0;
  while (text[from + length] === '`') {
    length += 1;
  }
  const closing = new RegExp(`(?<!\`)\`{${length}}(?!\`)`, 'g');
  closing.lastIndex = from + length;
  const found = closing.exec(text.slice(0, end));
  return found === null ? from + length : found.index + length;
}

function scanRegion(text, start, end, references) {
  let index = start;
  while (index < end) {
    const found =
      text[index] === '!' && text[index + 1] === '[' && text[index - 1] !== '\\' ? markdownImageAt(text, index, end)
      : text[index] === '<' && HTML_IMG_OPENER.test(text.slice(index, index + 6)) ? htmlImageAt(text, index, end)
      : null;
    if (found?.address !== undefined) {
      references.push({ start: found.start, end: found.end, address: found.address });
    }
    if (found) {
      index = found.after;
    } else {
      index = text[index] === '`' ? afterCodeSpan(text, index, end) : index + 1;
    }
  }
}

// Every image reference of the text, in the order they stand, each with the span that holds
// its address.
export function imageReferencesOf(markdown) {
  const references = [];
  for (const [start, end] of proseRegions(markdown)) {
    scanRegion(markdown, start, end, references);
  }
  return references;
}

// The addresses worth fetching, each once, in the order first met.
export function listableAddressesOf(markdown) {
  const seen = new Set();
  for (const { address } of imageReferencesOf(markdown)) {
    if (isListableAddress(address)) {
      seen.add(address);
    }
  }
  return [...seen];
}

// A path as it is written inside an image: relative to the Markdown file's directory, with
// every segment percent-encoded so a space, a parenthesis or an angle bracket in the store's
// location cannot end the image early. The result has no character that needs escaping in
// Markdown or in an HTML attribute.
export function relativeImagePath(markdownPath, imagePath) {
  const relative = path.relative(path.dirname(markdownPath), imagePath);
  return relative
    .split(path.sep)
    .map((segment) => encodeURIComponent(segment).replace(/[!'()*]/g, (c) => `%${c.charCodeAt(0).toString(16).toUpperCase()}`))
    .join('/');
}

// The Markdown with each image whose address `pathFor` knows pointed at what it returns.
// Anything `pathFor` answers undefined for keeps its address.
export function pointImagesAt(markdown, pathFor) {
  let result = '';
  let copied = 0;
  for (const { start, end, address } of imageReferencesOf(markdown)) {
    const replacement = pathFor(address);
    if (replacement !== undefined) {
      result += markdown.slice(copied, start) + replacement;
      copied = end;
    }
  }
  return result + markdown.slice(copied);
}

const ASSET_STRING_KEYS = ['address', 'final_address', 'content_type', 'sha256', 'file_path'];

// The lines `hoard list --assets --json` prints, as a map from the address that was asked
// for to the image file's path. Every line is checked and every problem reported at once,
// because a list read in part would leave some images remote without a word. When two lines
// hold one address the later wins.
export function parseAssetLines(text) {
  const files = new Map();
  const problems = [];
  text.split('\n').forEach((line, index) => {
    if (line === '') {
      return;
    }
    const { asset, problem } = readAssetLine(line);
    if (problem) {
      problems.push(`line ${index + 1}: ${problem}`);
    } else {
      files.set(asset.address, asset.file_path);
    }
  });
  return { files, problems };
}

function readAssetLine(line) {
  let value;
  try {
    value = JSON.parse(line);
  } catch (error) {
    return { problem: `not valid JSON: ${error.message}` };
  }
  if (value === null || typeof value !== 'object' || Array.isArray(value)) {
    return { problem: 'not a JSON object' };
  }
  const unusable = [
    ...ASSET_STRING_KEYS.filter((key) => typeof value[key] !== 'string' || (key === 'file_path' && value[key] === '')),
    ...(Number.isInteger(value.size_bytes) ? [] : ['size_bytes']),
  ];
  return unusable.length > 0 ? { problem: `missing or mistyped keys: ${unusable.join(', ')}` } : { asset: value };
}
