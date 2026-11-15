// The block that opens every Markdown file: one `key: <JSON value>` per line between two
// `---` lines. A person reads it as text, a program reads each value with JSON.parse, and
// neither needs a YAML parser to do it.

// What the library did not find comes back as an empty string; either way it is null here.
function givenOrNull(value) {
  return value === undefined || value === null || value === '' ? null : value;
}

// U+2028 and U+2029 are legal inside a JSON string but some readers treat them as line
// breaks, which would split a value across lines.
function jsonValueOnOneLine(value) {
  return JSON.stringify(value).replaceAll('\u2028', '\\u2028').replaceAll('\u2029', '\\u2029');
}

export function frontMatterOf(page, extracted) {
  const fields = [
    ['address', page.address],
    ['final_address', page.final_address],
    ['title', givenOrNull(extracted.title)],
    ['author', givenOrNull(extracted.author)],
    ['published', givenOrNull(extracted.published)],
    ['site', givenOrNull(extracted.site)],
    ['fetched_at', page.fetched_at],
    ['body_sha256', page.sha256],
    ['word_count', givenOrNull(extracted.wordCount)],
  ];
  const lines = fields.map(([key, value]) => `${key}: ${jsonValueOnOneLine(value)}`);
  return ['---', ...lines, '---'].join('\n');
}

// One blank line between the block and the Markdown, and the Markdown exactly as the
// library produced it.
export function markdownFileOf(page, extracted) {
  return `${frontMatterOf(page, extracted)}\n\n${extracted.markdown}`;
}
