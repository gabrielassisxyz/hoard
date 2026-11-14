# hoard

A personal web capture tool. It collects the raw HTML of a list of pages, then derives
clean HTML and Markdown from what it stored.

**Status: nothing works yet.** The repository holds its conventions and gates; the first
capture pipeline is being built.

## What it is meant to do

1. **Discover** the addresses of a site: from its sitemap, by following links, or from a
   command that prints them.
2. **Fetch** the HTML of each address, several at a time, under an explicit rate per host,
   and store every response exactly as it arrived.
3. **Verify** the result against a criterion stated for that site, and say what is missing.
4. **Derive**, as separate passes that can be run again at any time: clean HTML, Markdown,
   and the page's assets.

Fetching and deriving are deliberately apart. The stored response is the source of truth,
so a better cleaning pass tomorrow needs no second visit to the site.

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
