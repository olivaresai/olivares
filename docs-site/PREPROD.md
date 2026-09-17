<!-- SPDX-FileCopyrightText: 2026 Olivares.AI -->
<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Preprod for the documentation site

Order 38 gives three environments — `.dev` → `.preprod` → public. This is the middle one: the
same `docs-site/dist` production serves, on the sandbox zone, so a release candidate can be
reviewed before anything touches `docs.olivares.ai`.

## What is already done, and measured

| piece | state |
|---|---|
| DNS `docs-preprod.olivaresai.dev` | **created** — `AAAA 100::`, proxied, record `97bef290`. Order 24 is create-only; this was created with the Cloudflare API and verified by resolution. |
| DNS `docs.preprod.olivaresai.dev` | created first (record `788f6393`), **kept only as evidence** — see below. |
| `docs-site/wrangler.preprod.jsonc` | in this branch |
| Worker `olivares-docs-preprod` | **not deployed by this branch** — the release lane deploys it during E1b |
| Zone route | declared in the config; the release lane's E1b deploy creates it |

### ⛔ The hostname is one label deep, and that is forced, not chosen

The row named `docs.preprod.olivaresai.dev`. It cannot serve HTTPS on this zone. Measured
2026-08-29 with both records live and a positive control:

```
https://hooks-sandbox.olivaresai.dev/   -> 200                      (one label, existing convention)
https://docs-preprod.olivaresai.dev/    -> 522                      (one label: TLS terminated, then
                                                                     Cloudflare reached the 100::
                                                                     discard origin — the correct
                                                                     state before a route exists)
https://docs.preprod.olivaresai.dev/    -> TLS handshake failure, no HTTP at all
```

`GET /zones/{id}/ssl/certificate_packs?status=all` returns **zero** packs: the zone has Universal
SSL only, which covers the apex and `*.olivaresai.dev` — **one label**. A third-level name needs
Advanced Certificate Manager or Total TLS. Both are paid add-ons and the choice is not a lane's,
so preprod uses the name that works and the two-level record stays inert as evidence. Order 24 is
create-only: this task neither deletes nor routes that record.

The 522 is the point: it proves TLS terminated. A name without a certificate never gets far
enough to return an HTTP status at all, which is what the third line shows.

## The exact deploy command

The release lane runs this from the repository root during E1b, on the candidate SHA, after `docs-site` has
been built. This B18 branch configures and verifies the Worker but does not deploy it:

```sh
npm --prefix docs-site ci
npm --prefix docs-site test
node scripts/check-wrangler-pin.mjs        # refuse a wrangler that is not the pinned one
npm --prefix docs-site exec -- wrangler deploy \
  --cwd docs-site --config wrangler.preprod.jsonc
```

⛔ **The third line replaces what this file first told you to run, and the reason is worth
keeping.** It used to be `npm --prefix docs-site exec --no-install -- wrangler --version`, copied
from the production workflow. **`--no-install` is an npx flag, not an `npm exec` flag** — npm says
`Unknown cli config "--install"` and ignores it. Both of 2026-08-29's production documentation
deploys therefore fetched wrangler `4.127.1` from the registry at deploy time and ran it with the
production credential. A flag that reads as a control and is not one is worse than no control.

`scripts/check-wrangler-pin.mjs` **runs the binary that would run** and compares what it reports
against the exact pin in `docs-site/package.json`. Three answers: 0 clean, 1 finding, 2
could-not-look — a missing binary is 2 with its reason, never a pass by falling back to a download.

This branch carries both halves: `docs-site/package.json` pins an exact wrangler version, the
lockfile resolves that same version, and `scripts/check-wrangler-pin.mjs` reads the pin at run time
and verifies the installed binary against it.
Do not work around a refusal by dropping the check or by letting `npm exec` fetch another version.

**The workflow runs the same command from the preprod repository.** The first step of
`docs-site-deploy.yml` selects its target from a fixed table keyed by `github.repository_id`: the
Community preprod repository gets `wrangler.preprod.jsonc` and `docs-preprod.olivaresai.dev`; the
development hub and the public Community repository keep `wrangler.jsonc` and `docs.olivares.ai`;
any other repository refuses before the credential step. The deploy passes that file as `--config`
(resolved inside `--cwd docs-site`), and the live verification runs
`check-docs-site-live.sh --host` against the selected host. The PUBLISH confirmation, the main-only
ref, the pin check and the deploy concurrency are unchanged.

## The credential

The deploy needs the same Cloudflare API token production uses. **Its name is
`CLOUDFLARE_API_TOKEN`** — the repository secret the production workflow reads
(`docs-site-deploy.yml`), and whose presence that workflow's credential step refuses to proceed
without, naming it.

Never echo it. Load it by sourcing, with tracing off:

```sh
set +x; set -a; . /path/to/the/env/file; set +a
```

The value is not in this file, not in any commit, and is not printed by any command here.

## What is NOT mine, and is still open

- **The Worker deploy itself belongs to the release lane during E1b.** B18 must not run it; the config and
  command above are ready for that handoff.
- Whether preprod should live behind Cloudflare Access. Raised, not decided.

## `noindex` is shipped, and I took that decision — here is why, and how to undo it

Preprod serves a complete, crawlable copy of the documentation on a public hostname: a duplicate
of `docs.olivares.ai`. Production already sets its own canonical, so the duplicate carries the
risk, and search-engine damage is slow to notice and slower to undo. **The reversible direction
is to ship `noindex` and remove it if anyone ever wants preprod indexed; the irreversible one is
the other way round.** With the deploy imminent I took the reversible one rather than wait.

⚠ **And I first said this was "one line in the config", which was wrong.** A pure-assets Worker
has nowhere to set a response header, and the obvious alternative — `docs-site/public/_headers` —
is part of the BUILD and ships to production, so `noindex` there would deindex
`docs.olivares.ai`. It needs code, and the code must exist only on the preprod side. That is
`docs-site/preprod-worker.ts`, referenced by `wrangler.preprod.jsonc` and by nothing else.
Production keeps serving assets with no code in its response path, which was a deliberate
property of that config and stays one.

Cloudflare serves matching assets before Worker code by default; the preprod config therefore
declares both the `ASSETS` binding and `run_worker_first: true`, as required by the official
binding contract: <https://developers.cloudflare.com/workers/static-assets/binding/#run_worker_first>.

**To undo:** delete `"main"` from `wrangler.preprod.jsonc` and the worker file; nothing else
refers to either.

Before deploying, the dry-run must compile the Worker with the `ASSETS` binding and prove that
`assets.run_worker_first` is enabled. After the release lane deploys, E1b verifies the live header with
`curl -sI https://docs-preprod.olivaresai.dev/ | grep -i x-robots-tag`; a dry-run cannot prove a
live response.
