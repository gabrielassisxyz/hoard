import assert from 'node:assert/strict';
import { test } from 'node:test';
import { charsetDeclaredInMeta, decodeBody, LEGACY_FALLBACK } from '../lib/charset.mjs';
import {
  articleHtml,
  deriveInto,
  filesUnder,
  linesOf,
  listedPage,
  scratchDirectory,
} from '../test-support/harness.mjs';

const latin1 = (text) => Buffer.from(text, 'latin1');
const utf8 = (text) => Buffer.from(text, 'utf8');

// Every character in these is also in Latin-1, so the same text can be stored in either
// charset and told apart by how its bytes are read back.
const FRENCH = [
  'Le café est prêt, et on le sert dans le jardin pendant que les outils reposent sur leur clou.',
  'Une lame émoussée écrase la tige au lieu de la couper, et la plante met une semaine à se remettre.',
  "On huile le métal afin qu'il ne rouille pas, et une pierre ravive le fil en quelques minutes si on s'en sert souvent.",
  "Les manches méritent aussi de l'attention, car un manche fendu au milieu de la matinée oblige à retourner à la remise.",
  "Presque tout cela relève de l'habitude plutôt que du talent, et c'est pourquoi on n'y pense presque jamais.",
  'Au premier week-end chaud, chaque outil est accroché à son clou, et personne ne se souvient des après-midi froids.',
];

// ---- what a page declares about itself --------------------------------------------------

test('a <meta charset> tag is read in every spelling a page uses', () => {
  const declarations = {
    '<meta charset="iso-8859-1">': 'iso-8859-1',
    "<meta charset='koi8-r'>": 'koi8-r',
    '<meta charset=windows-1250>': 'windows-1250',
    '<META CHARSET="UTF-8">': 'UTF-8',
    '<meta   charset = "shift_jis"  >': 'shift_jis',
    '<meta charset="euc-kr" />': 'euc-kr',
    '<meta charset=gbk/>': 'gbk',
  };
  for (const [tag, charset] of Object.entries(declarations)) {
    assert.equal(charsetDeclaredInMeta(utf8(`<html><head>${tag}</head>`)), charset, tag);
  }
});

test('a <meta http-equiv="Content-Type"> tag declares the charset in its content', () => {
  const declarations = {
    '<meta http-equiv="Content-Type" content="text/html; charset=windows-1252">': 'windows-1252',
    '<meta content="text/html; charset=iso-8859-2" http-equiv="content-type">': 'iso-8859-2',
    "<meta HTTP-EQUIV='Content-Type' CONTENT='text/html;charset=\"big5\"'>": 'big5',
    '<meta http-equiv=Content-Type content=text/html;charset=latin1>': 'latin1',
  };
  for (const [tag, charset] of Object.entries(declarations)) {
    assert.equal(charsetDeclaredInMeta(utf8(`<head>${tag}</head>`)), charset, tag);
  }
});

test('a <meta> tag that does not declare a charset declares none', () => {
  const tags = [
    '<meta name="description" content="charset=koi8-r">',
    '<meta http-equiv="refresh" content="0; url=/next; charset=koi8-r">',
    '<meta name="viewport" content="width=device-width">',
    '<meta charset="">',
    '<metadata charset="koi8-r">',
    '<p>no tag at all</p>',
  ];
  for (const tag of tags) {
    assert.equal(charsetDeclaredInMeta(utf8(`<head>${tag}</head>`)), null, tag);
  }
});

test('the first tag that declares a charset wins, and a commented-out one declares nothing', () => {
  const twoDeclarations = utf8('<meta name="viewport" content="x"><meta charset="windows-1252"><meta charset="utf-8">');
  assert.equal(charsetDeclaredInMeta(twoDeclarations), 'windows-1252');

  const commented = utf8('<!-- <meta charset="koi8-r"> --><meta charset="utf-8">');
  assert.equal(charsetDeclaredInMeta(commented), 'utf-8');

  const unterminatedComment = utf8('<!-- never closed <meta charset="koi8-r">');
  assert.equal(charsetDeclaredInMeta(unterminatedComment), null);
});

