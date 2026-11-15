import assert from 'node:assert/strict';
import { test } from 'node:test';
import { charsetParameterOf, isHtmlContentType } from '../lib/content-type.mjs';

test('a content type is HTML by its media type alone, whatever its parameters or case', () => {
  const html = [
    'text/html',
    'text/html; charset=utf-8',
    'TEXT/HTML;CHARSET=UTF-8',
    '  text/html ; charset=windows-1252',
    'application/xhtml+xml',
    'Application/XHTML+XML; charset=utf-8',
    '',
    '   ',
  ];
  for (const contentType of html) {
    assert.equal(isHtmlContentType(contentType), true, `${JSON.stringify(contentType)} is HTML`);
  }
});

test('anything else is not HTML, including types that only start like it', () => {
  const notHtml = [
    'text/plain',
    'application/json',
    'application/pdf',
    'image/png',
    'application/xml',
    'text/xml',
    'text/html-sandboxed',
    'text/htmlx',
    'xtext/html',
    'text/css; charset=utf-8',
  ];
  for (const contentType of notHtml) {
    assert.equal(isHtmlContentType(contentType), false, `${JSON.stringify(contentType)} is not HTML`);
  }
});

test('the charset parameter is found wherever it sits among the parameters', () => {
  assert.equal(charsetParameterOf('text/html; charset=windows-1252'), 'windows-1252');
  assert.equal(charsetParameterOf('text/html;charset=UTF-8'), 'UTF-8');
  assert.equal(charsetParameterOf('text/html; Charset="iso-8859-1"'), 'iso-8859-1');
  assert.equal(charsetParameterOf('text/html; boundary=x; charset = latin1 ; foo=bar'), 'latin1');
});

test('a header with no charset, or an empty one, has none', () => {
  assert.equal(charsetParameterOf('text/html'), null);
  assert.equal(charsetParameterOf(''), null);
  assert.equal(charsetParameterOf('text/html; charset='), null);
  assert.equal(charsetParameterOf('text/html; charset=""'), null);
  assert.equal(charsetParameterOf('text/html; notcharset=utf-8'), null);
});
