import { charsetParameterOf } from './content-type.mjs';

// A body is bytes. Decoding them as UTF-8 whatever the page says would turn every accented
// letter of a Windows-1252 page into a replacement character, and nothing downstream could
// tell that it had happened. So the charset is settled first: the header's parameter, then
// what the page itself declares near its top. A page that declares nothing is UTF-8 when
// its bytes are valid UTF-8, and windows-1252 when they are not.

// What a browser in a Western locale falls back to for a page that names no charset, and
// what the sites that never named one were written in. Every byte value decodes in it, so
// the fallback itself cannot fail.
export const LEGACY_FALLBACK = 'windows-1252';

// How far into the body a browser looks for a <meta> declaration. A tag past it is not a
// declaration, which keeps a stray "<meta charset" in an article from changing the decoder.
const META_SCAN_BYTES = 1024;

// An unterminated comment runs to the end of what was scanned, as it does in a browser.
const COMMENT = /<!--[\s\S]*?(?:-->|$)/g;
// A tag cut off by the end of the scan has no ">" and so is not matched, as in a browser.
const META_TAG = /<meta(?=[\s/>])[^>]*>/gi;
// An unquoted value runs to the next space or ">", "=" and "/" included, as in a browser:
// content=text/html;charset=latin1 is one value.
const ATTRIBUTE = /([^\s"'<>/=]+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>][^\s>]*)))?/g;

// noteFallback is called with the charset's name when the bytes were read by the fallback,
// so that a guess is never made without a trace.
export function decodeBody(bytes, contentType, noteFallback = () => {}) {
  const declared = charsetParameterOf(contentType) ?? charsetDeclaredInMeta(bytes);
  if (declared !== null) {
    // A page that says what it is and is not: the declaration is wrong, and guessing over
    // a declaration is how text gets silently corrupted. decodeAs fails it.
    return decodeAs(declared, bytes);
  }
  try {
    return decodeAs('utf-8', bytes);
  } catch {
    noteFallback(LEGACY_FALLBACK);
    return decodeAs(LEGACY_FALLBACK, bytes);
  }
}

function decodeAs(label, bytes) {
  let decoder;
  try {
    decoder = new TextDecoder(label, { fatal: true });
  } catch {
    throw new Error(`cannot decode the body: unsupported charset "${label}"`);
  }
  try {
    return decoder.decode(bytes);
  } catch {
    // Failing is the point of "fatal": a replacement character written into a reading copy
    // is the silent corruption this decoding exists to prevent.
    throw new Error(`cannot decode the body: the bytes are not valid ${decoder.encoding}`);
  }
}

// The charset a <meta charset> or <meta http-equiv="Content-Type"> tag in the first 1024
// bytes declares, or null. The first tag that declares one wins.
export function charsetDeclaredInMeta(bytes) {
  // latin1 maps each byte to one character, which is all that is needed to find ASCII tags.
  const scanned = Buffer.from(bytes.buffer, bytes.byteOffset, Math.min(bytes.byteLength, META_SCAN_BYTES));
  const head = scanned.toString('latin1').replace(COMMENT, '');
  for (const [tag] of head.matchAll(META_TAG)) {
    const charset = charsetDeclaredBy(attributesOf(tag));
    if (charset !== null) {
      return charset;
    }
  }
  return null;
}

function attributesOf(tag) {
  // "<meta charset=utf-8/>": the slash closes the tag, it is not part of the value.
  const inside = tag.slice('<meta'.length).replace(/\/?>$/, '');
  const attributes = new Map();
  for (const [, name, doubleQuoted, singleQuoted, bare] of inside.matchAll(ATTRIBUTE)) {
    const key = name.toLowerCase();
    if (!attributes.has(key)) {
      attributes.set(key, doubleQuoted ?? singleQuoted ?? bare ?? '');
    }
  }
  return attributes;
}

function charsetDeclaredBy(attributes) {
  const direct = attributes.get('charset')?.trim();
  if (direct) {
    return direct;
  }
  if (attributes.get('http-equiv')?.trim().toLowerCase() !== 'content-type') {
    return null;
  }
  const declared = /charset\s*=\s*["']?\s*([^\s"';]+)/i.exec(attributes.get('content') ?? '');
  return declared ? declared[1] : null;
}
