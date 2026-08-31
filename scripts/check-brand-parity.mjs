#!/usr/bin/env node
// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// check-brand-parity.mjs — one brand, two surfaces.
//
// THE DEFECT THIS CLOSES. The operations console generates its palette from DTCG
// sources (web/tokens/*.tokens.json → tokens/build.mjs → src/styles/tokens.css,
// guarded by `pnpm tokens:check`). The public website (a sibling checkout of the
// website repo) hand-maintains the same brand in the Tailwind 4 `@theme`
// block + `light-dark()` re-declarations of src/styles/global.css. On 2026-08-01
// they agreed (#f08000/#b45500 orange, #28282b/#fafaf9 neutrals) — but NOTHING
// verified they stay agreed, and a drift is exactly the kind users see: the
// console and the marketing site wearing two different brands.
//
// DESIGN (deny-closed with two repos that cannot see each other in CI):
//   1. This script GENERATES a canonical brand manifest (brand.manifest.json at
//      the repo root — anywhere under web/ perturbs the embedded-console build
//      hashes and turns web:check red) from the DTCG sources with --write, and in
//      check mode FAILS if the committed manifest no longer matches the sources.
//      Runs in this repo's gate unconditionally — no other tree needed.
//   2. The website vendors a byte-identical copy of the manifest at
//      src/data/hub/brand-tokens.json (refreshed by its `npm run sync:hub`, the
//      established hub→site vendoring pattern) and its tests/brand-parity.test.ts
//      checks the LIVE global.css against that copy on every site CI run — no hub
//      tree needed there.
//   3. When both trees ARE present (the dev workspace, where every push runs the
//      pre-push gate), this script additionally does the LIVE cross-repo check:
//      parses the site's global.css and compares every mapped value, and verifies
//      the vendored manifest is byte-identical to the canonical one.
//   So even where the cross-repo step cannot run (either repo's remote CI), both
//   sides stay pinned to the SAME committed manifest by their own gates: the two
//   brands cannot drift apart without one gate going red. A skip here is loud and
//   is not a hole — the per-repo pins still hold.
//
// Self-battery: `--selftest` synthesizes hub and site fixtures, flips one hex per
// case and asserts the red cases are RED and the green cases are green. It runs
// in the gate next to the check (the lint:boundary pattern): a detector nobody
// has seen fail is not a detector.
//
// Usage:
//   node scripts/check-brand-parity.mjs             # check (gate mode)
//   node scripts/check-brand-parity.mjs --write     # regenerate the manifest
//   node scripts/check-brand-parity.mjs --selftest  # fixture battery
//   OLIVARES_WEB_DIR=<dir>  explicit website tree (dangling pointer = failure)

import { execFileSync } from 'node:child_process'
import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const SCRIPT_DIR = dirname(fileURLToPath(import.meta.url))
const DEFAULT_ROOT = resolve(SCRIPT_DIR, '..')
const MANIFEST_REL = 'brand.manifest.json'
const WEB_CSS_REL = 'src/styles/global.css'
const WEB_VENDORED_REL = 'src/data/hub/brand-tokens.json'

// --- THE MAPPING (the contract lives here, in the job) -----------------------
// Console side: DTCG token path inside web/tokens/<file> (values verbatim, so no
// Style Dictionary run is needed to read them). Website side: CSS custom property
// in src/styles/global.css, and WHICH arm carries the value:
//   dark  → the `@theme` literal (the site is dark-first; @theme must keep plain
//           dark literals as the pre-light-dark() browser fallback) AND, when the
//           var is re-declared as light-dark(light, dark) in @layer base, its
//           SECOND argument.
//   light → the FIRST argument of that light-dark() re-declaration — or, when the
//           var carries NO light-dark(), the value it is plainly declared with,
//           because that is then what the site renders in BOTH themes.
//
// THAT LAST CLAUSE IS NOT A LOOSENING, AND IS WHY IT EXISTS. Until then the
// light arm was found by SYNTAX ("there must be a light-dark()"), not by value, and
// a brand value that is deliberately the SAME in both themes has no light-dark() to
// find. order of 2026-08-06 creates exactly that on both surfaces: the orange
// FILL and the ink on it are now theme-independent by design (the ink sits ON the
// fill, so the deepening AA needs for TEXT buys nothing there). Measured 2026-08-09,
// the old rule reported `--color-on-primary: no light-dark() re-declaration` against
// a site that renders #1a1206 in light — the correct value. A gate that calls a
// correct site broken teaches lanes to write `light-dark(x, x)` into the brand
// stylesheet to satisfy a parser, which is a workaround masquerading as a palette.
// Comparing the RENDERED value is also strictly stricter: a var that lost its light
// arm by accident still goes red, now because the value is wrong rather than because
// the syntax is missing — and it says so.
//
// WHAT IS IN: the brand anchors both surfaces must share (the single orange in
// its two AA-tuned values, the two brand neutrals in both roles, ink-on-orange)
// plus the LIGHT theme set that global.css itself documents as mirroring the
// shipping console tokens ("the light values are NOT invented here", §LIGHT
// THEME) — surfaces, borders, muted text, semantic status, accent hover.
//
// WHAT IS OUT, deliberately:
//   * the site's DARK surfaces/semantics (--color-surface #343434, --color-ok
//     #9cc79c, …): global.css documents them as intentionally re-tuned for the
//     warm public dark palette; the console dark set (#2f2f33, #86c58a, …) is a
//     different, also-intentional tuning. Pinning them equal would be inventing
//     a rule neither repo claims.
//   * fonts: same families, but the console self-hosts the Variable builds via
//     @fontsource-variable and the site ships static-weight woff2 — the strings
//     legitimately differ.
//   * graphite-ramp coincidences (site --color-ink-soft == graphite-800 etc.):
//     numeric accidents, not declared contracts.
const BRAND_MAP = [
  // Brand anchors — DARK (the site's default @theme literals).
  entry('orange-dark', 'theme.dark.tokens.json', 'color.accent', '--color-primary', 'dark'),
  entry('on-orange-dark', 'theme.dark.tokens.json', 'color.accent-foreground', '--color-on-primary', 'dark'),
  entry('canvas-dark', 'theme.dark.tokens.json', 'color.background', '--color-bg', 'dark'),
  entry('text-on-dark', 'theme.dark.tokens.json', 'color.foreground', '--color-text', 'dark'),
  // Brand anchors — LIGHT. The orange is split BY ROLE on both surfaces since 
  // so the anchors follow the role and not the name: the FILL is the brand orange
  // itself in light too (console color.accent == site --color-primary-fill == #f08000,
  // the ink rides on top at 6.88:1), while the orange as TEXT stays deepened, because
  // #f08000 on #fafaf9 is 2.58:1 and fails AA for real (see orange-text-light below).
  // Pointing this entry at --color-primary would now compare the console's FILL to the
  // site's TEXT tone and demand one of the two be wrong.
  entry('orange-light', 'theme.light.tokens.json', 'color.accent', '--color-primary-fill', 'light'),
  entry('on-orange-light', 'theme.light.tokens.json', 'color.accent-foreground', '--color-on-primary', 'light'),
  entry('canvas-light', 'theme.light.tokens.json', 'color.background', '--color-bg', 'light'),
  entry('text-on-light', 'theme.light.tokens.json', 'color.foreground', '--color-text', 'light'),
  // The mirrored LIGHT set (site light theme == console light tokens, by the
  // site's own declaration).
  //
  // WHAT CHANGED IN AND WHY IT IS A REPAIR AND NOT A RELAXATION. This map used
  // to pin the console's FILL HOVER (color.accent-hover) to the site's
  // --color-primary-soft. Once both surfaces split the orange by role, those stopped
  // being the same thing: --color-primary-soft is a TEXT tone on the site (links,
  // callouts — its CTA has no fill-hover token at all, it uses a brightness filter),
  // while color.accent-hover is the console button's hover FILL, whose only real
  // constraint is that --accent-foreground stay readable ON it. Holding them equal
  // forced the console to keep #9a4800, where the new dark ink measures 2.91:1.
  // Pinning two roles together because they once shared a hex is how a gate starts
  // dictating a defect, so that entry is gone and a stronger one takes its place:
  // the orange as TEXT, which is the value order is actually about keeping
  // deepened. The count stays at 17, which the site's own plausibility assert floors.
  entry('orange-text-light', 'theme.light.tokens.json', 'color.accent-text', '--color-primary', 'light'),
  entry('surface-light', 'theme.light.tokens.json', 'color.surface', '--color-surface', 'light'),
  entry('border-light', 'theme.light.tokens.json', 'color.border', '--color-border', 'light'),
  entry('border-strong-light', 'theme.light.tokens.json', 'color.border-strong', '--color-border-strong', 'light'),
  entry('text-muted-light', 'theme.light.tokens.json', 'color.muted-foreground', '--color-text-muted', 'light'),
  entry('ok-light', 'theme.light.tokens.json', 'color.success', '--color-ok', 'light'),
  entry('warn-light', 'theme.light.tokens.json', 'color.warning', '--color-warn', 'light'),
  entry('danger-light', 'theme.light.tokens.json', 'color.danger', '--color-danger', 'light'),
  entry('info-light', 'theme.light.tokens.json', 'color.info', '--color-info', 'light'),
]

