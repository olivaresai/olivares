# Contributing to Olivares AI

Olivares AI is in beta; the architecture, data model, SDK and API are still stabilizing.
Discuss non-trivial changes in an issue before sending a pull request.

## Development setup

The [dev container](.devcontainer/) provisions pinned Go and development tools,
Node 24, the GitHub CLI and Git hooks through `task tools && task setup`. Open it
with VS Code's "Reopen in Container" or `devcontainer up`, then run [the gate](#the-gate).

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

### Release versions

`RELEASE-VERSION` owns the current release number. Every place that names the
current or next release carries a release mark, the same in every language. In
Markdown the mark is a pair of HTML comments whose text is `release` and
`/release`, around a word, a code span, a link or a whole fenced block; in MDX
it is the JSX comment pair `{/* release */}` and `{/* /release */}`; in any
other file it is a comment line `# release` (or `// release`) before the lines
and `# /release` after them. After changing `RELEASE-VERSION`,
run `task release:stamp`: it rewrites the versions inside marks and nothing
else; the public export runs the same generator. `task lint:release-version`
refuses a `1.x` version outside a mark unless it is a record (a dated changelog
entry, an install witness, a compatibility floor, a section citation) or a row
of `NOT_RELEASES` in `scripts/check-release-version.sh` names what it versions.
Attach `<!-- release-fixed -->` immediately after a fixed historical version
(such as a deprecation date); it stays fixed and must not exceed the release
baseline. The annotation applies only to that token, not to other versions on
its line.

Engine builds share `scripts/build-ldflags.sh`: version comes from that file,
commit from Git, and date from the commit timestamp (or `SOURCE_DATE_EPOCH`).
Source archives use commit `none` and epoch zero. Container `VERSION`/`COMMIT`
arguments remain supported. GoReleaser snapshots derive their names from the
same release record; a release whose tag disagrees is refused before building.
Stripping flags and the two release trust anchors remain build policy.

### The gate

Before submitting a pull request for acceptance, run these local checks:

```sh
task lint:spdx lint:boundary   # SPDX headers + the Apache/AGPL license boundary
task build:go                  # compile every workspace module
task test                      # the race-enabled Go test suite
```

`task test` covers Go, including the applicable private-module legs; web tests run
separately with `task test:web`. For web changes, also run `task check:web web:check`.
[`mainline-ci`](.github/workflows/mainline-ci.yml) uses `test:functional` plus
`test:race-hot` and additional checks; it does not run the identical local command
set. The slow root-package race tail has separate `race-full` qualification. Read
the workflow's jobs and steps for the exact candidate; these commands do not claim
that every required check has run or passed.

For changes under `web/`, commit the sources only. Generated files under
`core/internal/webui/dist` and `bundle-source.stamp` are ignored. `task web:check`
builds once and checks the fresh output; PR and mainline CI retain that output as
an artifact for their Go consumers. Those jobs require the shipped-document CSP
check to run; a missing bundle fails instead of skipping the check.
The release build runs the same builder before embedding the console. Go-only
development builds contain a placeholder until `task build:web` has run.
The required `web` check must pass on the candidate before merge; a later check
qualifies only that later commit.

The `pre-push` hook ([`.githooks/pre-push`](.githooks/pre-push)) checks what a push
changes, over the commits the remote does not have yet, in under two minutes:

- `gitleaks` over the pushed commits;
- SPDX headers of the changed files (`scripts/check-spdx.sh --files`);
- no generated console output in the push (`scripts/check-web-bundle-freshness.sh`);
- the license boundary (`task lint:boundary`) when Go code or modules changed;
- `go vet` on the touched packages and their tests (`scripts/changed-go-packages.sh`);
- the commit identity of the pushed commits (`task lint:commit-identity`), a hub-only leg
  that answers NOT APPLICABLE in the public tree.

