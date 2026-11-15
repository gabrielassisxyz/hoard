# hoard — Agent Briefing

> Read before every interaction. Living spec: short, imperative. On every gotcha or
> decision, append one line here.

> **What it is:** a personal web capture tool. It collects the raw HTML of a list of
> pages, then derives clean HTML and Markdown from what it stored.
> **Calibration:** Tier 1 · Phase: work. No users and nothing deployed yet; the goal is a
> first end-to-end capture that works. Raise to Tier 2 once that capture works and
> features start being added.
> **Review gate:** light. Self-review the diff in a fresh context before pushing.

## Stack & Commands

- **Stack:** Go for the collector, built as one binary with `CGO_ENABLED=0`. Node for the
  offline pass that cleans stored HTML and converts it to Markdown with `defuddle`: it
  lives in `markdown-pass/`, needs Node 22.15 or later (zstd comes with `node:zlib`), and
  has exactly two dependencies, pinned without a range.
- **Gate:** `bin/ci` — the exact thing CI runs.
- **After clone:** `bin/install-hooks` once (gitleaks pre-commit, attribution commit-msg).
- **New work:** `bin/worktree new <type>/<kebab-description>`, never in the main tree.
- **Build:** `CGO_ENABLED=0 go build -o hoard ./cmd/hoard` writes the binary at the
  repository root, where `.gitignore` hides it.
- **Test:** `go test ./...`, or `go test ./internal/fetch/` for one package. `bin/ci` runs
  the same Go steps (gofmt, vet, test, build) because `go.mod` exists. For the Node pass,
  `npm ci` once and then `npm test` inside `markdown-pass/`; `bin/ci` runs both after the
  Go steps. The Node tests need Go on the machine, because the end-to-end one runs the
  collector through `go run ./cmd/hoard`.

## Scope (current)

- **Current scope:** one pipeline exists, from a site's recipe to reading copies of its
  pages, and it has only ever run against synthetic sites in its own tests.
  `hoard discover` records a site's addresses from its sitemap, an index page or a
  command. `hoard fetch` exists in two forms: `--list` for a plain file of addresses, and
  `--recipe` for what was discovered, at the recipe's pace and signed in with its cookie.
  Either way it fetches under a concurrency ceiling and an explicit rate per host and
  stores every 2xx body raw in a store directory (`ledger.db` and
  `raw/<xx>/<sha256>.zst`). `hoard verify` judges the capture by the recipe's criterion,
  `hoard status` says where it stands, `hoard list --json` prints what the store holds,
  and `markdown-pass/derive.mjs` turns those lines into clean HTML and Markdown. Not built
  yet: `robots.txt`, assets, skipping pages already derived, a browser for pages that only
  exist after JavaScript runs. The next target is the first recipe for a real site, kept
  outside this repository; don't expand beyond that without a present need, and if a
  change drifts past it, STOP and flag it.

<!-- BEGIN universal-principles v3 -->
## Working principles

- **The human defines the WHAT; the agent decides the HOW.** Plan first for non-trivial
  tasks: show the plan and the to-do list, and wait for approval before building.
- **State assumptions; never pick between readings silently.** If several fit, present
  them. If a simpler approach exists, push back. If the task is impossible as stated, or
  information is missing, say so instead of guessing. This is a bias, not a ritual.
- **Surgical changes — touch only what you must.** Every changed line traces to the task.
  Don't "improve" adjacent code, reformat, or refactor what isn't broken; match existing style
  even if you'd do it differently. Flag unrelated dead code — don't delete it. Remove only the
  imports / variables / functions your own change orphaned.
