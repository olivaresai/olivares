// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Recalculate inline hashes after font-display changes and move Astro's CSP
// before governed elements. Missing or duplicate policy directives fail closed.
// JSON-LD hashes are harmless extra entries; data blocks are not executable scripts.
// The independent response-policy gate checks the final artifact.
// Exit: 0 sealed, 1 invalid artifact, 2 artifact unavailable.

import { readdir, readFile, writeFile } from 'node:fs/promises'
import { existsSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { fileURLToPath } from 'node:url'
import path from 'node:path'

const DIST = fileURLToPath(new URL('../dist', import.meta.url))

/** The meta Astro emits. Attribute order and the unescaped `'` are its own; matched, not assumed. */
const CSP_META = /<meta http-equiv="content-security-policy" content="([^"]*)"\s*\/?>/i
const CHARSET_META = /<meta charset="[^"]*"\s*\/?>/i
const HEAD_OPEN = /<head(?:\s[^>]*)?>/i

/** Inline `<script>` / `<style>` elements: an element with a `src` is governed by `'self'`. */
const SCRIPT_EL = /<script([^>]*)>([\s\S]*?)<\/script>/gi
const STYLE_EL = /<style([^>]*)>([\s\S]*?)<\/style>/gi

const sha256 = (body) => `'sha256-${createHash('sha256').update(body, 'utf8').digest('base64')}'`

/**
 * Hashes of every inline script and style in a document, in first-appearance order.
 * Duplicate bodies (Starlight emits `updatePickers()` twice) collapse to one hash.
 */
export function inlineHashes(html) {
  const scripts = new Set()
  const styles = new Set()
  for (const m of html.matchAll(SCRIPT_EL)) {
    if (/\ssrc\s*=/i.test(m[1])) continue
    scripts.add(sha256(m[2]))
  }
  for (const m of html.matchAll(STYLE_EL)) styles.add(sha256(m[2]))
  return { scripts: [...scripts], styles: [...styles] }
}

/** `a 'self' 'sha256-x'; b 'none'` -> [['a', ["'self'", "'sha256-x'"]], ['b', ["'none'"]]] */
export function parseDirectives(content) {
  const out = []
  for (const part of content.split(';')) {
    const trimmed = part.trim()
    if (!trimmed) continue
    const [name, ...values] = trimmed.split(/\s+/)
    out.push([name.toLowerCase(), values])
  }
  return out
}

export function serializeDirectives(directives) {
  return directives.map(([name, values]) => [name, ...values].join(' ')).join('; ')
}

function duplicateDirectiveNames(directives) {
  const seen = new Set()
  const dups = []
  for (const [name] of directives) {
    if (seen.has(name)) {
      if (!dups.includes(name)) dups.push(name)
    } else {
      seen.add(name)
    }
  }
  return dups
}

const isHash = (token) => /^'sha(?:256|384|512)-/.test(token)

/**
 * Replaces the hash tokens of `directive` with `hashes`, keeping its other sources in place.
 * Returns null when the directive is absent — the caller decides whether that is fatal.
 */
function reseal(directives, name, hashes) {
  const entry = directives.find(([n]) => n === name)
  if (!entry) return null
  const keep = entry[1].filter((token) => !isHash(token))
  entry[1] = [...keep, ...hashes]
  return entry
}

/**
 * Inserts `metaTag` at the front of `<head>`, so the policy governs the whole document.
 * `html` must already have the element Astro emitted removed.
 */
function hoist(without, metaTag) {
  const charset = CHARSET_META.exec(without)
  if (charset) {
    const at = charset.index + charset[0].length
    return { html: without.slice(0, at) + metaTag + without.slice(at), anchor: 'charset' }
  }
  const head = HEAD_OPEN.exec(without)
  if (head) {
    const at = head.index + head[0].length
    return { html: without.slice(0, at) + metaTag + without.slice(at), anchor: 'head' }
  }
  return { html: null, anchor: null }
}

/** Seals one document. Returns the new HTML plus what changed, or an `error` describing why not. */
export function sealDocument(html) {
  const found = CSP_META.exec(html)
  if (!found) return { error: 'no Content-Security-Policy meta element' }

  const directives = parseDirectives(found[1])
  const dups = duplicateDirectiveNames(directives)
  if (dups.length) {
    return {
      error:
        `duplicate CSP directive(s): ${dups.join(', ')} — browsers honour the first occurrence ` +
        'and ignore the rest; refusing to reseal one copy',
    }
  }
  const { scripts, styles } = inlineHashes(html)

  const before = serializeDirectives(directives)
  if (!reseal(directives, 'script-src', scripts)) return { error: "the policy has no `script-src` directive" }
  if (!reseal(directives, 'style-src', styles)) return { error: "the policy has no `style-src` directive" }
  const content = serializeDirectives(directives)

  const metaTag = `<meta http-equiv="content-security-policy" content="${content}">`
  // Remove the element Astro placed mid-head and re-insert the resealed one at the front.
  const hoisted = hoist(html.replace(found[0], ''), metaTag)
  if (hoisted.html === null) return { error: 'no <head> to carry the policy' }

  return {
    html: hoisted.html,
    rewritten: content !== before,
    moved: hoisted.html.indexOf(metaTag) !== found.index,
    scripts: scripts.length,
    styles: styles.length,
    anchor: hoisted.anchor,
  }
}

async function htmlFiles(dir, acc = []) {
  for (const entry of await readdir(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name)
    if (entry.isDirectory()) await htmlFiles(full, acc)
    else if (entry.name.endsWith('.html')) acc.push(full)
  }
  return acc
}

async function main() {
  if (!existsSync(DIST)) {
    console.error('seal-csp: COULD NOT LOOK — dist/ does not exist. Run `astro build` first.')
    process.exit(2)
  }
  const files = await htmlFiles(DIST)
  if (files.length === 0) {
    console.error('seal-csp: COULD NOT LOOK — dist/ contains no HTML.')
    process.exit(2)
  }

  const failures = []
  let rewritten = 0
  let moved = 0
  let scriptHashes = 0
  let styleHashes = 0

  for (const file of files) {
    const html = await readFile(file, 'utf8')
    const result = sealDocument(html)
    if (result.error) {
      failures.push(`${path.relative(DIST, file)}: ${result.error}`)
      continue
    }
    if (result.rewritten) rewritten++
    if (result.moved) moved++
    scriptHashes += result.scripts
    styleHashes += result.styles
    // Rewrite unconditionally: hoisting alone changes the bytes even when the hashes already matched.
    await writeFile(file, result.html)
  }

  if (failures.length) {
    console.error(`✗ seal-csp: ${failures.length} of ${files.length} page(s) carry no sealable policy.`)
    for (const f of failures.slice(0, 5)) console.error(`  - ${f}`)
    if (failures.length > 5) console.error(`  … and ${failures.length - 5} more`)
    process.exit(1)
  }

  console.log(
    `✓ seal-csp: ${files.length} page(s) sealed — ${rewritten} policy(ies) rewritten, ${moved} hoisted; ` +
      `${scriptHashes} script and ${styleHashes} style hashes derived from the shipped bytes.`,
  )
  process.exit(0)
}

const invokedAsCli =
  Boolean(process.argv[1]) && fileURLToPath(import.meta.url) === path.resolve(process.argv[1])
if (invokedAsCli) await main()
