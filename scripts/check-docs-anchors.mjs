// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// check-docs-anchors.mjs — fail on dangling in-page anchors in the docs-site,
// the i18n bug class the README nav fix resolved but never swept across the 115x6
// translated pages.
//
// THE BUG. A markdown in-page link `[text](#slug)` jumps to a heading whose id
// is derived from the heading TEXT (github-slugger, the slugger Starlight runs
// in its rehype pipeline). When a page is machine-translated, the heading
// text changes, so its slug changes — but the `#slug` written in the body still
// points at the ENGLISH slug. The jump silently lands nowhere. Build-time
// `docs-site/scripts/check-links.mjs` cannot catch this: it crawls dist/ and
// EXPLICITLY skips same-page anchors (`if (href.startsWith('#')) continue`).
//
// WHAT THIS CHECKS.
//   Primary (always, fail-closed): for every page, every in-page anchor
//   `](#slug)` resolves to a heading slug that actually exists IN THE SAME FILE.
//   Headings are slugged with a faithful re-implementation of github-slugger
//   (incl. its duplicate `-1/-2...` disambiguation); fenced code blocks, YAML
//   frontmatter, HTML comments and inline code spans are excluded. The slugger
//   keeps letters/numbers/marks of every script verbatim (no transliteration),
//   which is precisely why a translated heading no longer matches the English
//   `#slug`. Verified: the English authoritative pages report 0 problems (they
//   are self-consistent); the six MT locales surface the dangling anchors.
//
//   Secondary (--cross, OFF by default): for cross-page links that carry a
//   `#fragment` (`](/reference/glossary/#x)`, `](../foo/#y)`), resolve the target
//   page on disk and check the fragment against THAT page's heading slugs, with
//   EN-locale fallback (a `/<locale>/...` link falls back to the English page
//   when the localized file is absent, mirroring the site's EN-fallback routing).
//   This pass is WARN-ONLY and not wired into the gate: cross-references in the
//   MT locales deliberately point at EN-authoritative anchors, and route
//   resolution (Starlight base, index pages, the versioned `2026-06/` snapshot)
//   has edge cases that would make it non-deterministic as a hard gate. Use it
//   as an audit aid, not a blocker.
//
// Usage (run from the repo root):
//   node scripts/check-docs-anchors.mjs              # in-page sweep, exit 1 on any break
//   node scripts/check-docs-anchors.mjs --summary    # also print a per-locale tally
//   node scripts/check-docs-anchors.mjs --cross      # additionally audit cross-page #fragments (warn-only)
//   node scripts/check-docs-anchors.mjs --json       # machine-readable findings on stdout
//
// Pure Node, no dependencies — runs inside the Go-toolchain push gate the same
// way scripts/check-i18n-parity.mjs does (no `pnpm install` required).

import fs from 'node:fs'
import path from 'node:path'
import process from 'node:process'

const ROOT = path.resolve(path.dirname(new URL(import.meta.url).pathname), '..')
const DOCS = path.join(ROOT, 'docs-site', 'src', 'content', 'docs')

const args = new Set(process.argv.slice(2))
const SUMMARY = args.delete('--summary')
const CROSS = args.delete('--cross')
const JSON_OUT = args.delete('--json')
for (const a of args) {
  console.error(`unknown flag: ${a}`)
  process.exit(2)
}

if (!fs.existsSync(DOCS)) {
  console.error(`x docs anchors: not found: ${rel(DOCS)} — run from the repo root.`)
  process.exit(2)
}

// ── github-slugger, faithful re-implementation ────────────────────────────────
// github-slugger (the slugger Starlight runs in its rehype pipeline) lower-cases,
// strips punctuation + symbols, and turns each remaining space into a hyphen. It
// does NOT transliterate: letters/marks/numbers of EVERY script (accents, CJK,
// Cyrillic, ...) are kept verbatim — which is exactly why a translated heading
// yields a different slug than the English `#slug` left behind in the body.
//
// We express the strip set with Unicode property escapes instead of hand-typed
// code-point ranges (which are bug-prone): remove anything that is NOT a letter
// (\p{L}), number (\p{N}), mark (\p{M}), space, ASCII hyphen-minus or underscore.
// Keeping `-`/`_` and replacing each SINGLE space (never collapsing runs) matches
// github-slugger exactly — verified against the English authoritative pages, which
// are self-consistent and therefore MUST report zero dangling anchors. Edge cases
// covered: "Identity / NHI" -> "identity--nhi" (the removed slash leaves two
// independent spaces), "Drift (least-privilege drift)" -> keeps the inner hyphen,
// "Attribution(confidence)" with fullwidth parens -> the parens are punctuation
// and are stripped.
const SLUG_STRIP = /[^\p{L}\p{N}\p{M} _-]/gu