- **Chesterton's Fence — find the problem before undoing the decision.** A config, a flag, a
  workaround that looks arbitrary is a **fence**: someone put it there, probably to fix
  something that is invisible to you *because the fence is working*. You arrive with no
  history, so absence of a visible reason is evidence of your ignorance, not of its
  uselessness. When your fresh measurement contradicts what the human vaguely remembers
  ("I changed this once, because of some problem"), **your measurement is the suspect first**
  — it may be measuring the case that *isn't* failing. Go find the original problem, then
  decide. *(A CIFS share was benchmarked with a big sequential `dd`, looked fast, and the
  local-disk download dir was "fixed" away — while the actual failure was random writes:
  par2, unrar, torrent piece-writes. Two wrong commits.)*
- **"Flaky" is not a diagnosis — test in the environment the thing actually runs in.** A
  component that fails *consistently* under automation is being **mis-invoked**, not being
  unreliable; "it works when I run it by hand" is not evidence that it works. The shell you
  test in has a TTY, a `$HOME`, an `ssh-agent`, an interactive stdin — the systemd unit, the
  CI job and the scripted harness have none of those, so a passing manual run can be testing
  a different program. Reproduce it *there* (start the unit, `env -u SSH_AUTH_SOCK`,
  `</dev/null`, `--dry-run` to print the real command line) before accepting "unstable" as a
  cause. **When a fix doesn't change the symptom, stop fixing and go look at what is actually
  being executed.** *(An interactive-mode flag with no TTY made one harness fail every review
  panel for weeks, written off as "flaky"; it was the wrong flag.)*
- **KISS and YAGNI — don't build for a need that doesn't exist.** No preventive
  abstractions, no single-use interfaces. Architecture is *extracted* once a pattern proves
  itself in real use, never designed up front. Most over-engineering is doing "right" or
  "fast" before a working thing justifies it. When a solo tool starts being framed as a
  public / multi-tenant / plugin-system / configurable-N-backends platform with no present
  need, STOP and ask — that is a standing duty, not a suggestion.
- **Development cost is not your cost — don't let it pick the design.** Choosing between
  technical options, weight quality, simplicity, robustness and long-term maintainability;
  don't weight how long the work takes. The estimate comes out in human units — days,
  weeks — because that is what the training data measured, and the cheaper option then
  wins on a cost the agent does not pay. This is **not** licence to over-build: KISS and
  YAGNI decide *whether* a thing is needed, and this decides *how well* it is built once
  it is. "That would take a week" is not an argument here; "nothing needs this yet" is.
- **No silent decisions (comprehension debt).** Never make a silent architectural or
  design call — state it and record the rationale, so the reasoning is recoverable later.
- **Real decisions go in the chat, on their own — never via a popup.** Surface the options,
  what each means, the trade-offs, and a recommendation; then decide together. Don't bury
  it mid-text or bundle it with other topics, and don't compress it into a quick-pick widget
  (e.g. AskUserQuestion) — the widget overlays the reasoning instead of showing it. Widgets
  are for trivial short-answer picks only.

## Git: branches, commits, PRs, comments

- **Ask the repo for its default branch; never assume one.** Repos differ — `master` and `main`
  are both common, often in the same person's account — and a wrong guess sends a PR to a branch
  that does not exist, or, worse, has you "fixing" a URL that was right all along.
  `git symbolic-ref --short refs/remotes/origin/HEAD | sed 's|^origin/||'`, or
  `gh repo view --json defaultBranchRef -q .defaultBranchRef.name`.
  Never commit directly to it: branch, then PR.
- **A new repo starts on `main`.** That is the preferred name, and `init.defaultBranch` is
  set to it, so `git init` produces it without anyone choosing. It settles new repos only:
  an existing one keeps the branch it has, because renaming breaks open PRs, CI filters,
  deploy hooks and every permalink into the tree, and buys nothing. The rule above still
  governs everything already in existence — ask, never assume.
- **Branches** — Conventional Branch (conventionalbranch.org): `<type>/<kebab-description>`,
  types `feature/`, `bugfix/`, `hotfix/`, `chore/`, `release/`, `docs/`.