// A page, in UTF-8, whose <meta charset="iso-8859-1"> tag ends at byte number `endsAtByte`,
// counting from 1, after a <meta> of filler that declares nothing. A tag counts only when it
// is complete within the first 1024 bytes, so 1024 is in and 1025 is out.
function articleWithCharsetTagEndingAtByte(endsAtByte) {
  const tag = '<meta charset="iso-8859-1">';
  const build = (filler) => articleHtml({
    charsetMeta: `<meta name="filler" content="${filler}">${tag}`,
    paragraphs: FRENCH,
  });
  const unfilled = build('');
  const endOfTag = Buffer.byteLength(unfilled.slice(0, unfilled.indexOf(tag) + tag.length));
  return build('a'.repeat(endsAtByte - endOfTag));
}

test('only a tag that is complete within the first 1024 bytes counts', () => {
  assert.equal(charsetDeclaredInMeta(utf8(articleWithCharsetTagEndingAtByte(1024))), 'iso-8859-1', 'a tag whose last byte is byte 1024');
  assert.equal(charsetDeclaredInMeta(utf8(articleWithCharsetTagEndingAtByte(1025))), null, 'a tag whose last byte is byte 1025');
});

// ---- which charset the bytes are decoded with --------------------------------------------

test("the Content-Type header's charset decides, even against what the page says", () => {
  const body = utf8('<meta charset="iso-8859-1"><p>café</p>');
  assert.match(decodeBody(body, 'text/html; charset=utf-8'), /café/);
  assert.match(decodeBody(body, 'text/html; charset=iso-8859-1'), /cafÃ©/);
});

test("with no charset in the header the page's own declaration decides, then UTF-8", () => {
  const declaredLatin1 = latin1('<meta charset="iso-8859-1"><p>café</p>');
  assert.match(decodeBody(declaredLatin1, 'text/html'), /café/);
  assert.match(decodeBody(declaredLatin1, ''), /café/);

  const declaredByHttpEquiv = latin1('<meta http-equiv="Content-Type" content="text/html; charset=windows-1252"><p>café</p>');
  assert.match(decodeBody(declaredByHttpEquiv, 'text/html'), /café/);

  const undeclared = utf8('<p>café</p>');
  assert.match(decodeBody(undeclared, 'text/html'), /café/);
});

test('windows-1252 is decoded as the web means it, not as strict Latin-1', () => {
  // Byte 0x80 is the euro sign in windows-1252 and a control character in strict Latin-1.
  // Servers that say iso-8859-1 almost always mean windows-1252, and so does the decoder.
  const body = Buffer.from([0x80, 0xe9]);
  assert.equal(decodeBody(body, 'text/html; charset=windows-1252'), '€é');
  assert.equal(decodeBody(body, 'text/html; charset=iso-8859-1'), '€é');
  assert.equal(decodeBody(body, 'text/html; charset=latin1'), '€é');
});

test('an unknown charset, or bytes that are not in the charset, cannot be decoded', () => {
  assert.throws(() => decodeBody(utf8('<p>x</p>'), 'text/html; charset=no-such-charset'), /unsupported charset "no-such-charset"/);
  assert.throws(() => decodeBody(utf8('<meta charset="no-such-charset">'), 'text/html'), /unsupported charset "no-such-charset"/);
  // A page that says it is UTF-8 and is not: a replacement character here would be silent
  // damage, and so would a guess made over what the page declared.
  assert.throws(() => decodeBody(latin1('<p>café</p>'), 'text/html; charset=utf-8'), /not valid utf-8/);
  assert.throws(() => decodeBody(latin1('<meta charset="utf-8"><p>café</p>'), 'text/html'), /not valid utf-8/);
});

// ---- a page that declares nothing ----------------------------------------------------------

// Calls decodeBody and returns the text with every charset the fallback was noted for.
function decodeNoting(bytes, contentType) {
  const noted = [];
  const text = decodeBody(bytes, contentType, (charset) => noted.push(charset));
  return { text, noted };
}

test('a page that declares nothing and is not valid UTF-8 is read as windows-1252, and that is noted', () => {
  // é, an em dash and a curly apostrophe, as a page written in windows-1252 stores them.
  // Each stands apart: run together, 0xE9 0x97 0x92 is also one valid UTF-8 character, and
  // a page that is valid UTF-8 is read as UTF-8.
  // The last byte, 0xE3, is ã in windows-1252 and another letter in its Central European
  // sibling, which shares the other three.
  const { text, noted } = decodeNoting(Buffer.from([0x63, 0x61, 0x66, 0xe9, 0x20, 0x97, 0x20, 0x92, 0x20, 0xe3]), 'text/html');
  assert.equal(text, 'café — ’ ã');
  assert.deepEqual(noted, [LEGACY_FALLBACK]);
  assert.equal(LEGACY_FALLBACK, 'windows-1252');
});