It checks each pushed commit itself, in a temporary checkout when that commit is not your clean
`HEAD`, with this checkout's scripts. It needs `gitleaks`, `task` and Go on `PATH`
(`task tools`). Pull-request CI runs SPDX, the license boundary and the build again, and
gitleaks runs on every push to main, so the hook is your first secret scan. Run the build and
test checks above yourself for the scope of your change. A draft PR can preserve work with
failures or checks not yet run; it is not acceptance for integration. Use the normal hooks and
merge checks: declaring `--no-verify` or an admin bypass does not satisfy missing checks.

Full `task lint`, including `golangci-lint`, remains outside the check set because
the code has unresolved findings. It is not a toolchain failure.
The issue limits in `.golangci.yml` are both `0`; counts must include all findings.
Most `misspell` findings concern British spelling against the configured US locale
or false positives. Check identifiers before editing: renaming one can change an API.

```sh
bash scripts/misspell-census.sh   # derive category counts; fail if they do not sum to the total
```

Use `--clase "comment"` to list locations in a class. Every displayed class label
and its existing alias is accepted.

CI runs the structural lints, `govulncheck` and a full-history `gitleaks` scan.
`task fmt` formats Go and web sources.

### PostgreSQL for tests

PostgreSQL tests require the DSNs below. They skip locally when no server is
configured and fail in CI when its promised fixture is absent. A run that skips
them is partial and cannot establish PostgreSQL behavior.

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

**PostgreSQL versions: 15–18.** The minimum advances with upstream end of life.
Local tests exercised 15.x and the CI service uses 16.x.
[The scheduled major-version workflow](.github/workflows/pg-majors.yml) exercises
all four versions; it does not run on pull requests and is not a required check.
The managed-backup strategy remains pending for 17/18: Helm and Operator defaults
pin `pg_dump` 16, which cannot dump those servers. Their documented state remains
"contract + DR pending". Pre-release majors are rejected at boot by default.

### Adding a documentation page

The docs site publishes English and six locales (`es`, `zh`, `ru`, `ja`, `de`, `fr`).
For a new page under `docs-site/src/content/docs/`, add its translations or a
dated exemption in `docs-site/i18n-parity-waivers.json`. Console key and anchor
checks do not detect missing pages.

```sh
task lint:docs-parity   # list missing translated pages by locale
```

The informed mode (`--informed --summary`) reports pages absent from every locale.
It fails for a page absent from some locales, an orphan, a route collision, a
missing locale directory or an invalid waiver. A waiver requires an explicit
locale list (`"*"` is rejected), a reason of at least 20 characters and a real
date; an expiry is preferred. A waiver that no longer suppresses a finding fails.
Use `node scripts/check-docs-parity.mjs --strict` for the stricter check.

The locale list lives in `docs-site/src/site-locales.mjs`, which `astro.config.mjs` and this
gate both import. Adding a locale there is the only change needed: the site and the parity gate
pick it up.

Propose architecture changes through an issue. Keep internal development
records out of the public repository and documentation site.