function slugBase(text) {
  return text.trim().toLowerCase().replace(SLUG_STRIP, '').replace(/ /g, '-')
}

/** A github-slugger-style stateful slugger: disambiguates repeats with -1, -2... */
function makeSlugger() {
  const seen = new Map()
  return (text) => {
    const base = slugBase(text)
    let value = base
    if (seen.has(base)) {
      let n = seen.get(base)
      do {
        n += 1
        value = `${base}-${n}`
      } while (seen.has(value))
      seen.set(base, n)
    }
    seen.set(value, 0)
    return value
  }
}

// ── markdown parsing (no deps) ────────────────────────────────────────────────
// Strip the leading YAML frontmatter block (--- ... ---) so its `:` / `#` chars
// are never mistaken for headings or anchors.
function stripFrontmatter(src) {
  if (!src.startsWith('---')) return src
  const end = src.indexOf('\n---', 3)
  if (end === -1) return src
  const after = src.indexOf('\n', end + 1)
  return after === -1 ? '' : src.slice(after + 1)
}

// Reduce heading text to what github-slugger sees after Starlight renders it:
// `[label](url)` -> `label`, inline `code` -> its contents, **bold**/_italic_
// markers removed, trailing closing `#`s (ATX-closed headings) dropped.
function headingText(raw) {
  let t = raw.replace(/^#{1,6}\s+/, '').replace(/\s+#+\s*$/, '')
  t = t.replace(/!\[([^\]]*)\]\([^)]*\)/g, '$1') // images -> alt
  t = t.replace(/\[([^\]]*)\]\([^)]*\)/g, '$1') // links -> text
  t = t.replace(/\[([^\]]*)\]\[[^\]]*\]/g, '$1') // ref links -> text
  t = t.replace(/`+/g, '')
  t = t.replace(/(\*\*|__|\*|_)/g, '')
  return t.trim()
}

/** Heading slug set for one file (fenced code + frontmatter excluded). */
function headingSlugs(src) {
  const body = stripFrontmatter(src)
  const slug = makeSlugger()
  const slugs = new Set()
  let fence = null // current fence marker (``` or ~~~) when inside a code block
  for (const line of body.split('\n')) {
    const f = line.match(/^\s*(```+|~~~+)/)
    if (fence) {
      if (f && line.trim().startsWith(fence)) fence = null
      continue
    }
    if (f) {
      fence = f[1].slice(0, 3) === '~~~' ? '~~~' : '```'
      continue
    }
    if (/^#{1,6}\s+/.test(line)) slugs.add(slug(headingText(line)))
  }
  return slugs
}

/**
 * In-page anchors and cross-page links carrying a #fragment, with code spans /
 * fenced blocks excluded so example markdown in the prose is never scanned.
 * Returns { inPage: [{frag,line}], cross: [{href,frag,line}] }.
 */
function extractLinks(src) {
  const body = stripFrontmatter(src)
  const inPage = []
  const cross = []
  let fence = null
  const lines = body.split('\n')
  for (let i = 0; i < lines.length; i++) {
    let line = lines[i]
    const f = line.match(/^\s*(```+|~~~+)/)
    if (fence) {
      if (f && line.trim().startsWith(fence)) fence = null
      continue
    }
    if (f) {
      fence = f[1].slice(0, 3) === '~~~' ? '~~~' : '```'
      continue
    }
    // Drop inline code spans so `](#x)` shown as an example is not treated as a link.
    line = line.replace(/`[^`]*`/g, '')
    for (const m of line.matchAll(/\]\(([^)\s]+)\)/g)) {
      const url = m[1]
      const hashIdx = url.indexOf('#')
      if (hashIdx === -1) continue
      const frag = url.slice(hashIdx + 1)
      if (!frag) continue
      if (hashIdx === 0) inPage.push({ frag, line: i + 1 })
      else cross.push({ href: url, frag, line: i + 1 })
    }
  }
  return { inPage, cross }
}

// ── file discovery ────────────────────────────────────────────────────────────
function walk(dir, acc = []) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, e.name)
    if (e.isDirectory()) walk(full, acc)
    else if (full.endsWith('.md') || full.endsWith('.mdx')) acc.push(full)
  }
  return acc
}

function rel(p) {
  return path.relative(ROOT, p)
}

// The locale of a page = first path segment under docs/ when it is a known
// locale dir; otherwise the page is English-authoritative (locale '').
const LOCALES = new Set(['de', 'es', 'fr', 'ja', 'ru', 'zh'])
function localeOf(file) {
  const seg = path.relative(DOCS, file).split(path.sep)[0]
  return LOCALES.has(seg) ? seg : ''
}

// ── primary pass: in-page anchors (fail-closed) ───────────────────────────────
const files = walk(DOCS).sort()
const slugCache = new Map()
function slugsFor(file) {
  if (!slugCache.has(file)) slugCache.set(file, headingSlugs(fs.readFileSync(file, 'utf8')))
  return slugCache.get(file)
}

const findings = [] // {file, line, frag}
const perLocale = {}
let anchorsChecked = 0

for (const file of files) {
  const src = fs.readFileSync(file, 'utf8')
  const slugs = slugsFor(file)
  const { inPage } = extractLinks(src)
  const loc = localeOf(file) || '(en)'
  perLocale[loc] = perLocale[loc] || { checked: 0, broken: 0 }
  for (const a of inPage) {
    anchorsChecked++
    perLocale[loc].checked++
    const target = decodeURIComponent(a.frag).toLowerCase()
    if (!slugs.has(target)) {
      findings.push({ file: rel(file), line: a.line, frag: a.frag })
      perLocale[loc].broken++
    }
  }
}

// ── secondary pass: cross-page #fragments (warn-only, opt-in) ──────────────────
const crossWarnings = []
if (CROSS) {
  // Resolve a doc href to a file on disk; null if the page itself can't be found
  // (page-existence is already the job of docs-site/scripts/check-links.mjs).
  const resolvePage = (fromFile, href) => {
    const noHash = href.split('#')[0].replace(/\/$/, '')
    if (!noHash) return fromFile // pure same-page (shouldn't reach here)
    let p
    if (noHash.startsWith('/')) p = path.join(DOCS, noHash.slice(1))
    else p = path.resolve(path.dirname(fromFile), noHash)
    const cands = [p + '.md', p + '.mdx', path.join(p, 'index.md'), path.join(p, 'index.mdx')]
    let hit = cands.find((c) => c.startsWith(DOCS) && fs.existsSync(c))
    if (hit) return hit
    // EN-fallback: drop a leading /<locale>/ segment and retry on the English page.
    const m = noHash.match(/^\/(de|es|fr|ja|ru|zh)\/(.*)$/)
    if (m) {
      const en = path.join(DOCS, m[2])
      const enc = [en + '.md', en + '.mdx', path.join(en, 'index.md'), path.join(en, 'index.mdx')]
      hit = enc.find((c) => c.startsWith(DOCS) && fs.existsSync(c))
      if (hit) return hit
    }
    return null
  }
  for (const file of files) {
    const { cross } = extractLinks(fs.readFileSync(file, 'utf8'))
    for (const c of cross) {
      if (/^(https?:|mailto:|tel:|data:|\/\/)/i.test(c.href)) continue
      const target = resolvePage(file, c.href)
      if (!target) continue // page-existence not our concern (check-links.mjs owns it)
      const frag = decodeURIComponent(c.frag).toLowerCase()
      if (!slugsFor(target).has(frag))
        crossWarnings.push(`${rel(file)}:${c.line}  ->  ${c.href}  (no #${c.frag} in ${rel(target)})`)
    }
  }
}

// ── report ────────────────────────────────────────────────────────────────────
if (JSON_OUT) {
  console.log(JSON.stringify({ anchorsChecked, perLocale, findings, crossWarnings }, null, 2))
  process.exit(findings.length ? 1 : 0)
}

if (SUMMARY || findings.length) {
  console.log(`docs anchors: ${anchorsChecked} in-page anchor(s) across ${files.length} pages`)
  for (const loc of Object.keys(perLocale).sort()) {
    const s = perLocale[loc]
    console.log(`  ${loc}: ${s.broken ? `${s.broken}/${s.checked} broken` : `${s.checked} OK`}`)
  }
}

if (CROSS && crossWarnings.length) {
  console.warn(`\n! cross-page #fragments not found (warn-only, ${crossWarnings.length}):`)
  for (const w of crossWarnings.sort()) console.warn(`  - ${w}`)
}

if (findings.length) {
  console.error(`\nx docs anchors FAILED — ${findings.length} dangling in-page anchor(s):\n`)
  for (const f of findings) console.error(`  ${f.file}:${f.line}  ->  #${f.frag}`)
  console.error(
    '\nEach `](#slug)` above points at a heading id that does NOT exist in the same file.' +
      '\nWhen a heading is translated its slug changes; update the anchor to the translated' +
      '\nslug (github-slugger of the heading text), exactly as the README nav fix did.',
  )
  process.exit(1)
}

console.log(`docs anchors OK — ${anchorsChecked} in-page anchors across ${files.length} pages all resolve.`)
