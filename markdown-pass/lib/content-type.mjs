// A stored Content-Type header reads "type/subtype; name=value; ...". This pass needs two
// things from it: whether the page is HTML at all, and the charset parameter if there is one.

const HTML_MEDIA_TYPES = new Set(['text/html', 'application/xhtml+xml']);

export function mediaTypeOf(contentType) {
  return contentType.split(';', 1)[0].trim().toLowerCase();
}

// A page stored with no Content-Type is taken for HTML. It was fetched because someone
// listed it as a page, and dropping it for a header the server left out would lose it.
export function isHtmlContentType(contentType) {
  const mediaType = mediaTypeOf(contentType);
  return mediaType === '' || HTML_MEDIA_TYPES.has(mediaType);
}

// The charset parameter as written, or null when the header has none (or leaves it empty).
export function charsetParameterOf(contentType) {
  for (const parameter of contentType.split(';').slice(1)) {
    const separator = parameter.indexOf('=');
    if (separator === -1 || parameter.slice(0, separator).trim().toLowerCase() !== 'charset') {
      continue;
    }
    const value = parameter.slice(separator + 1).trim().replace(/^"(.*)"$/s, '$1').trim();
    return value === '' ? null : value;
  }
  return null;
}
