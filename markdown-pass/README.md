# markdown-pass

The offline pass of hoard: it turns the pages a capture holds into Markdown.
It reads what `hoard list` prints, never touches the network, and can be run again whenever
the cleaning improves, because the stored response is the source of truth and this only
derives from it.

```sh
hoard list --store <store> --json | node markdown-pass/derive.mjs --out <out>
```

It needs Node 22.15 or later, and one `npm ci` in this directory. The extraction is the
original [defuddle](https://github.com/kepano/defuddle) over [linkedom](https://github.com/WebReflection/linkedom),
both pinned to the versions the extractors were measured with.

## What it writes

For every line whose content type is HTML (`text/html`, `application/xhtml+xml`, or none):

- `<out>/markdown/<xx>/<name>.md`, the page as Markdown, opening with a block of
  `key: <JSON value>` lines between two `---` lines: `address`, `final_address`, `title`,
  `author`, `published`, `site`, `fetched_at`, `body_sha256` and `word_count`. A value the
  extractor did not find is `null`.
- With `--clean-html`, also `<out>/clean/<xx>/<name>.html`: the cleaned HTML the Markdown
  was made from. It is left out by default because nothing reads it and it can be made
  again from the stored page at any time.

`<name>` is the SHA-256 of the page's address as requested, in lowercase hex, and `<xx>` its
first two digits. The files are named after the address and not the body because two
addresses can share one body, and what the extractor makes of a page depends on the address
it is given as the base for links. That address is `final_address`, where the page was
actually served from.

Every file is written under a temporary name and renamed into place, and two runs over the
same input leave byte-identical files.

## Reading the bytes

A body is bytes, not text. They are decoded with the charset of the `Content-Type` header,
else the one a `<meta charset>` or `<meta http-equiv="Content-Type">` tag declares in the
first 1024 bytes. A page that declares neither is read as UTF-8 when its bytes are valid
UTF-8 and as windows-1252 when they are not, which is what a browser does with it, and one
line on standard error names each page read that way. Bytes that are not valid in a charset
the page did declare fail the page instead of being repaired with replacement characters,
which would put damaged text in a reading copy without saying so.

## What a run says

The last line on standard output is `<n> derived, <n> empty, <n> skipped, <n> failed`. Every
input line is counted once, in one of the four:

- derived: the page's file, or files, written.
- empty: written all the same, but the Markdown is blank after trimming.
- skipped: the content type is not HTML, so there is nothing to derive.
- failed: the line is not a page, or the body could not be read, decoded or extracted. One
  line on standard error names the page's address, or the line's number, and what went wrong;
  the rest of the list is still done.

The exit status is 0 when nothing failed, 1 when anything did, and 2 when the command line
could not be read.

## Tests

```sh
npm ci
npm test
```

The tests use synthetic pages only. The end-to-end test runs the real chain, `hoard fetch`,
`hoard list --json` and this program, against a web server it starts on a local port, so it
needs Go to be installed.