function entry(id, hubFile, hubPath, webVar, webTheme) {
  return { id, hubFile, hubPath, webVar, webTheme }
}

// --- manifest ---------------------------------------------------------------

function buildManifest(root) {
  const cache = new Map()
  const dtcg = (file) => {
    if (!cache.has(file)) {
      cache.set(file, JSON.parse(readFileSync(join(root, 'web/tokens', file), 'utf8')))
    }
    return cache.get(file)
  }
  const entries = BRAND_MAP.map((e) => {
    let node = dtcg(e.hubFile)
    for (const seg of e.hubPath.split('.')) {
      node = node?.[seg]
      if (node === undefined) {
        throw new Error(`brand-parity: token ${e.hubPath} not found in web/tokens/${e.hubFile}`)
      }
    }
    const value = node.$value
    if (typeof value !== 'string' || value.trim() === '') {
      throw new Error(`brand-parity: token ${e.hubPath} in ${e.hubFile} has no string $value`)
    }
    return { ...e, value }
  })
  return {
    $description:
      'GENERATED — DO NOT EDIT (node scripts/check-brand-parity.mjs --write). Canonical Olivares brand-token manifest: the shared subset of the brand declared by the console DTCG sources (web/tokens/*.tokens.json — the single source of truth) and mirrored by the stylesheet of the olivares.ai website. `task lint:brand-parity` pins this file to the DTCG sources on every push; the website vendors a byte-identical copy and pins its live palette to it in its own CI — the two surfaces cannot drift apart without one of the two gates going red.',
    brand: 'brandv4 (2026-06-10)',
    entries,
  }
}

const stringifyManifest = (manifest) => `${JSON.stringify(manifest, null, 2)}\n`

// --- website CSS parsing ----------------------------------------------------

