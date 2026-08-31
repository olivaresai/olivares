// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// test:links — fail on any broken INTERNAL link in the built site (dist/).
// Crawls every built HTML page, extracts <a href>, and resolves each internal
// link to a real file under dist/. External (http/https/mailto/tel), protocol-
// relative and pure-anchor links are skipped. (DOC-01/DOC-02 honesty: no dead
// links in the Diátaxis tree.)

import { readdir, readFile } from 'node:fs/promises'
import { existsSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import path from 'node:path'

const DIST = fileURLToPath(new URL('../dist', import.meta.url))

if (!existsSync(DIST)) {
  console.error('✗ links: dist/ not found — run `astro build` first.')
  process.exit(1)
}

async function htmlFiles(dir, acc = []) {
  for (const e of await readdir(dir, { withFileTypes: true })) {
    const full = path.join(dir, e.name)
    if (e.isDirectory()) await htmlFiles(full, acc)
    else if (e.name.endsWith('.html')) acc.push(full)
  }
  return acc
}

// Map a built file to its URL path (dist/foo/index.html -> /foo/).
function urlOf(file) {
  let rel = '/' + path.relative(DIST, file).split(path.sep).join('/')
  if (rel.endsWith('/index.html')) rel = rel.slice(0, -'index.html'.length)
  return rel
}

// Does a resolved site path exist as a built artifact?
function resolves(urlPath) {
  const clean = decodeURIComponent(urlPath.split('#')[0].split('?')[0])
  const base = path.join(DIST, clean.replace(/^\//, ''))
  const candidates = clean.endsWith('/')
    ? [path.join(base, 'index.html')]
    : [base, base + '.html', path.join(base, 'index.html')]
  return candidates.some((c) => existsSync(c))
}

const HREF = /<a\b[^>]*?\shref=("([^"]*)"|'([^']*)')/gi
const broken = []
let checked = 0

for (const file of await htmlFiles(DIST)) {
  const html = await readFile(file, 'utf8')
  const fromUrl = urlOf(file)
  let m
  while ((m = HREF.exec(html)) !== null) {
    const href = (m[2] ?? m[3] ?? '').trim()
    if (!href) continue
    if (/^(https?:|mailto:|tel:|data:|\/\/)/i.test(href)) continue // external
    if (href.startsWith('#')) continue // same-page anchor
    // Resolve relative to this page's URL.
    const resolved = href.startsWith('/')
      ? href
      : new URL(href, 'https://x' + fromUrl).pathname + (href.includes('#') ? '#' + href.split('#')[1] : '')
    checked++
    if (!resolves(resolved)) broken.push(`${fromUrl}  →  ${href}  (resolved ${resolved.split('#')[0]})`)
  }
}

if (broken.length > 0) {
  console.error(`\n✗ ${broken.length} broken internal link(s) of ${checked} checked:`)
  for (const b of [...new Set(broken)].sort()) console.error(`  - ${b}`)
  process.exit(1)
}

console.log(`✓ links: ${checked} internal links across the site all resolve.`)
