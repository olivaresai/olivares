#!/usr/bin/env node
// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// published-locales.mjs — the data contract between check-cli-ref-docs.sh and
// the standalone Go generator.
//
// IMPORTED, not parsed. docs-site/src/site-locales.mjs is the one declaration of
// which locales the docs site publishes. A second text-scan of that file (or a
// hardcoded Go list) is the silent-green this repository has already paid for:
// a NON-EMPTY WRONG locale set, so a new published locale's CLI page can lag
// the binary while the gate reports in-sync. This helper `import()`s the
// module, derives published locales from LOCALES minus `root`, derives archived
// slugs from VERSIONS, and refuses if the convenience projections disagree.
//
// Stdout is one JSON object, schema olivares.cli-ref-locales/1. Any failure is
// exit 2 on stderr; an empty successful roster is also exit 2.
//
// Usage: node scripts/cli-ref-docs/published-locales.mjs <repository-root>

import fs from 'node:fs'
import path from 'node:path'
import process from 'node:process'
import { pathToFileURL } from 'node:url'

const MANIFEST_REL = 'docs-site/src/site-locales.mjs'
const SAFE_SEGMENT = /^[A-Za-z0-9][A-Za-z0-9._-]*$/
const SCHEMA = 'olivares.cli-ref-locales/1'

function fail(msg) {
  process.stderr.write(`cli-ref-published-locales: CANNOT LOOK — ${msg}\n`)
  process.exit(2)
}

const root = process.argv[2]
if (!root || !String(root).trim()) fail('no repository root was given')

const manifest = path.join(root, MANIFEST_REL)
if (!fs.existsSync(manifest)) fail(`cannot find ${MANIFEST_REL} (looked in ${root})`)

let mod
try {
  mod = await import(pathToFileURL(manifest).href)
} catch (e) {
  fail(`${MANIFEST_REL} could not be imported: ${e && e.message ? e.message : e}`)
}

if (typeof mod.LOCALES !== 'object' || mod.LOCALES === null) {
  fail(`${MANIFEST_REL} must export a LOCALES object`)
}
if (!Array.isArray(mod.VERSIONS)) {
  fail(`${MANIFEST_REL} must export a VERSIONS array`)
}

const published = Object.keys(mod.LOCALES).filter((l) => l !== 'root')
const archived = mod.VERSIONS.map((v) => (v && typeof v === 'object' ? v.slug : undefined))

if (published.length === 0) fail(`${MANIFEST_REL} declares ZERO published locales`)

function assertSegments(list, what) {
  const seen = new Set()
  for (const v of list) {
    if (typeof v !== 'string' || !SAFE_SEGMENT.test(v) || v === '.' || v === '..') {
      fail(`${MANIFEST_REL}: ${what} ${JSON.stringify(v)} is not a safe directory name`)
    }
    if (seen.has(v)) fail(`${MANIFEST_REL}: ${what} ${JSON.stringify(v)} is declared twice`)
    seen.add(v)
  }
  return seen
}

const locSet = assertSegments(published, 'locale')
assertSegments(archived, 'archived version slug')
for (const a of archived) {
  if (locSet.has(a)) {
    fail(`${MANIFEST_REL}: ${JSON.stringify(a)} is both a locale and an archive slug`)
  }
}

const same = (a, b) => Array.isArray(a) && JSON.stringify(a) === JSON.stringify(b)
if (!same(mod.PUBLISHED_LOCALES, published)) {
  fail(`${MANIFEST_REL}: PUBLISHED_LOCALES disagrees with LOCALES minus root`)
}
if (!same(mod.ARCHIVED_SLUGS, archived)) {
  fail(`${MANIFEST_REL}: ARCHIVED_SLUGS disagrees with the VERSIONS slugs`)
}

process.stdout.write(
  JSON.stringify({ schema: SCHEMA, published, archived }) + '\n',
)