const stripComments = (css) => css.replace(/\/\*[\s\S]*?\*\//g, '')
const norm = (v) => v.replace(/\s+/g, ' ').trim().toLowerCase()

/** Custom-property literals inside the FIRST `@theme {}` block (dark-first defaults). */
function parseThemeLiterals(css) {
  const map = new Map()
  const at = css.search(/@theme\b/)
  if (at === -1) return map
  const open = css.indexOf('{', at)
  if (open === -1) return map
  let depth = 0
  let end = -1
  for (let i = open; i < css.length; i++) {
    if (css[i] === '{') depth++
    else if (css[i] === '}') {
      depth--
      if (depth === 0) {
        end = i
        break
      }
    }
  }
  if (end === -1) return map
  const body = css.slice(open + 1, end)
  for (const m of body.matchAll(/--([A-Za-z0-9-]+)\s*:\s*([^;{}]+);/g)) {
    map.set(`--${m[1]}`, m[2])
  }
  return map
}

/**
 * PLAIN (non-light-dark) custom properties that the document root really renders.
 *
 * DENY-CLOSED ON THE CONTEXT, and the first version of this was not, which is the whole
 * lesson. It matched `--x: v;` anywhere in the sheet and let the last one win, calling that
 * "the cascade". It is not: the cascade only ranks declarations that APPLY. Built as a decoy
 * and measured 2026-08-09 — an applicable `@theme` saying `--color-primary-fill: #000000`
 * plus a trailing `.never-applies-anywhere { --color-primary-fill: #f08000; }` turned the gate
 * from a correct red into "17 shared brand tokens agree". A gate that a dead rule can satisfy
 * is worse than no gate, because it is trusted.
 *
 * So the context is proven, not assumed. A declaration counts only if EVERY enclosing block is
 * one of: `@theme` (Tailwind emits it into :root), a plain `@layer <name>` (layers reorder, they
 * do not restrict matching), or a selector list whose every part is `:root` or `html`. Anything
 * else — a class, a `@media`, a `@supports`, a shape this parser does not recognise — is not
 * evidence, and the var reads as undeclared, which is RED. The failure direction is the safe one.
 *
 * @layer base wins over @theme (Tailwind orders theme before base), so a root re-declaration
 * overrides the @theme literal, which is exactly how the site's own light-dark() arms behave.
 *
 * AND DENY-CLOSED ON AMBIGUITY, which is the second half and cost a second round. Proving the
 * context is not enough: three of the four counterexamples the contrast pass built next used
 * declarations that DO apply, and beat each other by rules this file has no business
 * implementing — `:root` outranks `html` by specificity, an unlayered rule outranks a layered
 * one, `!important` outranks everything. Each produced "17 shared brand tokens agree" while a
 * browser rendered #000000. Writing a CSS cascade here to close them would be the wrong repair:
 * the gate would then be asserting a resolution it is not built to get right, and the next
 * counterexample would be the one nobody thought of.
 *
 * So it refuses instead. Declarations are ranked ONLY by the one axis that is unambiguous here
 * (unlayered > layered > @theme). If the winning rank holds more than one distinct value, or any
 * `!important` competes, the var resolves to AMBIGUOUS and the check reports that it cannot
 * determine what the site renders — which is red, and says why. A brand stylesheet declaring one
 * anchor twice, two ways, is a thing to fix in the stylesheet, not to adjudicate in a linter.
 */
function parseRootDeclarations(css) {
  const found = new Map() // name -> [{ rank, value, important, lightDark }]
  // Vars that carry a light-dark() this parser REFUSED for context. Without this, a var whose
  // only accepted declaration is the @theme fallback compares clean against that fallback and
  // reports a brand divergence that does not exist. Measured while writing this: tightening the
  // context rule made every light entry resolve to the DARK literal, and the gate printed
  // ELEVEN confident findings ("site has #28282b, console has #fafaf9") that were nothing but
  // its own blindness. A false RED reads exactly like a real one — silence would have been more
  // honest, and "I could not look" is more honest still.
  const rejectedLightDark = new Set()
  const isRootSelector = (sel) =>
    sel
      .split(',')
      .every((part) => ['-root-', 'html'].includes(part.trim().replace(':root', '-root-')))
  const stack = []
  let prelude = ''
  const flush = () => {
    const decl = prelude.trim()
    prelude = ''
    const m = /^--([A-Za-z0-9-]+)\s*:\s*([\s\S]+)$/.exec(decl)
    if (!m) return
    let theme = false
    let layered = false
    for (const ctx of stack) {
      if (/^@theme\b/.test(ctx)) theme = true
      else if (/^@layer\b/.test(ctx)) layered = true
      // The ONE conditional group this gate can reason about, and it is not a loophole: a
      // `@supports (color: light-dark(...))` is true in exactly the engines that honour a
      // light-dark() value, so declarations inside it are the ones that decide light vs dark.
      // The site wraps its whole light-dark palette in precisely this guard (global.css §LIGHT
      // THEME) so an engine without the function degrades to the @theme dark literals instead
      // of to initial values. Rejecting it would make the gate blind to the real palette — which
      // it briefly was, and every light entry then resolved to the DARK fallback. Any OTHER
      // @supports, and every @media/@container, stays not-evidence.
      else if (/^@supports\s*\(\s*color\s*:\s*light-dark\s*\(/i.test(ctx)) continue
      else if (ctx.startsWith('@') || !isRootSelector(ctx)) {
        if (/^light-dark\s*\(/.test(m[2].trim())) rejectedLightDark.add(`--${m[1]}`)
        return // not root-applicable
      }
    }
    if (stack.length === 0) return
    let value = m[2].trim()
    const important = /!\s*important\s*$/i.test(value)
    if (important) value = value.replace(/!\s*important\s*$/i, '').trim()
    const name = `--${m[1]}`
    if (!found.has(name)) found.set(name, [])
    found.get(name).push({
      rank: theme ? 0 : layered ? 1 : 2,
      value,
      important,
      lightDark: /^light-dark\s*\(/.test(value),
    })
  }
  for (let i = 0; i < css.length; i++) {
    const c = css[i]
    if (c === '{') {
      stack.push(prelude.trim())
      prelude = ''
    } else if (c === '}') {
      stack.pop()
      prelude = ''
    } else if (c === ';') flush()
    else prelude += c
  }
  /** → {value, lightDark} | {ambiguous: reason} | undefined */
  const resolve = (name) => {
    const all = found.get(name)
    if (!all || all.length === 0) return undefined
    const top = Math.max(...all.map((d) => d.rank))
    const winners = all.filter((d) => d.rank === top)
    const importantOnes = all.filter((d) => d.important)
    if (importantOnes.length > 0 && all.length > 1) {
      return { ambiguous: `${all.length} root declarations and one is !important` }
    }
    const distinct = [...new Set(winners.map((d) => norm(d.value)))]
    if (distinct.length > 1) {
      return {
        ambiguous: `declared ${winners.length} times at the same level with ${distinct.length} different values (${winners.map((d) => d.value.trim()).join(' / ')})`,
      }
    }
    if (!winners[0].lightDark && rejectedLightDark.has(name)) {
      return {
        ambiguous:
          'declares a light-dark() in a context this gate cannot evaluate, so the only value it ' +
          'can read is the fallback — comparing that would report a drift that is not there',
      }
    }
    return { value: winners[0].value, lightDark: winners[0].lightDark }
  }
  return { resolve }
}

/** Split a `light-dark(a, b)` value into its two arms (paren-aware). Null if not that shape. */
function splitLightDark(value) {
  const open = value.indexOf('(')
  if (open === -1) return null
  let depth = 1
  const args = ['']
  for (let i = open + 1; i < value.length && depth > 0; i++) {
    const c = value[i]
    if (c === '(') depth++
    else if (c === ')') {
      depth--
      if (depth === 0) break
    }
    if (depth === 1 && c === ',') {
      args.push('')
      continue
    }
    args[args.length - 1] += c
  }
  return args.length === 2 ? { light: args[0], dark: args[1] } : null
}

// --- checks -----------------------------------------------------------------

/** Committed manifest must match what the DTCG sources generate. */
function checkHub(root) {
  const errors = []
  const want = stringifyManifest(buildManifest(root))
  const path = join(root, MANIFEST_REL)
  if (!existsSync(path)) {
    errors.push(`${MANIFEST_REL} is missing — run \`node scripts/check-brand-parity.mjs --write\` and commit it`)
  } else if (readFileSync(path, 'utf8') !== want) {
    errors.push(
      `${MANIFEST_REL} is stale (or hand-edited): it no longer matches the DTCG sources under web/tokens/. ` +
        'Run `node scripts/check-brand-parity.mjs --write`, commit, and refresh the website copy (`npm run sync:hub` there).',
    )
  }
  return errors
}

/** Live cross-repo check of a website tree against the manifest. */
function checkWebTree(manifest, webDir) {
  const errors = []
  const notes = []
  const cssPath = join(webDir, WEB_CSS_REL)
  if (!existsSync(cssPath)) {
    errors.push(`website tree ${webDir} has no ${WEB_CSS_REL} — refusing to call that parity`)
    return { errors, notes }
  }
  const css = stripComments(readFileSync(cssPath, 'utf8'))
  const literals = parseThemeLiterals(css)
  const decls = parseRootDeclarations(css)
  for (const e of manifest.entries) {
    const want = norm(e.value)
    const resolved = decls.resolve(e.webVar)
    if (resolved?.ambiguous) {
      // Not a drift and not a pass: the sheet does not say one thing. Reported once per entry,
      // for both arms, because neither can be judged from an unresolved declaration.
      errors.push(
        `${e.webVar}: ${WEB_CSS_REL} ${resolved.ambiguous} — this gate does not adjudicate the ` +
          `CSS cascade, so it cannot say what the site renders (expected ${e.value}; ${e.id})`,
      )
      continue
    }
    const ld = resolved?.lightDark ? splitLightDark(resolved.value) : null
    if (e.webTheme === 'dark') {
      const lit = literals.get(e.webVar)
      if (lit === undefined) {
        errors.push(`${e.webVar}: not declared in the @theme block of ${WEB_CSS_REL} (expected ${e.value}; ${e.id})`)
      } else if (norm(lit) !== want) {
        errors.push(
          `${e.webVar} (@theme dark literal): site has ${lit.trim()}, console has ${e.value} (${e.hubFile} ${e.hubPath}; ${e.id})`,
        )
      }
      if (ld && norm(ld.dark) !== want) {
        errors.push(
          `${e.webVar} (light-dark() dark arm): site has ${ld.dark.trim()}, console has ${e.value} (${e.hubFile} ${e.hubPath}; ${e.id})`,
        )
      }
    } else {
      // What does the site RENDER in light theme? The light-dark() first arm when
      // there is one; otherwise the plain declaration, which applies to both themes.
      const got = ld ? ld.light : resolved?.value
      const via = ld ? 'light-dark() light arm' : 'theme-independent declaration'
      if (got === undefined) {
        errors.push(
          `${e.webVar}: not declared at all in ${WEB_CSS_REL} (expected light value ${e.value}; ${e.id})`,
        )
      } else if (norm(got) !== want) {
        errors.push(
          `${e.webVar} (${via}): site has ${got.trim()}, console has ${e.value} (${e.hubFile} ${e.hubPath}; ${e.id})`,
        )
      }
    }
  }
  const vendored = join(webDir, WEB_VENDORED_REL)
  if (!existsSync(vendored)) {
    notes.push(
      `NOTE: ${vendored} does not exist — the website has not vendored the brand manifest yet ` +
        '(refresh it there with `npm run sync:hub` and commit the copy). Live CSS parity was still checked above.',
    )
  } else if (readFileSync(vendored, 'utf8') !== stringifyManifest(manifest)) {
    errors.push(
      `vendored manifest ${WEB_VENDORED_REL} in ${webDir} differs from the canonical ${MANIFEST_REL} — ` +
        're-run `npm run sync:hub` in the website repo and commit the refreshed copy there. ' +
        'ORDER MATTERS, and the failure mode is silent: sync:hub copies the manifest COMMITTED in a ' +
        'hub checkout (its HUB_DIR, which defaults to the sibling clone tracking main), ' +
        'not the working tree you are reading this from. Run it before the token change is on main ' +
        'and it faithfully re-copies the OLD manifest, writes a byte-identical file, leaves ' +
        '`git status` clean, and looks exactly like a command that does not touch brand-tokens.json. ' +
        'So: land the change on main first, or point HUB_DIR at a tree that has it.',
    )
  }
  return { errors, notes }
}

/**
 * Locate the website tree: explicit env (dangling = failure), else siblings.
 *
 * Returns EVERY candidate, not the first one. Measured 2026-08-09: this box carries FIVE
 * sibling checkouts of the website, and picking the alphabetically-first match made the gate
 * compare the console against `olivares.ai.web-privacy2` — neither the canonical tree nor the
 * one the lane was editing — and then print the winner's path inside a sentence that reads as
 * a finding about the brand. Sorting is not a tie-break, it is a coin toss with a stable seed:
 * the verdict depended on which clone happened to sort first, and BOTH answers were wrong in
 * the same way, because a green there is as unearned as a red.
 *
 * Ambiguity therefore joins stale and dirty in the third answer this file already knows how to
 * give — COULD NOT LOOK, said out loud with the fix — instead of a guess wearing a verdict's
 * clothes. The per-repo pins still hold, so it is not a hole; and OLIVARES_WEB_DIR remains the
 * way to say which tree you mean, which is exactly what an ambiguous box should force you to.
 */
function discoverWebDir(root) {
  const env = process.env.OLIVARES_WEB_DIR
  if (env !== undefined && env !== '') {
    const p = resolve(env)
    if (!existsSync(p)) {
      console.error(`brand-parity: OLIVARES_WEB_DIR=${env} does not exist — an explicit pointer that dangles is a misconfiguration, not a skip.`)
      process.exit(1)
    }
    return [p]
  }
  // Discover the website checkout by CONTENT, not by name: any sibling of this tree
  // (or of the main checkout, when running from a linked worktree) that carries the
  // website's global stylesheet. Naming the private dev checkout here would leak it
  // into the public export.
  const parents = [resolve(root, '..')]
  try {
    // From a linked worktree, the sibling of the MAIN checkout is the one that exists.
    const common = execFileSync(
      'git',
      ['-C', root, 'rev-parse', '--path-format=absolute', '--git-common-dir'],
      { encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'] },
    ).trim()
    if (common) parents.push(resolve(common, '..', '..'))
  } catch {
    /* non-git tree (release tarball): sibling candidate only */
  }
  const found = new Set()
  for (const parent of parents) {
    let names = []
    try { names = readdirSync(parent) } catch { continue }
    for (const name of names.sort()) {
      const c = join(parent, name)
      if (resolve(c) !== resolve(root) && existsSync(join(c, WEB_CSS_REL))) found.add(resolve(c))
    }
  }
  return [...found]
}

/**
 * Is this website checkout usable as EVIDENCE about the live site?
 *
 * Measured 2026-08-02: a lane pushing to the hub was forced to --no-verify because this gate went
 * red over a vendored manifest in a SIBLING checkout that was 27 commits behind its own origin.
 * The disagreement was real in the bytes and meaningless as a fact: the clone was simply old. A
 * gate that reports "the console and the website disagree" when the only measurable difference is
 * the age of somebody's working copy blames the wrong culprit — and the pusher cannot fix it by
 * changing their own diff.
 *
 * So a stale or dirty checkout is treated the same way as an ABSENT one: not evidence, and said
 * out loud with the reason and the fix. That is the third answer (could not look), never a silent
 * green — the site's own CI still pins its vendored copy, so the property stays guarded there.
 * An explicit OLIVARES_WEB_DIR is exempt: pointing at a tree on purpose is a claim that it is the
 * one you mean.
 */
function webTreeUsable(dir) {
  const git = (args) => {
    // GIT_DIR/GIT_WORK_TREE MUST be cleared. A pre-push hook runs with GIT_DIR set by git, and an
    // inherited GIT_DIR beats `-C <dir>`: every question below would then be answered about the
    // repository being pushed instead of the tree we are judging. Measured 2026-08-03 — this is
    // why the two `usable=true` cases failed under the hook and passed by hand, 3 times out of 3,
    // and why three lanes reported it while nobody could reproduce it manually.
    const env = { ...process.env }
    delete env.GIT_DIR
    delete env.GIT_WORK_TREE
    delete env.GIT_INDEX_FILE
    try {
      return execFileSync('git', ['-C', dir, ...args], {
        encoding: 'utf8',
        stdio: ['ignore', 'pipe', 'ignore'],
        env,
      }).trim()
    } catch {
      return null
    }
  }
  if (git(['rev-parse', '--is-inside-work-tree']) !== 'true') return { usable: true } // tarball
  const dirty = git(['status', '--porcelain'])
  if (dirty === null) return { usable: true }
  if (dirty !== '') {
    return { usable: false, why: `has ${dirty.split('\n').length} uncommitted change(s)` }
  }
  const upstream = git(['rev-parse', '--abbrev-ref', '@{upstream}'])
  if (!upstream) return { usable: true } // no upstream to be behind of
  const behind = git(['rev-list', '--count', `HEAD..${upstream}`])
  if (behind && behind !== '0') {
    return { usable: false, why: `is ${behind} commit(s) behind ${upstream}` }
  }
  return { usable: true }
}

// --- selftest fixtures ------------------------------------------------------

const flipHex = (hex) =>
  hex.replace(/[0-9a-f](?=[^0-9a-f]*$)/i, (d) => ((parseInt(d, 16) + 1) % 16).toString(16))

/**
 * Minimal site tree in the exact shape the parser reads (mutate = entry id to corrupt).
 *
 * `sole` lists webVars the fixture declares ONCE — an @theme literal and no
 * light-dark() re-declaration — which is how a real theme-independent brand value is
 * written. It is the shape the light arm used to be unable to read.
 */
function synthesizeWebTree(dir, manifest, { mutate, sole = [], stray, wrap } = {}) {
  const value = (e) => (e.id === mutate ? flipHex(e.value) : e.value)
  const soleSet = new Set(sole)
  const byVar = new Map()
  for (const e of manifest.entries) {
    const cur = byVar.get(e.webVar) ?? {}
    cur[e.webTheme] = value(e)
    byVar.set(e.webVar, cur)
  }
  const theme = ['@theme {']
  const rootBlock = [':root {', '  color-scheme: light dark;']
  for (const [name, v] of byVar) {
    if (soleSet.has(name)) {
      // Declared once: the @theme literal is what BOTH themes render.
      theme.push(`  ${name}: ${v.dark ?? v.light};`)
      continue
    }
    theme.push(`  ${name}: ${v.dark ?? v.light};`)
    rootBlock.push(`  ${name}: light-dark(${v.light ?? v.dark}, ${v.dark ?? v.light});`)
  }
  theme.push('}')
  rootBlock.push('}')
  mkdirSync(join(dir, 'src', 'styles'), { recursive: true })
  // A trailing declaration in a context that does NOT apply to the root. It always carries
  // the CORRECT value, so if the parser ever counts it, the fixture's wrong applicable value
  // is masked and the case flips green — which is the failure this shape exists to catch.
  //
  // The last four APPLY, and beat the fixture's own declaration by rules this gate does not
  // implement — specificity, layer order, importance, and (for light-dark) context. Each one
  // returned "17 shared brand tokens agree" against a browser that renders #000000. They must
  // now come back RED, not because the gate learned the cascade but because it refuses to
  // pretend it knows: a brand anchor declared twice, two ways, is a stylesheet to fix.
  const strays = {
    class: `\n.never-applies-anywhere { --color-primary-fill: #f08000; }\n`,
    media: `\n@media (min-width: 99999px) {\n  :root { --color-primary-fill: #f08000; }\n}\n`,
    descendant: `\n.card :root { --color-primary-fill: #f08000; }\n`,
    specificity: `\n:root { --color-primary-fill: #000000; }\nhtml { --color-primary-fill: #f08000; }\n`,
    layer: `\n:root { --color-primary-fill: #000000; }\n@layer base {\n  :root { --color-primary-fill: #f08000; }\n}\n`,
    important: `\n:root { --color-primary-fill: #000000 !important; }\n:root { --color-primary-fill: #f08000; }\n`,
    'light-dark-stray': `\n:root { --color-bg: light-dark(#000000, #28282b); }\n.never { --color-bg: light-dark(#fafaf9, #28282b); }\n`,
  }
  // `wrap` puts the light-dark() block somewhere this gate cannot evaluate. The real site wraps
  // it in `@supports (color: light-dark(…))`, which IS evaluable and is accepted; a @media is
  // not, and the point of the case is that the gate must then say it cannot look rather than
  // quietly compare the @theme fallback and print a drift that is not there.
  const opener = wrap === 'media' ? '@media (min-width: 99999px) {' : '@layer base {'
  writeFileSync(
    join(dir, WEB_CSS_REL),
    `@import 'tailwindcss';\n\n${theme.join('\n')}\n\n${opener}\n${rootBlock.join('\n')}\n}\n` +
      (stray ? strays[stray] : ''),
  )
  mkdirSync(join(dir, 'src', 'data', 'hub'), { recursive: true })
  writeFileSync(join(dir, WEB_VENDORED_REL), stringifyManifest(manifest))
}

function selftest(root) {
  const manifest = buildManifest(root)
  const base = mkdtempSync(join(tmpdir(), 'brand-parity-selftest-'))
  let failures = 0
  // `wantId` is not decoration: a red case that only asserts "some error happened" passes just
  // as happily when the RED came from a different entry than the one the fixture corrupted,
  // which is how a battery keeps reporting ok while measuring nothing. Where a case names the
  // entry it broke, the message has to name it back.
  const expect = (name, errors, wantRed, wantId) => {
    const red = errors.length > 0
    if (red === wantRed && wantRed && wantId && !errors.some((e) => e.includes(wantId))) {
      failures++
      console.error(
        `brand-parity selftest: FAIL — ${name}: RED as expected, but no error mentions ${wantId} — ` +
          `the case is passing for the wrong reason (${errors.join(' | ')})`,
      )
      return
    }
    if (red === wantRed) {
      console.log(`brand-parity selftest: ok — ${name}`)
    } else {
      failures++
      console.error(
        `brand-parity selftest: FAIL — ${name}: expected ${wantRed ? 'RED' : 'GREEN'}, got ${
          red ? `RED (${errors.join(' | ')})` : 'GREEN'
        }`,
      )
    }
  }
  try {
    // Site fixtures.
    const ok = join(base, 'site-ok')
    synthesizeWebTree(ok, manifest)
    expect('faithful site is green', checkWebTree(manifest, ok).errors, false)

    const darkDrift = join(base, 'site-dark-drift')
    synthesizeWebTree(darkDrift, manifest, { mutate: 'orange-dark' })
    expect('a flipped DARK brand hex is detected', checkWebTree(manifest, darkDrift).errors, true, 'orange-dark')

    const lightDrift = join(base, 'site-light-drift')
    synthesizeWebTree(lightDrift, manifest, { mutate: 'ok-light' })
    expect('a flipped LIGHT semantic hex is detected', checkWebTree(manifest, lightDrift).errors, true, 'ok-light')

    //the theme-independent shape, in all three of its outcomes. The brand now
    // has values that are deliberately the same in both themes (the orange FILL and
    // the ink on it), written as ONE declaration with no light-dark(). The first two
    // cases are the new capability; the THIRD is the one that matters, because it is
    // the property the old syntax rule was really protecting: a var that lost its
    // light arm must still be caught. It is — by value now, not by syntax, which also
    // names what is wrong instead of only that something is missing.
    const soleOk = join(base, 'site-sole-declaration')
    synthesizeWebTree(soleOk, manifest, { sole: ['--color-on-primary', '--color-primary-fill'] })
    expect(
      'a brand value declared ONCE, at the right value, is green in both arms',
      checkWebTree(manifest, soleOk).errors,
      false,
    )

    // The drift case mutates --color-primary-fill, which is mapped by a LIGHT entry
    // and only that one, so the red can come from nowhere but the light path. Doing
    // it on --color-on-primary instead would prove nothing: a sole declaration
    // carries ONE value, the fixture emits the dark arm for it, and the flipped light
    // entry is discarded before it reaches the CSS — which is exactly how this case
    // first came back green and had to be rewritten.
    const soleDrift = join(base, 'site-sole-declaration-drift')
    synthesizeWebTree(soleDrift, manifest, { sole: ['--color-primary-fill'], mutate: 'orange-light' })
    expect(
      'a flipped hex on a declared-once brand value is detected by the light arm',
      checkWebTree(manifest, soleDrift).errors,
      true,
      'orange-light',
    )

    // A declaration the browser never applies must not be able to satisfy the gate. This is
    // the false green the first version of parseRootDeclarations shipped, found by the
    // external contrast pass and reproduced as a decoy before it was fixed: the applicable
    // value is the MUTATED (wrong) one, and only the unreachable rule carries the right hex.
    // If any of these three comes back green, the context check has stopped being deny-closed.
    for (const shape of ['class', 'media', 'descendant']) {
      const dir = join(base, `site-stray-${shape}`)
      synthesizeWebTree(dir, manifest, { sole: ['--color-primary-fill'], mutate: 'orange-light', stray: shape })
      expect(
        `a declaration under a context that never applies (${shape}) cannot satisfy the light arm`,
        checkWebTree(manifest, dir).errors,
        true,
        'orange-light',
      )
    }
    // Applicable-but-unresolvable: the gate must refuse, not guess. `sole` keeps the fixture
    // from emitting its own light-dark() for the var, so the only declarations are the ones the
    // stray adds — and the WRONG value is the one a browser would use in every case.
    for (const shape of ['specificity', 'layer', 'important']) {
      const dir = join(base, `site-conflict-${shape}`)
      synthesizeWebTree(dir, manifest, { sole: ['--color-primary-fill'], stray: shape })
      expect(
        `two applicable root declarations that disagree (${shape}) are refused, not adjudicated`,
        checkWebTree(manifest, dir).errors,
        true,
        'orange-light',
      )
    }
    // The light-dark() block itself in a context the gate cannot evaluate. It must REFUSE, and
    // the message must say so — not compare the @theme dark fallback and call the result a brand
    // divergence, which is the shape that briefly produced eleven confident false findings.
    const unreadable = join(base, 'site-lightdark-unreadable')
    synthesizeWebTree(unreadable, manifest, { wrap: 'media' })
    const unreadableErrors = checkWebTree(manifest, unreadable).errors
    expect(
      'a light-dark() block the gate cannot evaluate is refused, not compared to the fallback',
      unreadableErrors,
      true,
      'cannot evaluate',
    )
    if (unreadableErrors.some((e) => /site has|console has/.test(e))) {
      failures++
      console.error(
        'brand-parity selftest: FAIL — the unreadable-light-dark case reported a VALUE comparison; ' +
          'that is the false finding this case exists to forbid',
      )
    }

    const strayLd = join(base, 'site-conflict-light-dark')
    synthesizeWebTree(strayLd, manifest, { sole: ['--color-bg'], stray: 'light-dark-stray' })
    expect(
      'a light-dark() under a selector that never applies cannot supply the light arm',
      checkWebTree(manifest, strayLd).errors,
      true,
      'canvas-light',
    )

    const lostLightArm = join(base, 'site-lost-light-arm')
    synthesizeWebTree(lostLightArm, manifest, { sole: ['--color-bg'] })
    expect(
      'a var whose light and dark values DIFFER, collapsed to one declaration, is detected',
      checkWebTree(manifest, lostLightArm).errors,
      true,
      'canvas-light',
    )

    const vendoredDrift = join(base, 'site-vendored-drift')
    synthesizeWebTree(vendoredDrift, manifest)
    const stale = { ...manifest, brand: 'brandv3 (stale)' }
    writeFileSync(join(vendoredDrift, WEB_VENDORED_REL), stringifyManifest(stale))
    expect('a stale vendored site manifest is detected', checkWebTree(manifest, vendoredDrift).errors, true)

    // Hub fixtures: tokens + committed manifest copied, then one DTCG hex flipped.
    const hubOk = join(base, 'hub-ok')
    mkdirSync(join(hubOk, 'web', 'tokens'), { recursive: true })
    for (const f of ['theme.dark.tokens.json', 'theme.light.tokens.json']) {
      writeFileSync(join(hubOk, 'web', 'tokens', f), readFileSync(join(root, 'web/tokens', f)))
    }
    writeFileSync(join(hubOk, MANIFEST_REL), stringifyManifest(manifest))
    expect('faithful hub copy is green', checkHub(hubOk), false)

    const hubDrift = join(base, 'hub-drift')
    mkdirSync(join(hubDrift, 'web', 'tokens'), { recursive: true })
    for (const f of ['theme.dark.tokens.json', 'theme.light.tokens.json']) {
      writeFileSync(join(hubDrift, 'web', 'tokens', f), readFileSync(join(root, 'web/tokens', f)))
    }
    const dark = JSON.parse(readFileSync(join(hubDrift, 'web/tokens/theme.dark.tokens.json'), 'utf8'))
    dark.color.accent.$value = flipHex(dark.color.accent.$value)
    writeFileSync(join(hubDrift, 'web/tokens/theme.dark.tokens.json'), `${JSON.stringify(dark, null, 2)}\n`)
    writeFileSync(join(hubDrift, MANIFEST_REL), stringifyManifest(manifest))
    expect('a flipped DTCG source hex makes the committed manifest stale', checkHub(hubDrift), true)

    // Usability of the website checkout AS EVIDENCE (2026-08-02). These decide whether the
    // cross-repo comparison runs at all — the fixtures are real git repos, because the property
    // is about git state and a stub would prove nothing about the code that queries it.
    // FIXTURES THAT CANNOT ESCAPE THEIR SANDBOX (hardened the same day, after two lanes found
    // this battery writing into THEIR worktrees).
    //
    // `git -C <dir> add/commit` does NOT fail when <dir> is not a repository: git walks UP and
    // operates on the first repository it finds. So if `git init` did not take effect — a temp
    // dir that happens to live inside a checkout, an inherited GIT_DIR, a global template hook —
    // every later command lands on the ENCLOSING repo, and `add -A` there stages the whole tree
    // against a directory holding only a synthesized site. That is exactly the whole-repo
    // deletion two lanes found committed under this fixture's author name.
    //
    // Three fences, because one of them is a comment and two are checks: the environment cannot
    // leak a repository in (GIT_DIR/GIT_WORK_TREE cleared, GIT_CEILING_DIRECTORIES stops the
    // upward walk at the sandbox), and every repo PROVES its git dir is inside the sandbox
    // before a single write command runs.
    let gitBase = base
    const sandbox = resolve(base)
    const gitEnv = { ...process.env, GIT_CONFIG_NOSYSTEM: '1' }
    delete gitEnv.GIT_CEILING_DIRECTORIES
    delete gitEnv.GIT_DIR
    delete gitEnv.GIT_WORK_TREE
    const gitq = (dir, ...args) =>
      execFileSync('git', ['-C', dir, ...args], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'], env: gitEnv })
    /** Refuse to WRITE unless this directory's git dir is inside the sandbox. */
    const assertSandboxed = (d) => {
      const gitDir = resolve(gitq(d, 'rev-parse', '--absolute-git-dir').trim())
      const effective = resolve(gitBase)
      if (gitDir !== resolve(d, '.git') || !gitDir.startsWith(`${effective}/`)) {
        throw new Error(
          `brand-parity selftest: REFUSING to write — ${d} resolves to the repository at ` +
            `${gitDir}, outside the sandbox ${resolve(gitBase)}. A fixture that escapes would commit ` +
            'into a real checkout; this battery stops instead.',
        )
      }
    }
    // PRE-VUELO — y una ironia que costo dos carriles bloqueados (2026-08-03). La comprobacion
    // de ambiente DEBE correr SIN el ceiling: el mismo GIT_CEILING_DIRECTORIES que impide que
    // una escritura escape hacia arriba impide tambien VER el repositorio que esta ahi arriba.
    // Con el ceiling puesto la sonda no encontraba nada y declaraba el sandbox limpio mientras
    // vivia dentro de un checkout — que es como los fixtures acabaron heredando el estado sucio
    // del repo padre y devolviendo `usable=false` donde el caso espera `true`.
    //
    // Y no basta con negarse: el gate fija TMPDIR DENTRO del arbol de trabajo (Taskfile.yml:928
    // para el export), asi que negarse ahi bloquea el push de todos los carriles por una razon
    // que no es suya. Asi que primero se BUSCA una base segura fuera de cualquier repositorio, y
    // solo si no existe ninguna se rehusa.
    const ambientRepoOf = (dir) => {
      const env = { ...process.env, GIT_CONFIG_NOSYSTEM: '1' }
      delete env.GIT_DIR
      delete env.GIT_WORK_TREE
      delete env.GIT_CEILING_DIRECTORIES
      try {
        return execFileSync('git', ['-C', dir, 'rev-parse', '--absolute-git-dir'], {
          encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'], env,
        }).trim()
      } catch {
        return null // lo esperado: ese directorio no esta dentro de ningun repositorio
      }
    }
    if (ambientRepoOf(sandbox)) {
      const inside = ambientRepoOf(sandbox)
      let moved = null
      for (const cand of ['/tmp', '/var/tmp', process.env.HOME]) {
        if (!cand || !existsSync(cand) || ambientRepoOf(cand)) continue
        moved = mkdtempSync(join(cand, 'brand-parity-selftest-'))
        break
      }
      if (!moved) {
        throw new Error(
          `brand-parity selftest: REFUSING to build fixtures — the sandbox ${sandbox} sits inside ` +
            `the repository at ${inside}, and no directory outside a repository was available. ` +
            'Point TMPDIR outside any checkout and re-run.',
        )
      }
      console.log(
        `brand-parity selftest: NOTICE — TMPDIR resolves inside ${inside}; git fixtures moved to ` +
          `${moved} so they cannot inherit that repository's state.`,
      )
      gitBase = moved
    }
    // El ceiling y HOME siguen a la base EFECTIVA, ya decidida.
    gitEnv.GIT_CEILING_DIRECTORIES = resolve(gitBase)
    gitEnv.HOME = resolve(gitBase)
    const mkrepo = (name) => {
      const d = join(gitBase, name)
      synthesizeWebTree(d, manifest)
      gitq(d, 'init', '-q', '-b', 'main')
      assertSandboxed(d)
      gitq(d, 'config', 'user.email', 'selftest@olivares.invalid')
      gitq(d, 'config', 'user.name', 'selftest')
      gitq(d, 'add', '-A')
      gitq(d, 'commit', '-q', '-m', 'site')
      return d
    }
    const expectUsable = (name, dir, want) => {
      const got = webTreeUsable(dir).usable
      if (got === want) {
        console.log(`brand-parity selftest: ok — ${name}`)
      } else {
        failures++
        console.error(`brand-parity selftest: FAIL — ${name}: expected usable=${want}, got ${got}`)
      }
    }
    const clean = mkrepo('site-git-clean')
    expectUsable('a clean checkout with no upstream IS evidence', clean, true)

    const dirty = mkrepo('site-git-dirty')
    writeFileSync(join(dirty, WEB_CSS_REL), `${readFileSync(join(dirty, WEB_CSS_REL), 'utf8')}\n/* local edit */\n`)
    expectUsable('a DIRTY checkout is NOT evidence', dirty, false)

    const origin = mkrepo('site-git-origin')
    const behind = join(gitBase, 'site-git-behind')
    gitq(gitBase, 'clone', '-q', origin, behind)
    assertSandboxed(behind)
    writeFileSync(join(origin, WEB_CSS_REL), `${readFileSync(join(origin, WEB_CSS_REL), 'utf8')}\n/* moved on */\n`)
    gitq(origin, 'commit', '-q', '-a', '-m', 'site moves on')
    gitq(behind, 'fetch', '-q', 'origin')
    expectUsable('a checkout BEHIND its upstream is NOT evidence', behind, false)
    gitq(behind, 'merge', '-q', '--ff-only', 'origin/main')
    expectUsable('the same checkout, once caught up, IS evidence again', behind, true)

    // DISCOVERY, and how many answers it is allowed to have (2026-08-09). gitBase is used
    // because it is the base already proven to sit outside any repository: inside one, the
    // git-common-dir branch above would add a second parent and this fixture would discover
    // the real checkouts of the box instead of its own.
    const expectCount = (name, dir, want) => {
      const got = discoverWebDir(dir).length
      if (got === want) {
        console.log(`brand-parity selftest: ok — ${name}`)
      } else {
        failures++
        console.error(`brand-parity selftest: FAIL — ${name}: expected ${want} candidate(s), got ${got}`)
      }
    }
    const disco = join(gitBase, 'disco')
    const discoRoot = join(disco, 'hub')
    mkdirSync(discoRoot, { recursive: true })
    expectCount('no sibling website tree is ZERO candidates', discoRoot, 0)
    synthesizeWebTree(join(disco, 'site-one'), manifest)
    expectCount('one sibling website tree is ONE candidate', discoRoot, 1)
    synthesizeWebTree(join(disco, 'site-two'), manifest)
    expectCount('two sibling website trees are TWO candidates, not a winner', discoRoot, 2)
  } finally {
    rmSync(base, { recursive: true, force: true })
  }
  if (failures > 0) {
    console.error(`brand-parity selftest: ${failures} case(s) FAILED — the detector itself is broken; fix it before trusting the gate.`)
    process.exit(1)
  }
  console.log('brand-parity selftest: OK — every red case is red, every green case is green')
}

// --- main -------------------------------------------------------------------

function main() {
  const args = process.argv.slice(2)
  let write = false
  let self = false
  let root = DEFAULT_ROOT
  for (let i = 0; i < args.length; i++) {
    if (args[i] === '--write') write = true
    else if (args[i] === '--selftest') self = true
    else if (args[i] === '--root') root = resolve(args[++i] ?? '')
    else {
      console.error(`brand-parity: unknown argument ${args[i]}`)
      process.exit(2)
    }
  }

  if (write) {
    const manifest = buildManifest(root)
    writeFileSync(join(root, MANIFEST_REL), stringifyManifest(manifest))
    console.log(`brand-parity: wrote ${MANIFEST_REL} (${manifest.entries.length} entries) from the DTCG sources`)
    return
  }
  if (self) {
    selftest(root)
    return
  }

  const errors = checkHub(root)
  const manifest = buildManifest(root)
  const candidates = discoverWebDir(root)
  const webDir = candidates.length === 1 ? candidates[0] : null
  // Did the LIVE cross-repo comparison actually happen? The final verdict must not describe a
  // comparison it skipped — an absent or unusable site tree and a compared one cannot share a
  // sentence. (Measured 2026-08-02: the green line claimed `console DTCG == <dir>/global.css`
  // while the tree it named had been ruled out as evidence three lines earlier.)
  let compared = false
  if (candidates.length > 1) {
    console.log(`brand-parity: NOTICE — ${candidates.length} public-website checkouts are present, so which one is`)
    console.log('brand-parity: "the website" is not a question this script can answer by looking:')
    for (const c of candidates) console.log(`brand-parity:   ${c}`)
    console.log('brand-parity: picking one by sort order would make the verdict depend on a directory name,')
    console.log('brand-parity: so the live cross-repo check is NOT PERFORMED here — and it is not reported')
    console.log('brand-parity: green either. Name the tree you mean with OLIVARES_WEB_DIR=<dir> and it comes')
    console.log(`brand-parity: back. Meanwhile the site CI pins its ${WEB_CSS_REL} to its vendored copy of`)
    console.log('brand-parity: this manifest, and this run pinned the manifest to the DTCG sources.')
  } else if (webDir === null) {
    console.log('brand-parity: NOTICE — no public-website tree found (set OLIVARES_WEB_DIR, or check out the website repo as a sibling).')
    console.log('brand-parity: the live cross-repo check is skipped HERE, but this is not a hole: the site CI pins')
    console.log(`brand-parity: its ${WEB_CSS_REL} to its vendored copy of this manifest (tests/brand-parity.test.ts),`)
    console.log(`brand-parity: and this run pinned ${MANIFEST_REL} to the DTCG sources — the surfaces cannot drift`)
    console.log('brand-parity: apart without one of the two gates going red.')
  } else {
    const explicit = process.env.OLIVARES_WEB_DIR !== undefined && process.env.OLIVARES_WEB_DIR !== ''
    const state = explicit ? { usable: true } : webTreeUsable(webDir)
    if (!state.usable) {
      console.log(`brand-parity: NOTICE — the website checkout at ${webDir} ${state.why}.`)
      console.log('brand-parity: a stale or dirty clone cannot tell a real brand drift from its own age,')
      console.log('brand-parity: so the live cross-repo check is NOT PERFORMED here — it is not reported')
      console.log('brand-parity: green either. Refresh that checkout (git -C <dir> pull --ff-only) or point')
      console.log('brand-parity: OLIVARES_WEB_DIR at the tree you mean, and this check comes back.')
      console.log(`brand-parity: meanwhile the site CI pins its ${WEB_CSS_REL} to its vendored copy of`)
      console.log('brand-parity: this manifest, and this run pinned the manifest to the DTCG sources.')
    } else {
      const { errors: webErrors, notes } = checkWebTree(manifest, webDir)
      errors.push(...webErrors)
      for (const n of notes) console.log(`brand-parity: ${n}`)
      compared = true
    }
  }

  if (errors.length > 0) {
    console.error(`brand-parity: FAIL — the console and the public website disagree on the brand (D-08):`)
    for (const e of errors) console.error(`  - ${e}`)
    process.exit(1)
  }
  console.log(
    `brand-parity: OK — ${manifest.entries.length} shared brand tokens agree` +
      (compared
        ? ` (console DTCG == ${webDir}/${WEB_CSS_REL})`
        : ' with the committed manifest — the live site comparison was NOT performed (see NOTICE above)'),
  )
}

main()