test('a page that declares nothing and is valid UTF-8 is read as UTF-8, and nothing is noted', () => {
  // 0xC3 0xA9 is é in UTF-8 and Ã© in windows-1252: trying the fallback first would show.
  const { text, noted } = decodeNoting(Buffer.from([0x63, 0x61, 0x66, 0xc3, 0xa9]), 'text/html');
  assert.equal(text, 'café');
  assert.deepEqual(noted, []);
});

test('the fallback reads every byte value', () => {
  const everyByte = Buffer.from(Array.from({ length: 256 }, (_, value) => value));
  const { text, noted } = decodeNoting(everyByte, '');
  assert.equal([...text].length, 256);
  assert.deepEqual(noted, [LEGACY_FALLBACK]);
});

test('a page that declares a charset is never read by the fallback', () => {
  const noted = [];
  const note = (charset) => noted.push(charset);
  assert.throws(() => decodeBody(latin1('<p>café</p>'), 'text/html; charset=utf-8', note), /not valid utf-8/);
  assert.throws(() => decodeBody(latin1('<meta charset="utf-8"><p>café</p>'), 'text/html', note), /not valid utf-8/);
  assert.match(decodeBody(latin1('<meta charset="iso-8859-1"><p>café</p>'), 'text/html', note), /café/);
  assert.deepEqual(noted, []);
});

// ---- through the whole pass --------------------------------------------------------------

// Derives one page and returns what the run said and the Markdown file it wrote ('' if none).
async function deriveOne(t, { body, contentType }) {
  const directory = await scratchDirectory(t);
  const outDir = `${directory}/out`;
  const page = await listedPage(directory, { body, content_type: contentType });
  const result = await deriveInto(outDir, linesOf([page]));
  const files = await filesUnder(outDir);
  const markdownKey = [...files.keys()].find((key) => key.startsWith('markdown/'));
  return { result, markdown: markdownKey ? files.get(markdownKey).toString('utf8') : '', page };
}

const FIRST_SENTENCE = /Le café est prêt, et on le sert/;

test('a page served as windows-1252 has its 0xE9 byte read as é', async (t) => {
  const html = articleHtml({ title: 'Café', charsetMeta: '', paragraphs: FRENCH });
  assert.ok(latin1(html).includes(0xe9), 'the fixture holds the single byte 0xE9');

  const { result, markdown } = await deriveOne(t, { body: latin1(html), contentType: 'text/html; charset=windows-1252' });

  assert.equal(result.status, 0, result.stderr);
  assert.match(markdown, FIRST_SENTENCE);
  assert.doesNotMatch(markdown, /�|Ã/);
});

test('a page with no charset in its header and <meta charset="iso-8859-1"> in its head has its 0xE9 byte read as é', async (t) => {
  const html = articleHtml({ title: 'Café', charsetMeta: '<meta charset="iso-8859-1">', paragraphs: FRENCH });

  const { result, markdown } = await deriveOne(t, { body: latin1(html), contentType: 'text/html' });

  assert.equal(result.status, 0, result.stderr);
  assert.match(markdown, FIRST_SENTENCE);
  assert.match(markdown, /^title: "Café"$/m);
});

test("the header's charset beats the page's, and a page that declares nothing is UTF-8", async (t) => {
  const declaresLatin1 = articleHtml({ charsetMeta: '<meta charset="iso-8859-1">', paragraphs: FRENCH });
  const headerWins = await deriveOne(t, { body: utf8(declaresLatin1), contentType: 'text/html; charset=utf-8' });
  assert.equal(headerWins.result.status, 0, headerWins.result.stderr);
  assert.match(headerWins.markdown, FIRST_SENTENCE);
  assert.doesNotMatch(headerWins.markdown, /Ã/);

  const declaresNothing = articleHtml({ charsetMeta: '', paragraphs: FRENCH });
  const fallback = await deriveOne(t, { body: utf8(declaresNothing), contentType: 'text/html' });
  assert.equal(fallback.result.status, 0, fallback.result.stderr);
  assert.match(fallback.markdown, FIRST_SENTENCE);
  assert.equal(fallback.result.stderr, '', 'a page read as UTF-8 leaves nothing to say');
});

