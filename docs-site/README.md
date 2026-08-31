<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Olivares AI — product documentation site

The product documentation for Olivares AI — integrate, manage and secure AI in
your enterprise — built with
[Astro Starlight](https://starlight.astro.build/). It is **part of the AGPL
product** (like `/web`); it is not a connector and never imports `/core`.

The content is organized with the **[Diátaxis](https://diataxis.fr/)** information
architecture — `tutorials/`, `how-to/`, `reference/`, `explanation/` (+ a `start/`
hub) under `src/content/docs/`.

## Develop

```bash
npm install
npm run dev      # local preview (also runs the ADR sync first)
npm run build    # static site -> dist/  (ADR sync + astro build + Pagefind)
npm test         # spec-lint + build + anti-drift + link-check
```

`npm test` runs:

- `spec:openapi` — validates the product's OpenAPI 3.1 contract (`@readme/openapi-parser`).
- `spec:asyncapi` — validates the AsyncAPI 3.0 event-bus spec (`@asyncapi/parser`).
- `build` — ADR sync, then `astro build` (Pagefind search index + sitemap).
- `test:drift` — proves the API reference renders from the real spec (no copy).
- `test:modules` — proves every module dir has a linked reference page (no catalog orphans).
- `test:links` — fails on any broken internal link across the built site.

## Release build (publishing is separate and owner-gated)

Building or testing this tree never changes the live site. Publishing is a separate,
deliberate act — see [Publishing](#publishing) below.

```bash
npm test              # full gate: specs + build + anti-drift + link-check
```

Do not infer that the current source tree is live merely because its build passes: it
is measurably often not. `bash ../scripts/check-docs-site-live.sh` answers that
question against the running site.

## How content is generated (single sources, no divergent copies)

- **REST API reference** (`/reference/api/`) is rendered at build time **directly
  from `../web/openapi/openapi.json`** (the contract served by `core/api`) via
  `starlight-openapi`. There is **no copy** in this project, so the reference cannot
  drift; `scripts/check-spec-drift.mjs` enforces that wiring.
- **Event-bus reference** (`/reference/events/`) renders from
  `public/asyncapi/asyncapi.yaml` — an AsyncAPI 3.0 contract hand-derived from the
  Go SDK (there is no AsyncAPI codegen in the product, so this file is the source of
  truth; keep it in step with `sdk/event` + `sdk/model`).
- **ADRs are NOT published here, and that is deliberate.** Until 2026-08-25 this
  site carried 229 generated pages under `explanation/adr/`, written from the
  project's internal MADR register by `scripts/sync-adr.mjs`. The project owner
  withdrew the whole section that day: architecture decision records are internal
  development documentation, and publishing them can compromise the integrity and
  security of the project and its paid business/enterprise part. The register stays
  exactly where it was — in the development repository, committed and private — and
  the generator, its strings file and its `lint:adr-sync` gate were removed with it.
  `task lint:adr-not-published` now holds the line in the other direction: it fails
  if an ADR page, an ADR-shaped filename, a live link to the withdrawn route, or the
  generator itself comes back to this tree.

## Search & i18n

- **Search** is [Pagefind](https://pagefind.app/) — local, client-side, built into
  Starlight. No external search service, consistent with the self-hosted product.
- **i18n**: English at the root, with six translated locales under `/es`, `/de`,
  `/fr`, `/ja`, `/ru`, `/zh`. Page parity between English and every locale is
  enforced in CI by `../scripts/check-docs-parity.mjs` (strict — a missing page
  fails the build); untranslated pages fall back to English automatically.

## Versioning

Versioning is provided by the [`starlight-versions`](https://starlight-versions.vercel.app/)
plugin and is **active**. The only archived version is honestly labelled as a
**dated docs snapshot**, not a product release: slug `2026-06`, label
**"2026-06 (pre-1.0 preview)"**. The planned first public release is `v26.8.0`,
but until that release is cut the current tree remains **Latest** rather than a
fabricated release archive.

How it works here:

- The archived content lives at `src/content/docs/2026-06/` (committed; generated
  once by the plugin at activation) plus `src/content/versions/2026-06.json` (the
  versioned sidebar). The version selector renders in the site header.
- **Do not edit or translate the snapshot** — it is a frozen archive. The Spanish
  locale translates the *current* docs only.
- **The `starlight-openapi` interaction** (known, handled): the generated REST
  reference renders for the *current* version only, so the snapshot's links to
  `/reference/api/` (and the AsyncAPI asset) were pointed at the current reference
  after the snapshot was cut — the `test:links` gate verifies every archived link
  resolves.

Archived versions are declared in `src/site-locales.mjs`, which is also consumed by
the parity and ADR-sync checks. Creating a release archive is a separate,
maintainer-reviewed change; do not hand-edit an archived tree or add a speculative
version to `astro.config.mjs`.

## Publishing

⛔ **Corrected 2026-08-27.** Two sections of this README used to say this tree carried
"no `wrangler` configuration or publishing workflow". That stopped being true on
2026-08-25, when `wrangler.jsonc` landed in this very directory — the commit that added
it (`1852bef7e`) even called it "the wrangler config the README said did not exist", so
the contradiction was noticed and not fixed. And it did not stay private: the public
export carries this directory, build caches aside, so the config went out to the public
tree standing next to a README that denied it existed.

What is true today, measured:

| | |
|---|---|
| Worker | `olivares-docs`, a static-assets Worker; config in [`wrangler.jsonc`](./wrangler.jsonc) |
| Live at | `https://docs.olivares.ai` — a **zone route** onto that Worker. The hostname's DNS is still carried by a custom domain on the marketing Worker; `wrangler.jsonc` documents the pending migration and why its order matters |
| Build artifact | `.github/workflows/docs-site-artifact.yml` — dispatch-only, uploads `dist/`, **deploys nothing** |
| Deploy | `.github/workflows/docs-site-deploy.yml` — dispatch-only, requires typing `PUBLISH`, and **refuses with a named secret** if `CLOUDFLARE_API_TOKEN` is absent (it is, in this repository, today) |
| Staleness | `bash ../scripts/check-docs-site-live.sh` — compares the live site against what this tree promises. `0` up to date · `1` stale or broken · `2` could not look |

Publishing is still owner-gated and still deliberate: there is no push trigger, and the
deploy workflow will not run without an explicit confirmation input. What changed is
that the path exists, is written down, and is checkable — rather than being a manual
step somebody had to remember, which is how `public/_redirects` came to sit on `main`
for two days while the live site answered 404 for both routes it promises.