- **Commits** — Conventional Commits (conventionalcommits.org): `<type>(scope): <description>`,
  types `feat`, `fix`, `docs`, `refactor`, `test`, `chore`, `ci`, `build`, `perf`, `style`.
  Breaking change → `!` after the type or a `BREAKING CHANGE:` footer.
- **Atomic commits** — one logical change per commit, each independently green and
  revertible. Never `git add .` blind; split unrelated changes.
- **Always work in your own worktree — mandatory, not conditional.** Parallel sessions
  are opened freely and nothing signals their existence to you, so a "check whether another
  session is here first" step can never be reliable — the honest answer is always "maybe".
  The only collision-proof arrangement is structural: keep the main working tree on the
  default branch as a clean reference and **never work in it** — before your first write
  (commit, branch, rebase, stash; read-only exploration is exempt), create your own worktree
  and do everything there. **When the repo ships a worktree tool, that tool is the only correct
  way to make one** — `bin/worktree new <type>/<kebab-desc>`, or whatever the repo calls it.
  A raw `git worktree add` materialises tracked files and nothing else, so every git-ignored
  path the repo links into a worktree is silently absent, and the tools that read those paths
  fail inside it without saying why: a tracker whose database is git-ignored reports itself
  uninitialised and offers to create a second one. Only where the repo ships no such tool is
  `git worktree add ../<repo>-<task> -b <your-branch> <origin>/<default-branch>` the right
  command. Do this **whether or not** you believe another agent is running — that belief is
  exactly what you cannot verify. Report which worktree/branch you used; remove it once merged.
  Only the human can see all the open sessions.
- **Pull requests** — describe **what + why**. *What*: a 1–3 line summary. *Why* (the bulk):
  decisions, trade-offs, rejected alternatives. The diff shows the what; the PR explains why.
- **Comments** — always **WHY, not WHAT**: explain intent, never restate the obvious
  mechanics. Keep existing comments; they carry intent.

## Code style (baseline)

- Functions: 4–40 lines, one thing each (SRP). Files: under ~500 lines, split by responsibility.
- Names specific and unique — avoid `data`, `handler`, `Manager`, `util`.
- Explicit types. Early returns over nested ifs; max ~2 levels of indentation.
- Inject dependencies; wrap third-party libs behind a thin interface this project owns.
- No duplication — but don't extract *too early*. Tolerate duplication while the pattern is
  still forming; extract the abstraction *from* proven, repeated code, never ahead of it.
- **Refactoring is not automatic.** After a large feature, list the candidates and ask
  before pruning; consolidate once the thing works and the seams are obvious, not before.
<!-- END universal-principles v3 -->

## How the pipeline is cut

- **Raw first.** The stored response is the source of truth. Clean HTML and Markdown are
  derived from it and can always be produced again.
- **Fetching and deriving are separate passes.** The collector never waits for cleaning,
  Markdown or assets, and those passes never touch the network.
- **A site is described by a recipe, and a recipe is data.** A recipe file holds values
  only: where the list of addresses comes from, the rate, the credential's location, the
  success criterion. A site that needs logic to produce its list supplies a separate
  command that prints the addresses. An unknown key in a recipe is an error, and so is a
  key that belongs to another way of finding pages; one load reports every problem.
- **A recipe collects one site.** A discovered address on another host is left out and
  counted. What counts as one site is decided in one place, `internal/hostkey`.
- **A discovery gives the whole list or fails.** A sitemap index is read exactly one level
  deep. When any document cannot be read nothing is recorded, because a partial list looks
  complete to everything that reads the ledger afterwards.
- **The session cookie is decided for every request.** It goes to the recipe's own host,
  over https or to this machine itself, and nowhere else: not to the host a redirect leads
  to, not over plain http. No client here keeps a cookie jar or follows a redirect by
  itself, so `hostkey.SessionMayGoTo` is the only thing that puts a cookie on a request.
- **A cookie file is a secret on disk.** One readable by group or others is refused, and
  no message quotes its content.
