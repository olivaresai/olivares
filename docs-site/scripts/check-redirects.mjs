#!/usr/bin/env node
// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// test:redirects — every URL the built site itself offers answers 200, or one 301 to a 200 page.
//
// The language and version pickers and the hreflang alternates of every generated REST page list a
// copy per locale and per snapshot that was never built, and the version picker does the same for
// each page the 2026-06 snapshot lacks. Search Console reported those as 404s on 2026-10-20. A
// published URL keeps working, so each one gets a 301 to the page that exists
// (scripts/gen-redirects.mjs writes them into public/_redirects).
//
// This reads dist/, the tree that is deployed. Its model of _redirects is the two forms the file
// uses: an exact path and a `/prefix/*` splat with `:splat` in the target. Anything else is a rule
// this check cannot read, and it says so instead of passing. The order and the limits are the
// platform parser's own (see the comment above the order check): this file once passed 449 rules
// that wrangler dev honored only up to the 100th.
//
//   0  clean · 1  finding · 2  could not look

import { readdir, readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { PUBLISHED_LOCALES } from '../src/site-locales.mjs'

const DIST = fileURLToPath(new URL('../dist', import.meta.url))
const HOST = 'https://docs.olivares.ai'
// Workers static assets: https://developers.cloudflare.com/workers/static-assets/redirects/
const MAX_STATIC = 2000
const MAX_DYNAMIC = 100
const MAX_RULE_CHARS = 1000

async function* walk(dir) {
  for (const e of await readdir(dir, { withFileTypes: true })) {
    const p = `${dir}/${e.name}`
    if (e.isDirectory()) yield* walk(p)
    else yield p
  }
}

function parseRules(text) {
  const rules = []
  for (const raw of text.split('\n')) {
    const line = raw.trim()
    if (!line || line.startsWith('#')) continue
    const [src, dst, status] = line.split(/\s+/)
    if (!src || !dst || !status || /:(?!splat\b)\w/.test(src + dst) || (src.includes('*') && !src.endsWith('/*'))) {
      throw new Error(`cannot read the _redirects rule: ${line}`)
    }
    rules.push({ src, dst, status: Number(status), splat: src.endsWith('/*'), len: line.length })
  }
  return rules
}

/** The rule's target for this URL, or null when the rule does not match it. */
function applyRule(rule, url) {
  if (!rule.splat) return rule.src === url ? rule.dst : null
  const prefix = rule.src.slice(0, -1)
  return url.startsWith(prefix) ? rule.dst.replace(':splat', url.slice(prefix.length)) : null
}

async function main() {
  const pages = new Set()
  const offered = new Map() // URL a built page offers -> the first page offering it
  const files = []
  for await (const f of walk(DIST)) files.push(f)
  for (const f of files) {
    if (f.endsWith('/index.html')) pages.add(f.slice(DIST.length, -'index.html'.length))
  }
  for (const f of files) {
    if (!f.endsWith('.html') || f.endsWith('/404.html')) continue
    const html = await readFile(f, 'utf8')
    for (const [, v] of html.matchAll(/\b(?:href|value|content)="([^"]*)"/g)) {
      const path = (v.startsWith(`${HOST}/`) ? v.slice(HOST.length) : v).replace(/[?#].*$/, '')
      if (path.startsWith('/') && !path.startsWith('//') && path.endsWith('/') && !offered.has(path)) {
        offered.set(path, f.slice(DIST.length))
      }
    }
  }

  const rules = parseRules(await readFile(`${DIST}/_redirects`, 'utf8'))
  const findings = []
  const lookup = (url) => {
    for (const r of rules) {
      const to = applyRule(r, url)
      if (to !== null) return { r, to }
    }
    return null
  }

  for (const [url, from] of offered) {
    if (pages.has(url)) continue
    const hit = lookup(url)
    if (!hit) findings.push(`${url} answers 404 (offered by ${from})`)
    else if (hit.r.status !== 301) findings.push(`${url} answers ${hit.r.status}, not 301`)
    else if (!pages.has(hit.to)) findings.push(`${url} redirects to ${hit.to}, which is not a page`)
  }
  for (const url of pages) {
    const hit = lookup(url)
    if (hit) findings.push(`the rule ${hit.r.src} would redirect the built page ${url}`)
  }
  for (const l of PUBLISHED_LOCALES) {
    if (!rules.some((r) => !r.splat && r.src === `/${l}` && r.dst === `/${l}/` && r.status === 301)) {
      findings.push(`/${l} has no 301 rule to /${l}/`)
    }
  }
  // The platform's parser (workers-shared parseRedirects, which wrangler dev runs too) counts every
  // rule after the FIRST splat as dynamic, wildcard or not, and drops everything past the 100th
  // dynamic rule. Measured with wrangler dev on 2026-11-02: with `/cli/*` on the first line, the
  // 101st rule and every one after it answered 404.
  const firstSplat = rules.findIndex((r) => r.splat)
  const statics = firstSplat === -1 ? rules.length : firstSplat
  const lateStatics = rules.slice(Math.max(firstSplat, 0)).filter((r) => !r.splat)
  if (firstSplat !== -1 && lateStatics.length) {
    findings.push(`${lateStatics.length} exact rules come after the first splat rule (${rules[firstSplat].src}) and count as dynamic; first: ${lateStatics[0].src}`)
  }
  if (statics > MAX_STATIC) findings.push(`${statics} exact rules exceed the limit of ${MAX_STATIC}`)
  if (firstSplat !== -1 && rules.length - firstSplat > MAX_DYNAMIC) {
    findings.push(`${rules.length - firstSplat} rules from the first splat on exceed the limit of ${MAX_DYNAMIC}, the rest are ignored`)
  }
  const seen = new Set()
  for (const r of rules) {
    if (seen.has(r.src)) findings.push(`duplicate rule for ${r.src}, the platform ignores the second`)
    seen.add(r.src)
    if (r.len > MAX_RULE_CHARS) findings.push(`a rule of ${r.len} characters exceeds ${MAX_RULE_CHARS}: ${r.src}`)
  }

  if (findings.length) {
    console.error(`✗ check-redirects: ${findings.length} finding(s), first ${Math.min(10, findings.length)}:`)
    for (const f of findings.slice(0, 10)) console.error(`  ${f}`)
    console.error('  fix: node scripts/gen-redirects.mjs, commit public/_redirects, rebuild')
    process.exit(1)
  }
  console.log(`✓ check-redirects: ${offered.size} URLs offered by ${pages.size} built pages resolve (${rules.length} rules)`)
}

main().catch((e) => {
  console.error(`check-redirects: could not look: ${e.message}`)
  process.exit(2)
})
