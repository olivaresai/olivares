#!/usr/bin/env node
// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// test:header-link: the built docs home of every locale carries exactly one header link to the
// apex site, https://olivares.ai. The docs site is a separate property, so a visible link is the
// only edge between the two hosts (a 301 or a cross-host canonical is wrong: see
// tests/docs-subdomain.test.ts in the web repo). Reads dist/, not the sources: a config change that
// stops rendering the link edits no line of any component.
//
// Exit codes: 0 clean, 1 finding, 2 could not look.

import { existsSync } from 'node:fs'
import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'

import { LOCALES } from '../src/site-locales.mjs'

const DIST = fileURLToPath(new URL('../dist', import.meta.url))
const APEX_LINK = /<a\b[^>]*\bhref="https:\/\/olivares\.ai"/g

if (!existsSync(DIST)) {
  console.error('header-link: could not look: dist/ does not exist. Run `astro build` first.')
  process.exit(2)
}

const findings = []
for (const locale of Object.keys(LOCALES)) {
  const file = locale === 'root' ? 'index.html' : `${locale}/index.html`
  if (!existsSync(`${DIST}/${file}`)) {
    findings.push(`${file}: the built home of locale "${locale}" does not exist`)
    continue
  }
  const html = await readFile(`${DIST}/${file}`, 'utf8')
  const header = html.match(/<header\b[\s\S]*?<\/header>/)?.[0] ?? ''
  const inHeader = header.match(APEX_LINK)?.length ?? 0
  const inPage = html.match(APEX_LINK)?.length ?? 0
  if (inHeader !== 1 || inPage !== 1) {
    findings.push(`${file}: expected exactly one <a href="https://olivares.ai"> in the header and the page, found ${inHeader} in the header and ${inPage} in the page`)
  }
}

if (findings.length > 0) {
  console.error(`header-link: ${findings.length} finding(s)\n${findings.map((f) => `  ${f}`).join('\n')}`)
  process.exit(1)
}
console.log(`header-link: ok (${Object.keys(LOCALES).length} locales, one header link to https://olivares.ai each)`)