```sh
task lint:adr-not-published   # reject internal decision pages, links or a publisher in docs-site
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

- **External contributors** open a pull request from a branch or fork — do not push to
  `main` directly — so the CLA/DCO and review flow below applies.
- **CI before integration:** [`mainline-ci`](.github/workflows/mainline-ci.yml) has
  `workflow_dispatch` and push to `main`, subject to its journal-path exclusions;
  it has no pull-request trigger. The maintainer dispatches it on the merge candidate
  and requires all applicable jobs and substantive steps to pass on its exact SHA,
  including checks beyond the repository's required contexts. A changed candidate
  needs its own verdict; verify the merge tree and post-merge CI separately.
  [`pr-ci`](.github/workflows/pr-ci.yml) provides a smaller automatic PR regime for
  the public repository or the configured preprod profile. Its repository guards
  can skip the private-dev jobs; skipped or absent checks are not passes.
- **Conventional Commits**, in English: `feat:`, `fix:`, `refactor:`, `docs:`, `test:`, `chore:`, etc. Commit messages are linted by the `commit-msg` hook locally and by CI.
- Keep pull requests focused; describe what changed and why, and link the issue.
- Write commit bodies with `git commit -F -` and a quoted heredoc so the shell
  preserves backticks, `$` and quotes.

  ```sh
  git commit -s -F - <<'EOF'   # ← the QUOTES around EOF disable every expansion
  fix(thing): the subject line

  Body with `backticks`, $VARS and "quotes", all safe.
  EOF
  ```

  Use `-m` only for one-line subjects without backticks. If the body includes
  a heredoc, build it with a script or use distinct delimiters.

## DCO sign-off and CLA

Attribution trailers (`Co-Authored-By`, `Co-Developed-By`, `Assisted-By`,
`Generated-By`, and `Generated-With`) are refused regardless of the address.
The commit-msg hook and PR CI use `scripts/check-commit-trailers.sh` for this policy.
The hook checks the message under the invoking `git commit` or `git merge` cleanup
mode, including command-line overrides of `commit.cleanup`. Verbose commit diffs
and scissors sections are discarded before checking DCO and attribution.
On Linux it reads that process's arguments from procfs without logging them.
When those arguments are unavailable, ordinary edited commits and merges still work: the
hook moves Git's trailing editor comment block before the existing trailer
paragraph, preserving the body, comments, and sign-offs. For a verbose/scissors
appendix, it instead inserts Git's native `---` patch divider before the editor
footer, keeping the existing sign-off above the scissors cut. This divider remains
in the stored message; the original footer and diff stay available for Git's
cleanup. It validates the result
as stored text and under comment/scissors cleanup before updating the message;
it never adds a sign-off or removes attribution. This also keeps a copied editor
footer valid if Git retains comments with `--cleanup=verbatim`. Messages without
an editor footer are accepted unchanged when the policy holds under every cleanup
mode. If cleanup could discard the only sign-off or retain invalid trailing prose,
the hook refuses the message instead of guessing.
Plain message-file and CI range checks inspect the stored text without rewriting it.
Automatic comment-marker recovery uses Git's English editor hints or scissors
marker. If those hints are removed or translated, keep `core.commentChar` fixed
when editing: Git does not export its automatically selected marker either.

Every commit must be signed off under the [Developer Certificate of Origin](https://developercertificate.org/):

```sh
git commit -s -m "feat: ..."
```

This appends a `Signed-off-by:` trailer with your name and email; configure `git config user.name` / `user.email` accordingly. The DCO check on pull requests rejects commits without it (a required status check, provisioned with the public repository).

Because the product is dual-licensed (AGPL plus a private commercial exception, alongside separately-licensed additive add-ons), the project also requires a **Contributor License Agreement (CLA)**. The CLA grants Olivares.AI the rights needed to offer the commercial exception while you retain ownership of your contribution. The project does **not** use an automated CLA bot: sign the CLA manually per [`CLA.md`](CLA.md) — download the Harmony Agreements PDF, sign it, and email it to `enterprise@olivares.ai` before your first contribution is merged (one-time).

The `CLA / cla` pull-request check (`pull_request_target`, base code only) passes maintainers, whose GitHub
author association is `OWNER`, `MEMBER` or `COLLABORATOR`. For every other author it reads
`.github/CLA-SIGNATURES` from the base commit. After receiving the signed PDF, a
maintainer adds `<github-login> <YYYY-MM-DD> HA-CLA-I-1.0` (or `HA-CLA-E-1.0`
for an entity) in a separate change to the base branch. Update the PR branch
from that base to rerun the check. A signature added only in the contributor's
PR does not grant access. The public record contains no PDFs or email addresses.

Third-party Go dependencies in both editions must use MIT, BSD-2-Clause,
BSD-3-Clause, ISC, Apache-2.0, or unmodified MPL-2.0. The shared gate uses the
actual build tags and refuses unknown licenses. Its default roots include the
engine and the embedded connectors from `scripts/build-connectors.sh --list`.
Use repeated `--target GOOS/GOARCH` arguments to collect the union for a release. MPL replacements, vendored
copies and modified module-cache contents are rejected. Run it with:

```sh
go install github.com/google/go-licenses/v2@v2.0.1
python3 scripts/license-gate.py --tags release
```

The console build emits `licenses/console.json`. After building the console, use
`--notice .license-notices/NOTICE-community` to generate the Community NOTICE
from the first-party notice, Go license and upstream NOTICE texts, and the
console dependency metadata and available license texts. Release archives, native packages and images carry
this generated notice. An assembled private build calls the same script with
its own `--tags`, `--edition`, `--notice`, and package arguments.

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

Use US English for comments, diagnostics, and test names. Keep locale JSON and
Unicode fixture values. For inline test or locale data, use a plain quoted string
assignment with `# language-data: fixture` or `// language-data: fixture`. In JavaScript
and TypeScript, declare the variable with `const`, `let`, or `var`. Comments, call
arguments, and executable interpolation remain checked. Review each marker to confirm
that its value is input data.
Run `python3 scripts/test-source-language.py`, then check staged or committed changes
with `python3 scripts/check-source-language.py --base origin/main`.
Run `task lint:tooling-language` to check all existing contributor-facing descriptions,
workflow names and annotations, and script diagnostics in the public file set.
This check also runs during public export and works without Git metadata; published
task names and script paths remain unchanged. The check matches
common Spanish patterns; review the remaining wording.