- **A derived file is named after its address, never after its body.** The name is the
  SHA-256 of the address as requested. Two addresses can share one body, and what the
  extractor returns depends on the address it is given as the base for links.
- **The derive pass makes no request.** It runs `defuddle` with `useAsync: false`, the
  option that stops the library's own fetches, and a test counts requests to prove it.
- **A body is bytes until its charset is settled.** The header's parameter, then a
  `<meta>` declaration in the first 1024 bytes. A page that declares nothing is read as
  UTF-8 when its bytes are valid UTF-8 and as windows-1252 when they are not, which is
  what a browser does with it, and the run names every page it read that way. Bytes that
  are not valid in a charset the page did declare fail the page, by name, instead of
  being repaired into a reading copy that is quietly wrong.
- **A command that only reads does not create a store.** `list`, `status`, `verify` and
  `fetch --recipe` refuse a directory with no ledger; only `discover` and `fetch --list`
  create one.
- **The criterion is the recipe's three values and nothing else.** Failed and pending
  addresses are reported by `verify` and do not fail it; whether everything asked for was
  obtained is `fetch`'s exit code.
- **A run says what it did not get.** Expected against obtained is reported, and a run
  that took less than it was asked for does not exit 0.
- **The body is written before its row.** A body file goes in by temporary file and rename;
  only then does its ledger row become `ok`. A crash may leave a body with no row, never a
  row naming a body that is not there.
- **A settled address is not asked again.** `ok` and `failed` rows keep their state across
  runs; only `pending` ones are fetched. A network error or a 5xx counts toward 3 tries per
  address per run, and the third makes it `failed`.
- **A refusal never fails an address.** A 429 or 403 means "later", so the address stays
  `pending` and spends none of its tries. After 5 refusals in a row from one host the run
  stops asking that host, leaves what it owed `pending`, and ends short of its list.
- **A host is its host and port**, lower-cased, without the scheme's default port. Each has
  its own limiter and its own pause after a 429 or 403. Every hop of a redirect is a request
  of its own, paced and paused by the host it leads to.

## What never enters this repository

- Recipes for real sites, cookies and other credentials, captured pages, and HTML taken
  from a real site as a test fixture. Tests use synthetic pages served by a local server.
  A test that needs a host name uses one under `.test` or `.invalid`, never a real one.

## Git & secrets

- Before any commit, show `git status` + `git diff --cached`; confirm no secret is staged.
  If you spot one, STOP and report it. The gitleaks pre-commit hook is the deterministic
  backstop; this habit is the probabilistic one.
- Real secrets stay out of git — only `.env.example` with fake values is committed.
- Default branch is `main`. Work reaches it through a pull request.
- A pull request is merged by the agent that opened it, once `bin/ci` and the pull
  request's own checks are green. Nobody waits for a person to press merge: the pull
  request exists for its description and its CI run, not as a review queue.

## Common hurdles

| hurdle | class | gate |
|---|---|---|
| `bin/worktree` falls back to `master` when `origin/HEAD` is unset, which a fresh clone can lack; run `git remote set-head origin -a` and check the banner says `main` | tripwire | none yet |
| `node --test` runs every file under a directory named `test`, so a shared helper put there is run as a test; helpers live in `markdown-pass/test-support/` | tripwire | none yet |
| A test whose mutation survives is the suspect before the code is: a fragment check ran under a pattern that already excluded fragments, and a failing command printed an off-site address, so both went red for a reason other than the rule they named | tripwire | none yet |
| A test that asserts an upper bound on elapsed time fails on a loaded CI runner, because `go test` compiles the pure-Go SQLite beside it and can starve a test for hundreds of milliseconds; assert lower bounds, or read the property off the code (a limiter's burst, which host owns which queue) | tripwire | none yet |

**A hurdle promoted to a gate is deleted from this table, not duplicated.** The gate is the
instruction; a line here restating it only dilutes the ones still unguarded.

