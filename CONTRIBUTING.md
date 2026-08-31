# Contributing to Olivares AI

Thank you for your interest. Olivares AI is in **beta**: the architecture, data model, SDK, and API are still stabilizing. Please open an issue to discuss any non-trivial change before sending a pull request.

## Development setup

The fastest path is the **dev container** ([`.devcontainer/`](.devcontainer/)): open the
repo in a container-capable editor (VS Code "Reopen in Container", or `devcontainer up`)
and it provisions the pinned Go (`.devcontainer/devcontainer.json`), Node 24, the GitHub CLI,
the pinned dev tools and the git
hooks for you (`task tools && task setup`). Skip to [the gate](#the-gate) once it finishes.

To set up locally instead, install:

- **Go 1.26+** — the engine and the single static binary.
- **Task (go-task)** — the task runner: `go install github.com/go-task/task/v3/cmd/task@v3.51.1`.
- **pnpm** — for the web UI under `web/` (React + TypeScript + Vite + Tailwind). Optional;
  CI installs it, and `task setup` never blocks on it.

```sh
task setup    # install git hooks (core.hooksPath) + commit tooling + web deps
task tools    # install the pinned Go dev tools (golangci-lint, govulncheck, gitleaks)
task build    # compile the olivares binary (web + first-party connectors embedded)

./bin/olivares version
```

`task setup` installs the `commit-msg` hook, so your commit messages are validated
locally exactly as in CI. Run `task` with no arguments to list every available task.

### The gate

Before opening a pull request, run the **green gate** — the same subset
[`mainline-ci`](.github/workflows/mainline-ci.yml) enforces:

```sh
task lint:spdx lint:boundary   # SPDX headers + the Apache/AGPL license boundary
task build:go                  # compile every workspace module
task test                      # the race-enabled Go test suite
```

> ### ⛔ Touched anything under `web/`? Run `task build:web` and COMMIT `core/internal/webui/dist`.
>
> The console bundle is `go:embed`-ed into the binary, so a stale `dist` means a release build ships
> a console that does not match the sources every gate measured. CI catches it — `web:check` fails
> with *«the committed console bundle is STALE vs web/ sources»*, and `web` is a **required**
> context, so it blocks — but it catches it **after** your change has already reached `main`.
>
> **This paragraph exists because the duty was written NOWHERE a contributor reads.** Measured
> 2026-08-17: no contributor-facing document mentioned `task build:web` at all — the single mention
> in the whole tree was inside the push hook. The consequence was not theoretical: the bundle went
> stale **three times in one day**, and each time it had to be rebuilt on someone else's behalf. A
> duty that only exists in a tool's error message is a duty nobody can perform in advance.

**What the `pre-push` hook runs depends on where your commits land** (GATES v3, since
2026-08-02). It classifies the push by its *remote ref* — the rule is
[`scripts/prepush-refclass.sh`](scripts/prepush-refclass.sh), proved by
`task lint:prepush-refclass` without executing any gate:

- `refs/heads/main` and `refs/tags/*` — the fast lints **and** the full gate
  (`test:license-worker`, `build:cloud`, `test:cloud:norace`, `check:web`, `tokens:check`, `lint:format-ratchet`, `lint:guide-docs`, `lint:raw-palette`, `test:web`, `web:check`, `build:go`, `test`, `sdk:check`),
  under the same host-wide mutex this repository already had. The split changed *who* takes
  that lock — every lane on the box used to; now only pushes classified `full` do: main,
  tags, and any deny-closed promotion (unknown namespace, malformed line) — and nothing
  about the lock itself; its known weaknesses are listed by name in the hook's header.
- Any other `refs/heads/*` — the fast lints only. **The three commands above are yours
  to run**, or the integrator's on the batch.
- A deletion, `refs/gate-locks/*`, `refs/integration-claims/*`, an empty push — nothing to gate.
  The claim ref is a POINTER an integrator publishes before pushing a batch; it used to score
  `full` as an unrecognised namespace, which made obeying that protocol cost a full gate and taught
  people to reach for `--no-verify`. Strictest-wins still applies: a claim travelling with
  `main` pays the full gate.
- Anything it cannot parse, or a namespace it does not recognise — the full gate. Not
  knowing what something is costs more than a feature branch, never less.

So on a feature branch the hook does **not** compile or test your work: run `task test` (and
`check:web` / `web:check` / `sdk:check`) yourself, or scope `-race` to the packages your diff
touches and say so in the PR. The sanctioned bypass is `git push --no-verify`, declared in
the PR.

**Without `task` on `PATH` the hook refuses the push** — a gate that cannot run has not
cleared anything. There is exactly **one named exception, the pure-deletion push**: when
*every* ref line is a deletion (`git push --delete old-branch`, `git push origin :old`)
there are no commits to lint, build or test in any lane, so it needs no toolchain to run
nothing and the hook lets it through, saying which exception it applied. A deletion
travelling with anything else — another ref, the gate-lock ref, a line the rule cannot
read — is not that class and still refuses.

> `task lint` (the *full* lint, including `golangci-lint`) is **not** part of the gate yet, and
> the reason is **the code, not the toolchain**. Do not read its red as expected noise, and do not
> try to close it in one sitting: the volume is a campaign of its own.
>
> **Two things about the numbers, because both have already misled someone here.**
>
> 1. **`golangci-lint` truncates by default.** `max-issues-per-linter: 50` and `max-same-issues: 3`
>    are on unless you turn them off, and a count taken with them is a CEILING, not a total. This
>    file used to quote one such ceiling as the total; with the caps at `0` the real figure was
>    almost three times larger. Both caps are now `0` in `.golangci.yml`, so what you see is what
>    there is. The cost of the old reading was not the number: a campaign planned against a
>    truncated count never converges, because the cap keeps releasing what it was hiding as you fix
>    things, and nothing says so.
> 2. **The largest linter by volume is `misspell`, and it contains no typographical errors.** It is
>    almost entirely British spelling measured against the `locale: US` this repository fixes, plus
>    a tail of genuine false positives. That makes most of it a mechanical sweep rather than a set
>    of defects — with one exception that needs judgement, the findings that sit on
>    **identifiers**, where a rename is an API change and not a spelling fix.
>
> **Run the census instead of copying a figure from prose:**
>
> ```sh
> bash scripts/misspell-census.sh   # per-category counts, derived from the tree, and it FAILS
>                                   # if its own rows stop summing to its total
> ```
>
> A number written into a paragraph goes stale silently; that is exactly how this section came to
> carry two different totals for the same fact. CI gates on the structural lints, plus
> `govulncheck` and a full-history `gitleaks` scan. `task fmt` formats Go and web.

### PostgreSQL for tests

The Go suite has a **server-gated PostgreSQL leg**: without the DSNs below it skips
locally (and **fails** in CI, where the fixture is promised — a skipped regression is
not evidence). A run without PostgreSQL configured is a **PARTIAL** run, not a green
one; several past defects were only visible with the leg on.

```sh
export OLIVARES_TEST_POSTGRES_SUPERUSER_DSN='postgres://postgres:<superuser-pw>@127.0.0.1:5432/postgres?sslmode=disable'
export OLIVARES_TEST_POSTGRES_DSN='postgres://olivares_app:<app-pw>@127.0.0.1:5432/olivares?sslmode=disable'
# The DSN's DATABASE matters: point it at the dedicated app-owned database
# (here `olivares`), NEVER at the shared `postgres` — suites that consume the
# DSN directly (the topology matrix) run migrations in that database, and the
# app role has no CREATE on the shared one (SQLSTATE 42501 at boot).
```

- `…_SUPERUSER_DSN` is the maintenance DSN: `pgtest` (core) and the module fixtures
  provision **one isolated database per test** from it and drop it afterwards. Tests
  never share a database; never point it at a database you care about.
- `…_DSN` names the **application role** (`olivares_app`) and its password. That exact
  role name is compiled into the schema's append-only `REVOKE`, so the fixtures refuse
  to run as anyone else — a per-test role would leave the ACL targeting nobody while
  every assertion still passed.
- The optional split roles (`OLIVARES_TEST_POSTGRES_OWNER_DSN`,
  `OLIVARES_TEST_POSTGRES_ADMIN_DSN`) enable the owner/app and admin topologies where a
  test declares them; `pgtest` derives the split itself from the superuser DSN.
- **Never run two suites concurrently against one server.** The leader-election
  advisory lock is cluster-wide and the provisioning path serializes on one too;
  concurrent suites (or any heavy process storm — a second gate, a container hitting
  `pids.max`) turn into fork failures and handshake garbage that read like product
  bugs. One suite at a time.

**Supported PostgreSQL: 15–18.** The floor advances with upstream EOL. Verified by
execution on 15.x (local) and 16.x (CI service); 17/18 were supported by contract
until the topology matrix grows execution legs for them (tracked as residual R5)
> *(Updated 2026-08-15: 17/18 DO have an execution leg now — `.github/workflows/pg-majors.yml`
> runs all four majors against real service containers on a schedule. It is deliberately NOT on
> `pull_request` and not a required check, so PR CI still does not cover them. R5 still owns the
> managed-backup client strategy — the pinned pg_dump 16 client cannot dump 17/18 servers — so the
> honest state there remains "contract + DR pending". "today" was replaced by a date on purpose:
> that word is what let this sentence rot silently.)* — and
note R5 also owns the managed-backup client strategy: the Helm/Operator defaults pin a
pg_dump 16 client, which upstream defines as unable to dump 17/18 servers, so until R5
lands the honest 17/18 state is "contract + DR pending", never plain "supported".
Pre-release majors (beta/RC) are rejected at boot by default.
### Adding a documentation page

The docs site ships English plus six locales (`es`, `zh`, `ru`, `ja`, `de`, `fr`). **A new
English page under `docs-site/src/content/docs/` opens a gap in all six**, and neither
`lint:i18n` (console UI keys) nor `lint:i18n-anchors` (in-page anchors) can see a missing
page — that is how 44 pages once sat untranslated behind a green gate.

```sh
task lint:docs-parity   # lists every English page with no translation, per locale
```

It runs in **informed mode** (`--informed --summary`, `Taskfile.yml:1575`) and **it can fail your
push**: a page missing in EVERY locale is the English-only backlog — reported, not blocking — while
a page missing in SOME locales, an orphan, a route collision, a missing locale directory or any
waiver defect exits 1 (`scripts/check-docs-parity.mjs:651`, called from `.githooks/pre-push:606`). Either translate the page, or declare the exemption
explicitly in `docs-site/i18n-parity-waivers.json` with an explicit locale list (`"*"` is
rejected), a real `reason` of at least 20 characters, and a real `date` (an `expires` is
preferred). There is no silent exemption, and a waiver that stops suppressing anything
becomes a finding itself.

> *(Corrected 2026-08-15. This paragraph was written on 2026-07-29 in `4aec142dd`, when the task
> did pass `--report` and the text was true. Since 2026-08-01 it passes NEITHER flag: `--report`
> was examined and **deliberately rejected** — a gate that cannot fail is not a gate — so there is
> no `--report` left to change. Run `node scripts/check-docs-parity.mjs --strict` yourself if you
> want the stricter verdict; do not quote a page count from this file, measure it.)*

The locale list lives in `docs-site/src/site-locales.mjs`, which `astro.config.mjs` and this
gate both import. Adding a locale there is the only change needed: the site and the parity gate
pick it up.

**Architecture decisions are recorded, and the register is internal.** The project keeps its
architecture decisions in MADR form in the development repository; that register is not part of
this tree and is not published on the documentation site. Until 2026-08-25 the site carried 229
generated pages for it — the project owner withdrew the whole section that day, because
architecture decision records are internal development documentation and publishing them can
compromise the integrity and security of the project and its paid business/enterprise part.

**So there is nothing here to edit and nothing to republish**: the generator and its sync gate
were removed with the pages. To propose an architecture decision, open an issue as described at
the top of this guide and it is taken up there. A gate holds the line in the other direction:

```sh
task lint:adr-not-published   # fails if a page, an ADR-shaped filename, a live link to the
                              # withdrawn route, or the generator comes back to docs-site
```

To exercise the product end-to-end against the real binary:

```sh
task smoke:quickstart   # the install→value path + the R/RW drift hero
task smoke:examples     # every examples/<scenario> against the current binary
```

The [`examples/`](examples/) directory is the best place to see real, copy-paste usage —
governing Claude Code tool-calls, OpenTelemetry GenAI ingest, and scaffolding a connector.

> **Dev license key.** A normal `go build` carries a **public, non-secret dev signing key**
> (`core/license/embedded_dev.go`), so `olivares license sign`/`verify` and the demos work
> out of the box — license verification is attestation-only and gates nothing. Release
> artifacts are built `-tags release`, which **drops the dev seed** and embeds the real
> Olivares key (`embedded_release.go`); there, `license sign` requires `--key`. `task
> test:release` / `task build:release` exercise that path (the default gate does not compile
> it). See `docs/RELEASE-VERIFICATION.md` and [`LICENSING.md`](LICENSING.md).

## Branching and commits

- **External contributors** open a pull request from a branch or fork — do not push to `main` directly — so the CLA/DCO and review flow below applies. `mainline-ci` runs the green gate on every pull request and push to `main`, and it must be green to merge.

  > **Correction 2026-08-15.** `mainline-ci` is `on: workflow_dispatch` and nothing else
  > (`.github/workflows/mainline-ci.yml:94-95`). It does **not** start on a pull request or on a
  > push to `main` — the integrator dispatches it per branch, by hand. Every required check on
  > `main` is a job of that workflow, so a pull request has no verdict at all until somebody
  > dispatches a run for its branch; with `strict` also on, a `main` that moves invalidates the
  > run and it must be dispatched again. The sentence above is left in place: it is what this
  > guide promised, and this is the record of it being false.
- **Conventional Commits**, in English: `feat:`, `fix:`, `refactor:`, `docs:`, `test:`, `chore:`, etc. Commit messages are linted by the `commit-msg` hook locally and by CI.
- Keep pull requests focused; describe what changed and why, and link the issue.
- **Write commit bodies with `git commit -F -` and a QUOTED heredoc, never `-m`.** A body containing
  backticks, `$`, or quotes is shell input until git sees it, and the shell wins. Measured on
  2026-08-18, twice in one day by two different people: `` `_ = caepTx` `` inside `-m "…"` ran as a
  command substitution and published a sentence with a hole; an unquoted `<<EOF` ate `` `push` ``
  and `` `main` `` out of three paragraphs. **Both pushes SUCCEEDED** — that is what makes it
  expensive: the corruption is silent and lands in published history that cannot be rewritten.

  ```sh
  git commit -s -F - <<'EOF'   # ← the QUOTES around EOF disable every expansion
  fix(thing): the subject line

  Body with `backticks`, $VARS and "quotes", all safe.
  EOF
  ```

  `-m` is for one-line subjects with no backticks. And if the body needs to SHOW a heredoc, build it
  with a script: an inner `EOF` closes the outer one and the shell dies with `unmatched`.

## DCO sign-off and CLA

Every commit must be signed off under the [Developer Certificate of Origin](https://developercertificate.org/):

```sh
git commit -s -m "feat: ..."
```

This appends a `Signed-off-by:` trailer with your name and email; configure `git config user.name` / `user.email` accordingly. The DCO check on pull requests rejects commits without it (a required status check, provisioned with the public repository).

Because the product is dual-licensed (AGPL plus a private commercial exception, alongside separately-licensed additive add-ons), the project also requires a **Contributor License Agreement (CLA)**. The CLA grants Olivares.AI the rights needed to offer the commercial exception while you retain ownership of your contribution. The project does **not** use an automated CLA bot: sign the CLA manually per [`CLA.md`](CLA.md) — download the Harmony Agreements PDF, sign it, and email it to `enterprise@olivares.ai` before your first contribution is merged (one-time).

## License frontier and mandatory SPDX headers

Licensing is fixed from the first commit. Every new source file **must** start with an SPDX header matching the directory it lives in. CI (`scripts/check-spdx.sh`) fails any file without a correct, module-matching identifier.

| Directory | License | SPDX identifier |
|---|---|---|
| `core/`, `modules/`, `web/` | GNU AGPL v3.0 | `AGPL-3.0-only` |
| `sdk/`, `connectors/`, `clients/` | Apache-2.0 | `Apache-2.0` |
| `enterprise/` (separate private repository — not in this repo) | Commercial | `LicenseRef-Olivares-Commercial` |

Use the comment syntax of the file's language. The two header lines (copyright + license) are, for `//`-comment files:

```go
// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
```

In `sdk/`, `connectors/` and `clients/` the identifier is `Apache-2.0`; in `enterprise/` it is `LicenseRef-Olivares-Commercial`. For hash-comment files (YAML, shell, Dockerfile, Taskfile) use `#`; for CSS use `/* … */`. Non-code files (Markdown, JSON, lockfiles, manifests) are annotated centrally in `REUSE.toml` and do **not** get inline headers, so the repository stays [REUSE](https://reuse.software/)-compliant.

## Code style

- **Go:** `gofmt` (enforced) and `golangci-lint` v2 (config in `.golangci.yml`). No unformatted code; no lint errors **in the gate's subset** — the full `task lint` is red today
for the reason recorded above, so "no lint errors" is the standard for what the gate runs, not a
claim that the whole tree is clean. In a `go.work` workspace, lint runs per-module — use `task lint:go` (which calls `scripts/lint.sh`), not a bare `golangci-lint run ./...`.
- **Web (`web/`):** ESLint and Prettier; TypeScript strict mode.
- Keep dependencies minimal and pinned — this is a security product and the dependency surface is part of the threat model.

## Adding a connector

Connectors are the breadth moat and the cleanest place to contribute. The single hard rule:

> A connector imports **only** from `sdk/`. It must never import from `core/`.

This keeps the Apache-2.0 / AGPL boundary clean and lets your connector ship without copyleft obligations. The boundary is enforced in CI by `task lint:boundary` (a `go list -deps` check over the real build graph).

**Start from the scaffold.** The fastest way to a correct connector is the generator,
which emits a complete, compiling, boundary-clean repository — including a lifecycle
test and a standalone boundary check:

```sh
go run ./sdk/scaffold/cmd/olivares-connector-new \
  -dir ./my-connector -name acme.my-connector \
  -module github.com/acme/olivares-connector-my-connector -kind source
```

See [`examples/build-a-connector/`](examples/build-a-connector/) for the full,
runnable walkthrough, and `go doc ./sdk` for the contract. The lifecycle is:

1. **`Descriptor()`** — the stable self-description (`Apache-2.0` SPDX header in every file).
2. **`Open(ctx, cfg)`** — validate config and connect; fail fast here, not in `Gather`.
3. **`SourceConnector.Gather`** (emit `model.Observation` facts) and/or
   **`OutputConnector.Notify`** (deliver events/findings to Slack, a SIEM, PagerDuty,
   a webhook). A source connector emits normalized observations into the engine's
   ingest path; an output connector delivers notifications to an external system.
4. **Be honest about coverage and confidence:** if a signal is approximate or untrusted
   (for example MCP annotations), report it as such — use the SDK's `Confidence` /
   attribution vocabulary — rather than presenting it as ground truth.
5. **`Close(ctx)`** — release resources (safe to call even if `Open` failed).
6. Add tests; run the [gate](#the-gate) (`task lint:spdx lint:boundary && task build:go && task test`).

A *first-party* connector lives under `connectors/<name>/` and is embedded in the
binary; a *third-party* connector ships as its own signed, distributed artifact (see
the generated `README.md` and [`docs/contracts/S142-external-connector-sdk.md`](docs/contracts/S142-external-connector-sdk.md)).
The set of first-party connectors is small and high-value by design; breadth comes from the community.