// U+0097 and U+0092 are written to a Latin-1 buffer as the bytes 0x97 and 0x92, which is
// how a page written in windows-1252 stores an em dash and a curly apostrophe.
const LEGACY_PARAGRAPHS = [
  'The gardener said one thing\u0097that a sharp blade is a kind one\u0097and then went back to the café for the morning.',
  ...FRENCH,
  'It\u0092s the habit that matters, she said, and nobody at the long table by the window disagreed with her.',
];

test('a page with no declared charset that is not valid UTF-8 is derived as windows-1252, and the run says so', async (t) => {
  const html = articleHtml({ charsetMeta: '', paragraphs: LEGACY_PARAGRAPHS });
  const body = latin1(html);
  for (const byte of [0xe9, 0x97, 0x92]) {
    assert.ok(body.includes(byte), `the fixture holds the byte 0x${byte.toString(16)}`);
  }

  const { result, markdown, page } = await deriveOne(t, { body, contentType: 'text/html' });

  assert.equal(result.status, 0, result.stderr);
  assert.match(markdown, /one thing—that a sharp blade is a kind one—and/);
  assert.match(markdown, /It’s the habit that matters/);
  assert.match(markdown, FIRST_SENTENCE);
  assert.doesNotMatch(markdown, /�|Ã/);
  assert.equal(result.stdout.trimEnd().split('\n').at(-1), '1 derived, 0 empty, 0 skipped, 0 failed');

  const said = result.stderr.split('\n').filter((line) => line !== '');
  assert.equal(said.length, 1, result.stderr);
  assert.ok(said[0].startsWith('derive: '), said[0]);
  assert.ok(said[0].includes(page.address), 'the line names the page');
  assert.ok(said[0].includes('windows-1252'), 'the line names the charset it was read in');
});

test('a page of every byte above 0x7F with no declared charset is derived, not failed', async (t) => {
  const highBytes = Buffer.from(Array.from({ length: 128 }, (_, index) => 0x80 + index));
  const html = articleHtml({ charsetMeta: '', paragraphs: FRENCH });
  const body = Buffer.concat([latin1(html.replace('</body>', '<p>')), highBytes, latin1('</p></body>')]);
  assert.ok(html.includes('</body>'), 'the fixture has a body to add a paragraph to');

  const { result, markdown } = await deriveOne(t, { body, contentType: '' });

  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stdout.trimEnd().split('\n').at(-1), '1 derived, 0 empty, 0 skipped, 0 failed');
  assert.match(markdown, FIRST_SENTENCE);
});

test('a <meta> that ends at byte 1024 declares the charset and one that ends at byte 1025 does not', async (t) => {
  const inside = await deriveOne(t, { body: utf8(articleWithCharsetTagEndingAtByte(1024)), contentType: 'text/html' });
  assert.equal(inside.result.status, 0, inside.result.stderr);
  assert.match(inside.markdown, /Le cafÃ© est prÃªt/, 'honoured: the UTF-8 bytes were read as windows-1252');

  const outside = await deriveOne(t, { body: utf8(articleWithCharsetTagEndingAtByte(1025)), contentType: 'text/html' });
  assert.equal(outside.result.status, 0, outside.result.stderr);
  assert.match(outside.markdown, FIRST_SENTENCE, 'ignored: the bytes were read as UTF-8');
});

test('a page whose charset is unknown, or whose bytes are not valid in it, is failed and leaves no file', async (t) => {
  const html = articleHtml({ charsetMeta: '', paragraphs: FRENCH });
  const cases = {
    'unknown charset': { body: utf8(html), contentType: 'text/html; charset=no-such-charset', complaint: /unsupported charset "no-such-charset"/ },
    'Latin-1 bytes under a header that says UTF-8': { body: latin1(html), contentType: 'text/html; charset=utf-8', complaint: /not valid utf-8/ },
    'Latin-1 bytes under a <meta> that says UTF-8': {
      body: latin1(articleHtml({ charsetMeta: '<meta charset="utf-8">', paragraphs: FRENCH })),
      contentType: 'text/html',
      complaint: /not valid utf-8/,
    },
  };
  for (const [name, { body, contentType, complaint }] of Object.entries(cases)) {
    const { result, markdown, page } = await deriveOne(t, { body, contentType });
    assert.equal(result.status, 1, name);
    assert.equal(markdown, '', `${name}: no file`);
    assert.ok(result.stderr.includes(page.address), `${name}: names the address`);
    assert.match(result.stderr, complaint, name);
    assert.equal(result.stdout.trimEnd().split('\n').at(-1), '0 derived, 0 empty, 0 skipped, 1 failed', name);
  }
});
