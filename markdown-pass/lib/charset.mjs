import { charsetParameterOf } from './content-type.mjs';

// A body is bytes. Decoding them as UTF-8 whatever the page says would turn every accented
// letter of a Windows-1252 page into a replacement character, and nothing downstream could
// tell that it had happened. So the charset is settled first: the header's parameter, then
// what the page itself declares near its top, then UTF-8.

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

export function decodeBody(bytes, contentType) {
  const label = charsetParameterOf(contentType) ?? charsetDeclaredInMeta(bytes) ?? 'utf-8';
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
