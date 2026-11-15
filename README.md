# hoard

A personal web capture tool. It collects the raw HTML of a list of pages, then derives
Markdown from what it stored.

**Status: early.** The pipeline runs end to end against synthetic sites in its own tests.
It has not been run against a real site yet.

## What it is meant to do

1. **Discover** the addresses of a site: from its sitemap, by following links, or from a
   command that prints them.
2. **Fetch** the HTML of each address, several at a time, under one explicit rate for the run,
   and store every response exactly as it arrived.
3. **Verify** the result against a criterion stated for that site, and say what is missing.
4. **Derive**, as separate passes that can be run again at any time: Markdown, and the
   page's assets.

Fetching and deriving are deliberately apart. The stored response is the source of truth,
so a better cleaning pass tomorrow needs no second visit to the site.

## How a site is captured

A site is described once, in a recipe: a small TOML file that says where the list of
pages comes from, how fast the site may be asked, and what a complete capture looks like.

```toml
[site]
name = "example"
host = "example.com"

[discover]
kind = "sitemap"                  # or "links", or "command"
url = "https://example.com/sitemap.xml"

[fetch]
rate = 1.0                        # requests per second
cookie_file = "~/secrets/example.cookie"   # only for a site that needs a login

[verify]
min_pages = 100
min_body_bytes = 2000
forbidden = ["Subscribe to read"]
```

```sh
hoard discover --recipe example.toml --store ~/archives/example   # find the pages
hoard fetch    --recipe example.toml --store ~/archives/example   # fetch what is missing
hoard verify   --recipe example.toml --store ~/archives/example   # is the capture complete?
hoard status   --store ~/archives/example                         # where does it stand?

hoard list --store ~/archives/example --json \
  | node markdown-pass/derive.mjs --out ~/archives/example        # Markdown to read
```

Every command can be run again. A page that was fetched is not fetched twice, and a
second discovery adds only what is new.

The `[fetch]` table sets the pace of a capture. To try a site at another pace once, type
`--workers`, `--rate`, `--timeout` or `--user-agent` beside `--recipe`: a flag that is typed
wins over the recipe for that one run, and the recipe file stays as it is. A flag that is
not typed leaves the recipe's value. Each value that was replaced is said on standard
error, naming the flag, the value used and the recipe's, so a log read later does not take
that run for one at the recipe's pace.

After its summary line, `fetch` says how long the site took to answer:
`median response <n> ms over <m> pages`, over the pages that run stored. Each page is timed
from just before its last request was sent to the last byte of its body, so the wait for the
pace, a redirect's earlier hops, refusals and errors are not in it. A site that is asked too
fast usually slows down before it starts refusing, and this is the number to compare between
two runs at different paces. A run that stored nothing, such as a second one over a finished
capture, prints only its summary line.

### Keeping a page's images

A post full of screenshots is stored as text with addresses where the pictures were. To keep
the pictures too, give `assets` a file with one image address per line:

```sh
hoard assets --store ~/archives/example --list images.txt   # keep these images
hoard list   --store ~/archives/example --assets --json     # what is held
```

Each image is stored as it arrived under `assets/<xx>/<sha256><extension>`, once however
many addresses serve the same bytes, and recorded apart from the pages: `list --json`,
`verify` and the page counts of `status` never see it, and `status` reports the images on a
line of their own, `assets: <n> held, <n> failed, <n> pending`. The extension comes from the
answer's `Content-Type` (`.jpg` for JPEG), or for another image type from the address, and is
`.img` when neither says. An answer that is not an image, such as an error page served with
status 200, fails its address with the type in the reason. Images may live on any host, so
the command asks whichever host an address names, never sends a cookie, and takes the same
`--workers`, `--rate`, `--timeout` and `--user-agent` as `fetch`. Like `fetch --list`, it
creates the store when there is none. It prints `<n> listed, <n> held, <n> failed, <n> pending`
and exits 0 only when every address is held.

### Capturing part of a site

`discover` records every address the site lists. To take only part of it, say so when
discovering. The choice is made on the command line and not in the recipe, because a recipe
describes a site, and how much of it one capture takes is decided when capturing.

```sh
# the first 50 addresses under these sections, in the order the site lists them
hoard discover --recipe example.toml --store ~/archives/example --paths /essays/,/notes/ --limit 50
# the same call with a higher limit: only the next 50 are added
hoard discover --recipe example.toml --store ~/archives/example --paths /essays/,/notes/ --limit 100
# a sample of 200 addresses of the whole site, picked at random
hoard discover --recipe example.toml --store ~/archives/sample --limit 200 --random
```

`--paths` takes path prefixes separated by commas, each starting with `/`. An address is
chosen when its path starts with one of them, compared as text: `/essays/` chooses
`/essays/an-article` and not `/essays-archive/x`. An address also has to match the
recipe's `pattern`, when it has one.

`--limit` is the number of chosen addresses the store ends up holding, not the number
this call adds. The chosen addresses the store already has count toward it, in any state,
so asking again with the same words adds nothing, and raising the limit adds only the
difference. The additions are the first chosen addresses the store does not have yet, in
the order the site lists them; with `--random` they are picked among those instead, each
as likely as any other. `--random` needs `--limit`.

When the limit leaves chosen addresses out, the line that `discover` prints ends with how
many: `200 found, 50 new, 0 off-site, 150 beyond the limit`.

## What it is not

- Not a general crawler. Each site is described by its own small recipe.
- Not a hosted service, and not a way around a paywall. It fetches what the person running
  it is entitled to read.
- Recipes for real sites are not part of this repository.

## Development

```sh
bin/install-hooks   # once after cloning
bin/ci              # the same checks CI runs
```

Conventions for contributors and their agents are in [`AGENTS.md`](AGENTS.md).

## License

MIT. See [`LICENSE`](LICENSE).