## Adding a connector

Connectors extend the product through the SDK:

> A connector imports **only** from `sdk/`. It must never import from `core/`.

This keeps the Apache-2.0 / AGPL boundary clean and lets your connector ship without copyleft obligations. The boundary is enforced in CI by `task lint:boundary` (a `go list -deps` check over the real build graph).

The scaffold generates a compiling connector with a lifecycle test and a
standalone license-boundary check:

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
Third-party connectors extend the first-party set.

## Migration history

Run `bash scripts/check-migrations.sh` before submitting migration changes. It
compares SHA-256 content hashes with the branch's merge base against `origin/main`;
on main it uses the preceding commit, and in a repository without that remote it
uses `HEAD`. Set `OLIVARES_MIGRATION_BASE=<commit>` to select an explicit baseline.
PR CI pins this to the pull request's base SHA. Missing history or unreadable
source is an error, never an empty successful comparison.

The same entry point checks SQL migrations (including D1), core Go migrations,
Go callback/generator dependencies and plan-call bindings (including local
initializers), auxiliary runner plans and their tracker namespaces, core descriptor
declarations, and module trigger transitions. Existing files and versions cannot be removed or rewritten;
add a forward migration. New versions and new descriptor declarations are allowed.
Go hashes use formatted syntax without comments; SQL hashes cover exact bytes.
The Go guard is deliberately conservative about shared source: preserve historical
constructors and helpers, and put new behavior in new helpers. It does not execute
callbacks or add checksums to deployed databases. Historical metadata, including
core v4's `expand` label, is preserved rather than silently relabeled.

Schema installation uses `core/migrate`. Core v21 adopts descriptor schema and OS
account reservations, v22 normalizes federation aliases, and v23 adopts audit
blinding schema. Each commits with its version record. New schema changes require
a new version and immutable historical descriptor inputs. Module descriptors use
version 1 in `schema_migrations_tbl_<table>`; existing `applied_module_tables` rows
are retained. Later module changes use the module's numbered SQL migrations.
Rollout, scope, lineage, directory guards, leader epochs and PostgreSQL rate-limit
storage use the same runner with separate tracking tables.

Use `Apply` for a migration-owned transaction or `ApplyTx` when schema and boot
admission/data must share a transaction. `ReconcileTx` is restricted to immutable,
forward-only security repair plans: staged directory guards retain their published
restart repair behavior without rewriting migration history. Schema evolution is
one-shot; per-boot security verification and privilege reconciliation still run.
Missing/changed enforced guards and reverted repair records refuse boot.

`python3 scripts/test-migration-immutability.py` exercises edits, deletions,
callback definitions and injection changes, registration removal, new versions
and unavailable baselines.
