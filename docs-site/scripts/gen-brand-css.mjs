#!/usr/bin/env node
// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// gen-brand-css.mjs — src/styles/brand.css, DERIVED from the product's own design tokens.
//
// WHY THIS IS A PROGRAM AND NOT A FILE, which is the same reason
// scripts/gen-release-diagrams.mjs is a program:
//
//   "COLOUR IS NOT AUTHORED HERE. The palette is read from web/tokens/theme.*.tokens.json,
//    the same source the console and the transactional emails derive from. A diagram may
//    not decide what the brand looks like; move the token and the diagrams move with it."
//                                            — scripts/gen-release-diagrams.mjs:19-22
//
// The diagrams obey that. The documentation site did not. Until this file existed, the link
// between the brand and docs.olivares.ai was a COMMENT — brand.css said "Values mirror the
// console design tokens (web/tokens/*.tokens.json); this file is the single place to retune"
// — and a comment has never moved a colour. Measured on 2026-08-29 before this ran:
//
//   * the console emits 101 distinct custom properties from the DTCG tokens; brand.css
//     declared 19, four of which were its own aliases.
//   * so BODY TEXT on the docs site was Starlight's stock --sl-color-gray-2, hsl(224,6%,77%)
//     = #c1c3c8 — a BLUE-tinted grey, on a brand whose primitives were deliberately
//     "re-derived to a near-neutral cool gray ... so the warm-graphite tint no longer
//     survives" (web/tokens/primitives.tokens.json:2). A different neutral family.
//   * and the four callout types read --sl-color-{blue,purple,orange,red}
//     (@astrojs/starlight/style/asides.css:7-26), none of which was overridden. The most
//     used callout in this documentation is :::caution — 490 of them — and it painted in
//     Starlight's --sl-color-orange, hsl(41,82%,63%) = #eebd53. That is a SECOND ORANGE,
//     490 times, on a brand whose every other surface is held to one (the "one orange per
//     composition" rule that gen-release-diagrams.mjs:30-33 enforces at generation time).
//
// Nobody chose that palette. It was the default of a theme, showing through the gaps of a
// hand-written override — which is exactly the failure mode a generated file removes.
//
// THREE PROPERTIES ARE ENFORCED HERE rather than reviewed by eye, same as the diagrams:
//
//   1. Every colour traces to web/tokens/*.tokens.json. This file introduces NO new brand
//      value. The soft/strong variants the theme needs but the tokens do not carry are
//      COMPUTED from a token by a declared oklab mix, never picked.
//   2. Every text pair is MEASURED. WCAG 2.2 contrast is computed against the surface the
//      text actually sits on; anything below its threshold fails. Non-text (borders that
//      carry meaning) is held to 3:1 by the same routine.
//   3. One brand orange. The accent family resolves to --accent and only to --accent; the
//      warning family is the product's own semantic amber, not a second accent.
//
// ⛔ WHAT THE 56 MEASURED PAIRS ARE, AND WHAT THEY ARE NOT — read this before citing the number.
//
// They are TOKEN pairs: for each colour this file emits, the ratio against the surface the
// mapping says it sits on. That is a real measurement and it is the first contrast measurement
// this site has ever had — `at:gate`, the product's only formal WCAG 2.2 A+AA gate
// (Taskfile.yml:716, wired as the `a11y` job in .github/workflows/mainline-ci.yml:1886), sweeps
// the CONSOLE's routes in both themes and mentions docs-site exactly ZERO times; no gate runs
// axe over the documentation site at all.
//
// But a green here is NOT "the documentation site is accessible", and nobody should cite it that
// way. A gate says what its DISCOVERY MECHANISM reaches (canon §0-COBERTURA): this one discovers
// pairs from the MAPPING, so a composition that only exists once a page renders — text laid over
// an image, a colour combination two components make together, anything Starlight or a plugin
// composes at runtime — is invisible to it BY CONSTRUCTION, not by failure. That is the gap
// axe-core exists to close, and closing it for this site is separate work with its own name.
//
// ⛔ AND THE PALETTE ITSELF DOES NOT COVER THE WHOLE SITE, so "one orange site-wide" is not a
// claim this file gets to make. `starlight-openapi` ships its own method palette built from HUE
// NUMBERS — starlight-openapi/styles.css:4-11 sets --sl-openapi-method-hue-put to
// var(--sl-hue-orange), post to --sl-hue-green, delete to --sl-hue-red, and so on. This file
// redefines --sl-color-orange but NOT --sl-hue-orange, so the REST reference pages still render
// their badges from the stock hues.
//
// That is left alone on purpose, twice over: a hue is a single number and the brand's colours are
// not expressible as one at Starlight's fixed saturation and lightness, so redefining it would
// mean inventing a value — the one thing this file may not do — and GET/POST/PUT badge colours are
// a convention API readers navigate by, not decoration to be rebranded. Declared, not fixed.
//
// Usage:
//   node docs-site/scripts/gen-brand-css.mjs            write src/styles/brand.css
//   node docs-site/scripts/gen-brand-css.mjs --check    0 clean · 1 the tree differs · 2 cannot look
//   node docs-site/scripts/gen-brand-css.mjs --selftest 0 the colour maths matches the browser
//
// The three answers are the EXIT CODE, never the prose (canon §1.5): a caller that has to
// parse the message is a caller that breaks on the first rewrite.

import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const here = path.dirname(fileURLToPath(import.meta.url))
const TOKENS = path.join(here, '..', '..', 'web', 'tokens')
const OUT = path.join(here, '..', 'src', 'styles', 'brand.css')

// ── cannot-look is an exit code, not a stack trace ────────────────────────────────────────
function cannotLook(msg) {
  process.stderr.write(`gen-brand-css: NO HE PODIDO MIRAR: ${msg}\n`)
  process.exit(2)
}
function found(msg) {
  process.stderr.write(`gen-brand-css: ${msg}\n`)
  process.exit(1)
}

// ── token loading ─────────────────────────────────────────────────────────────────────────
// The DTCG shape is {group: {$type, leafName: {$value}}} and the LEAF key is the CSS custom
// property name — the same contract web/tokens/build.mjs:26-28 states. We read the sources
// directly rather than through Style Dictionary: docs-site must not grow a build dependency
// to know what colour the brand is, and there are no {alias} references in these four files
// (verified: `grep -oE '"\$value": "\{[^}]*\}"' web/tokens/*.tokens.json` = 0 hits).
function load(file) {
  const p = path.join(TOKENS, file)
  let raw
  try {
    raw = fs.readFileSync(p, 'utf8')
  } catch (e) {
    cannotLook(`no leo ${p} (${e.code || e.message})`)
  }
  let doc
  try {
    doc = JSON.parse(raw)
  } catch (e) {
    cannotLook(`${file} no es JSON válido: ${e.message}`)
  }
  if (doc === null || typeof doc !== 'object' || Array.isArray(doc)) {
    cannotLook(`${file} no es un objeto DTCG en su raíz`)
  }
  const out = {}
  // ⛔ `typeof null === 'object'`, so a null group used to slip past the filter straight into
  // Object.entries(null), which throws and exits with Node's DEFAULT 1 — "cannot look" reported as
  // "finding", the one confusion this contract exists to prevent.
  for (const [group, body] of Object.entries(doc)) {
    if (group.startsWith('$') || body === null || typeof body !== 'object') continue
    for (const [leaf, node] of Object.entries(body)) {
      if (leaf.startsWith('$')) continue
      if (node && typeof node === 'object' && '$value' in node) out[leaf] = node.$value
    }
  }
  return out
}

const dark = load('theme.dark.tokens.json')
// The light theme is the theme file PLUS derived.tokens.json, exactly as the console's own build
// composes it (web/tokens/build.mjs:167 — `load(['theme.light.tokens.json','derived.tokens.json'])`).
// That is where the light `accent-strong` lives; reading only the theme file would make this
// generator's idea of "light" narrower than the system's.
const light = { ...load('theme.light.tokens.json'), ...load('derived.tokens.json') }
const prim = load('primitives.tokens.json')

// A token that vanishes from the source must stop the build, not emit `undefined` into CSS.
// web/tokens/build.mjs:71 records the exact bug this prevents: a token silently absent from
// a theme made the build report a count and emit nothing.
function tok(bag, name, whose) {
  const v = bag[name]
  if (typeof v !== 'string' || v === '') cannotLook(`falta el token \`${name}\` en ${whose}`)
  return v
}

// ── colour maths ──────────────────────────────────────────────────────────────────────────
function parseHex(hex) {
  const m = /^#([0-9a-f]{6})$/i.exec(hex.trim())
  if (!m) cannotLook(`no sé leer el color \`${hex}\` (sólo #rrggbb)`)
  const n = parseInt(m[1], 16)
  return [(n >> 16) & 255, (n >> 8) & 255, n & 255]
}
const toHex = ([r, g, b]) =>
  '#' + [r, g, b].map((c) => Math.round(Math.min(255, Math.max(0, c))).toString(16).padStart(2, '0')).join('')

const srgbToLin = (c) => (c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4)
const linToSrgb = (c) => (c <= 0.0031308 ? c * 12.92 : 1.055 * c ** (1 / 2.4) - 0.055)

// OKLab, so a mix here means the same thing `color-mix(in oklab, ...)` means everywhere else
// in this design system (web/tokens/derived.tokens.json uses exactly that space).
function toOklab(hex) {
  const [R, G, B] = parseHex(hex).map((v) => srgbToLin(v / 255))
  const l = Math.cbrt(0.4122214708 * R + 0.5363325363 * G + 0.0514459929 * B)
  const m = Math.cbrt(0.2119034982 * R + 0.6806995451 * G + 0.1073969566 * B)
  const s = Math.cbrt(0.0883024619 * R + 0.2817188376 * G + 0.6299787005 * B)
  return [
    0.2104542553 * l + 0.793617785 * m - 0.0040720468 * s,
    1.9779984951 * l - 2.428592205 * m + 0.4505937099 * s,
    0.0259040371 * l + 0.7827717662 * m - 0.808675766 * s,
  ]
}
function fromOklab([L, a, bb]) {
  const l = (L + 0.3963377774 * a + 0.2158037573 * bb) ** 3
  const m = (L - 0.1055613458 * a - 0.0638541728 * bb) ** 3
  const s = (L - 0.0894841775 * a - 1.291485548 * bb) ** 3
  const R = +4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s
  const G = -1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s
  const B = -0.0041960863 * l - 0.7034186147 * m + 1.707614701 * s
  return toHex([R, G, B].map((c) => linToSrgb(c) * 255))
}
/** `pct`% of `a` mixed into `b`, in OKLab — the space this system already mixes in. */
const mix = (a, b, pct) => {
  const A = toOklab(a)
  const B = toOklab(b)
  const t = pct / 100
  return fromOklab([0, 1, 2].map((i) => A[i] * t + B[i] * (1 - t)))
}

const relLum = (hex) => {
  const [r, g, b] = parseHex(hex).map((v) => srgbToLin(v / 255))
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}
const contrast = (a, b) => {
  const [x, y] = [relLum(a), relLum(b)].sort((p, q) => q - p)
  return (x + 0.05) / (y + 0.05)
}

// Every pair this file emits gets measured; the list is printed on every run and the count is
// never frozen in a comment (Taskfile.yml:716 says why: a number in prose lies as soon as the
// thing it counts grows).
const pairs = []
let theme_ = ''
const check = (label, fg, bg, min) => {
  pairs.push({ key: `${label}@${theme_}`, label, fg, bg, min, ratio: contrast(fg, bg) })
  return fg
}

// ── KNOWN, MEASURED, PINNED DEBT ──────────────────────────────────────────────────────────
// One pair the brand's own ramp cannot satisfy. This is NOT a waiver: it is re-measured on every
// run and fails three ways — if it gets WORSE, if it starts PASSING (stale, so the debt has to be
// retired rather than accumulate), or if any pair not listed here falls short. It is the shape
// the console's assistive-technology gate already uses for the same problem (Taskfile.yml:716,
// `CONTRAST_DEBT` — "re-measured every run, failing if it regresses, if it starts passing
// (stale), or if new debt appears"), and the shape theme.light.tokens.json:2 uses to declare the
// accent fill's 2.58 as a measured trade-off rather than an oversight.
// ⛔ AND AN ENTRY PINS THE PAIR, NOT JUST ITS LABEL. The first version keyed on `label@theme`
// and a ratio floor only, so renaming a check or re-pointing it at different colours kept the
// dispensation alive — which is exactly how the phantom "search panel" entry above survived. An
// entry now has to match the foreground, the background AND the threshold it was granted for; if
// any of those move, it stops applying and the pair is a finding again.
const DEBT = {
  'gray-4 hover border on the FileTree container@dark': {
    ratio: 2.8,
    fg: '#71717a',
    bg: '#2e2e32',
    min: 3,
    why:
      "FileTree's container is gray-6 and its hover border is gray-4 " +
      '(user-components/FileTree.astro:18-20,54-55). The graphite ramp holds no value that is ' +
      'BOTH a plausible fourth step and >=3:1 on #2e2e32: graphite-500 (#71717a) measures 2.80 ' +
      'and the next step up, graphite-400, is already gray-3. Inventing an in-between grey is ' +
      'the one thing this file may not do. It still IMPROVES on the stock theme, which measures ' +
      '2.15 for the same composition. And it is LATENT: `FileTree` appears in zero pages of this ' +
      'site today (git grep FileTree over the content = 0), so the pair is pinned so that the ' +
      'first page to use the component does not ship a sub-3:1 border in silence. Retire this ' +
      'entry the day the ramp gains a step or the container lightens.',
  },
}

// ── --selftest: the colour maths, pinned to an EXTERNAL oracle ────────────────────────────
// The whole palette below rests on mix() meaning the same thing the browser means by
// `color-mix(in oklab, A p%, B)`. Believing that because the code looks right is how a wrong
// conversion ships a whole theme. So the reference values are not mine: each was read out of
// Chromium on 2026-08-29 by setting `rgb(from color-mix(in oklab, A p%, B) r g b)` on an element
// and reading back the computed colour. Nine cases, every soft fill this file derives plus the
// dark body ink. If someone edits toOklab/fromOklab/mix, this goes red before anything renders.
const SELFTEST = [
  ['#e7b65a', '#28282b', 14, '#3f3a34'],
  ['#f38881', '#28282b', 14, '#423536'],
  ['#6fb6e6', '#28282b', 14, '#323a42'],
  ['#86c58a', '#28282b', 14, '#353b38'],
  ['#8a5300', '#fafaf9', 12, '#ede5dd'],
  ['#b0201b', '#fafaf9', 12, '#f5e1de'],
  ['#1e5a86', '#fafaf9', 12, '#dfe6eb'],
  ['#2f6b2c', '#fafaf9', 12, '#e1e8df'],
  ['#fafaf9', '#aaaab3', 55, '#d5d5d9'],
]
if (process.argv.includes('--selftest')) {
  const bad = []
  for (const [a, b, pct, want] of SELFTEST) {
    const got = mix(a, b, pct)
    if (got !== want) bad.push(`    color-mix(in oklab, ${a} ${pct}%, ${b}) -> ${got}, el navegador da ${want}`)
  }
  // Both halves: the WCAG ratio has to be right too. Two anchors, and the FIRST one is the
  // canonical one that does not depend on any Olivares colour at all — white on black is exactly
  // 21:1 by definition of the 2.x formula, so a broken relLum cannot hide behind our palette.
  // The second is the brand's own extreme pair, cross-computed in a SEPARATE implementation
  // (Python, not this file) so the number is not this code agreeing with itself. ⛔ It was first
  // written as 15.28 from memory and the battery caught it on its first run: the true value is
  // 14.0744. A hardcoded expectation is only worth what its source is worth.
  const anchor = contrast('#ffffff', '#000000')
  if (Math.abs(anchor - 21) > 0.001) bad.push(`    contrast(#ffffff,#000000) = ${anchor.toFixed(4)}, tiene que ser 21`)
  const known = contrast('#fafaf9', '#28282b')
  if (Math.abs(known - 14.0744) > 0.01) bad.push(`    contrast(#fafaf9,#28282b) = ${known.toFixed(4)}, se esperaba 14.0744`)
  // And the non-firing direction: a value the oracle did NOT produce must be reported as wrong.
  if (mix('#e7b65a', '#28282b', 14) === '#000000') bad.push('    control negativo: mix() devuelve negro')
  if (bad.length) {
    process.stderr.write('gen-brand-css: la aritmética de color NO coincide con el oráculo:\n' + bad.join('\n') + '\n')
    found(`${bad.length} casos de la batería fallan`)
  }
  process.stdout.write(`gen-brand-css: batería OK — ${SELFTEST.length} mezclas coinciden con color-mix(in oklab) del navegador, y el contraste conocido mide ${known.toFixed(2)}:1\n`)
  // ⛔ This used to exit 0 unconditionally, so `--selftest --check` reported a clean battery and
  // NEVER looked at drift — a green that answered a question nobody asked. With both flags the
  // battery is a precondition and execution continues into the check.
  if (!process.argv.includes('--check')) process.exit(0)
}


// A token may carry its own recipe instead of a literal — `accent-strong` on dark is
// `color-mix(in oklab, var(--accent) 85%, var(--surface))`. Resolving it here keeps the value the
// TOKEN's (its percentage, its operands) rather than something this file chose; emitting the
// expression verbatim is not an option because docs-site declares no --accent/--surface. Anything
// that is not a plain hex and not this exact shape is a cannot-look, never a guess.
function resolveTokenExpr(value, bag) {
  if (/^#[0-9a-f]{6}$/i.test(value.trim())) return value.trim()
  const m = /^color-mix\(\s*in\s+oklab\s*,\s*var\(--([a-z0-9-]+)\)\s+([\d.]+)%\s*,\s*var\(--([a-z0-9-]+)\)\s*\)$/i.exec(
    value.trim()
  )
  if (!m) cannotLook(`no sé resolver el valor de token \`${value}\` (sólo #rrggbb o color-mix(in oklab, var(--a) N%, var(--b)))`)
  const [, a, pct, b] = m
  const pn = Number(pct)
  if (!(pn >= 0 && pn <= 100)) cannotLook(`porcentaje fuera de [0,100] en \`${value}\``)
  return mix(tok(bag, a, 'el mismo tema'), tok(bag, b, 'el mismo tema'), pn)
}

// ── the mapping: Olivares token → Starlight property ──────────────────────────────────────
// Starlight's own scale is documented in @astrojs/starlight/style/props.css. Everything below
// resolves to a token; the `mix()` calls carry the token they derive from in the argument, so
// a reader can see there is no free-hand colour anywhere in this file.
function theme(t, isDark, graph2, graph3, graph4, graph6) {
  theme_ = isDark ? 'dark' : 'light'
  const bg = tok(t, 'background', isDark ? 'dark' : 'light')
  const surface = tok(t, 'surface', isDark ? 'dark' : 'light')
  const muted = tok(t, 'muted', isDark ? 'dark' : 'light')
  const fg = tok(t, 'foreground', isDark ? 'dark' : 'light')
  const mutedFg = tok(t, 'muted-foreground', isDark ? 'dark' : 'light')
  const border = tok(t, 'border', isDark ? 'dark' : 'light')
  const borderStrong = tok(t, 'border-strong', isDark ? 'dark' : 'light')
  // THE ORANGE IS SPLIT BY ROLE and the split is the whole point — theme.light.tokens.json:2
  // states it: `accent` is the FILL and is the brand orange #f08000 in BOTH themes because the
  // ink sits on it; `accent-text` is the orange as TEXT on the canvas and stays deepened on
  // light, "because #f08000 as text on #fafaf9 is 2.58:1 and fails AA for real".
  // Starlight has the same split and brand.css was not honouring it: --sl-color-text-accent
  // defaults to --sl-color-accent (props.css:156), and it is what colours EVERY in-content link
  // (markdown.css:101), the active table-of-contents item, the sidebar marker, the site title
  // and the search UI. So the text role must be wired explicitly, to `accent-text`.
  const accent = tok(t, 'accent', isDark ? 'dark' : 'light')
  const accentText = tok(t, 'accent-text', isDark ? 'dark' : 'light')
  // ⛔ The LINE role took `accent` on dark and `accent-text` on light — both text/fill tokens
  // pressed into a third duty — while the system already has a token for exactly this:
  // `accent-strong`, whose own description reads "Selection/active INDICATOR line — not a soft
  // container hairline. WCAG 2.2 SC 1.4.11 requires >=3:1" (web/tokens/derived.tokens.json:29-31).
  // Ignoring it was a role contradiction dressed as a passing contrast.
  //
  // On light the token is an explicit hex (#c26000, measured 3.72 on its worst surface — the value
  // derived.tokens.json:30 paid for after a color-mix version measured WEAKER than the accent it
  // was meant to strengthen). On dark it is still the token's own recipe, so it is RESOLVED here
  // from the same theme's tokens rather than emitted verbatim: docs-site has no --accent or
  // --surface custom property for a runtime color-mix to reach.
  const accentStrong = resolveTokenExpr(tok(t, 'accent-strong', isDark ? 'dark' : 'light'), t)
  const accentFg = tok(t, 'accent-foreground', isDark ? 'dark' : 'light')
  const accentSoft = tok(t, 'accent-soft', isDark ? 'dark' : 'light')
  const accentSoftFg = tok(t, 'accent-soft-foreground', isDark ? 'dark' : 'light')
  const success = tok(t, 'success', isDark ? 'dark' : 'light')
  const warning = tok(t, 'warning', isDark ? 'dark' : 'light')
  const danger = tok(t, 'danger', isDark ? 'dark' : 'light')
  const info = tok(t, 'info', isDark ? 'dark' : 'light')
  // Read but deliberately NOT mapped — see the note on `fam` below. Kept as a read so that a
  // token disappearing from the source still trips the cannot-look path rather than passing.
  void tok(t, 'confidence-attributed', isDark ? 'dark' : 'light')

  // Body copy. ⛔ This was `mix(fg, mutedFg, 55)` on dark — a 55% blend of my own invention, which
  // is precisely the "no new brand value" claim this file makes about itself being false. There is
  // no 55% anywhere in the token system. It is now a LITERAL: the graphite step Starlight itself
  // resolves --sl-color-text from (gray-2), which on dark is graphite-300 and on light is the
  // warm --muted-foreground the console already uses for secondary text. Measured 8.90:1 on the
  // dark page. Nothing is computed here any more.
  const bodyText = isDark ? graph2 : mutedFg

  // A callout's fill is the semantic hue laid ON the page — the SAME construction the design
  // system already uses for its soft fills: derived.tokens.json:5-27 defines *-soft as
  // `color-mix(in oklab, var(--<hue>) 12%, var(--surface))`. Using it here means a :::caution
  // in the documentation is literally the surface a warning gets in the console.
  //
  // The title ink is the semantic token ITSELF, in both themes and with no deepening: the two
  // theme files already role-split it (dark carries the light hues — warning #e7b65a — and
  // light carries the deep, text-safe ones — warning #8a5300). Deriving a "darker" variant on
  // top of an already-dark token would be this file deciding a brand value, which is exactly
  // what its header forbids. If a pair does not reach AA, that is a finding against the TOKEN
  // and the run exits 1 — it is not something to paper over with a local mix.
  // ⛔ The dark side used 14% and the light 12%. The canon has ONE number:
  // web/tokens/derived.tokens.json:5-27 builds every *-soft as
  // `color-mix(in oklab, var(--<hue>) 12%, var(--surface))`. A second percentage invented here is
  // a local recipe wearing the canon's name, so both themes now use 12%.
  //
  // The BACKDROP is the one honest deviation and it is declared rather than hidden: the canon
  // mixes toward `surface` because a console badge sits on a card; a Starlight aside sits on the
  // PAGE. Mixing toward `surface` would compute a fill for a backdrop this element never has. So
  // the CONSTRUCTION is the canon's (12%, oklab, toward the backdrop) and the backdrop is the real
  // one — which is why this file no longer claims "no derived values", only that it invents no
  // brand HUE and no percentage of its own.
  const softPct = 12
  const soft = (hue) => mix(hue, bg, softPct)
  const ink = (hue) => hue

  // A note for whoever is tempted to "finish" this by mirroring the console's badge exactly
  // (`bg-*-soft` + `border-*-line` + `text-*`, web/src/components/ui/badge.tsx:25-28): the
  // border here is NOT the console's `-line`. `-line` is a 1px resting hairline that only
  // reinforces a label, which is why the AT gate waives it as advisory
  // (web/tokens/build.mjs:145-148). Starlight's aside border is a 0.25rem bar and is the
  // callout's main visual identity, so it takes the FULL hue and is held to 3:1 below. Same
  // shape, different duty, different threshold — the distinction build.mjs:145-149 already
  // draws for accent-strong.
  const family = (name, hue, asideTitleIsText) => {
    const low = soft(hue)
    const high = ink(hue)
    // The title sits ON the low fill; asides.css:35 colours it with *-high.
    if (asideTitleIsText) check(`${name}-high on ${name}-low`, high, low, 4.5)
    // The 0.25rem border is the only thing separating one callout kind from another.
    check(`${name} border on ${name}-low`, hue, low, 3)
    return { low, base: hue, high }
  }

  // Starlight offers FIVE colour families; the product's sanctioned "semantic text on its own
  // soft fill" set is FOUR — success / warning / danger / info, the exact pairing
  // web/src/components/ui/badge.tsx:25-28 ships (`border-*-line bg-*-soft text-*`). So two of
  // Starlight's families land on the same product semantic, and that is the right trade:
  // inventing a fifth brand colour is what this file exists to prevent.
  //
  // ⛔ The first draft used the confidence teal (--confidence-attributed) for :::tip, because
  // it is a brand token and it is distinct. The contrast routine below REJECTED it at
  // 4.11:1 against its own soft fill on light — and it was right to: the teal belongs to the
  // confidence axis ("teal attributed / slate approximate", theme.dark.tokens.json:2), the
  // system never pairs it as text on a soft fill, and there is no --teal-soft anywhere. The
  // remedy is not a thinner mix to sneak it past the threshold; it is to use a colour the
  // system already sanctions for this duty.
  const fam = {
    orange: family('caution', warning, true), // :::caution — 490 uses, the most of any
    red: family('danger', danger, true), //     :::danger  — 39
    blue: family('note', info, true), //        :::note    — 344
    purple: family('tip', success, true), //    :::tip     — 77
    green: family('success', success, true), // Card/Badge success variants
  }

  // Body text and the aside's own copy are the two pairs a reader spends all their time on.
  check('body text on page', bodyText, bg, 4.5)
  check('heading ink on page', fg, bg, 4.5)
  check('body text on sidebar', bodyText, surface, 4.5)
  // The four surfaces an accent LINK actually lands on. brand.css shipped #b45500 here, a value
  // the token source retired on 2026-08-18 for measuring 4.38 on --muted and 4.36 on
  // --accent-soft (theme.light.tokens.json:49) — and .sl-mt-banner puts accent ON accent-soft.
  check('accent link on page', accentText, bg, 4.5)
  check('accent link on nav/sidebar', accentText, surface, 4.5)
  check('accent link on muted', accentText, muted, 4.5)
  check('accent link on accent-soft (the MT banner)', accentText, accentSoft, 4.5)
  // The accent as a LINE (search outline, mobile-TOC border, expressive-code active tab) is
  // non-text: 3:1. On light this is why the deepened value has to be the one that lands here —
  // the brand orange fill is a declared 2.58:1 against the canvas and cannot carry an edge.
  check('accent indicator line on page', accentStrong, bg, 3)
  check('accent indicator line on nav/sidebar', accentStrong, surface, 3)
  // ⛔ THE PAIR THAT ACTUALLY EXISTS, and getting here cost a round trip worth recording.
  // The contrast run reported that emitting the page background here shipped #fafaf9 on #f08000 =
  // 2.58:1. I acted on it and set the ink to --accent-foreground. Then I measured the RENDERED
  // page and the sidebar's active item came out at 3.34:1 — WORSE, and below AA. Both of us had
  // the wrong fill: these two consumers paint on --sl-color-text-accent, which is #a84f00 on light
  // and #f08000 on dark, never on --sl-color-accent. Measured over the real fill:
  //
  //            fill (text-accent)   ink=accent-foreground   ink=background
  //   dark          #f08000               6.88                  5.45
  //   light         #a84f00               3.34  ⛔              5.31
  //
  // So the original mapping was right in both themes and the "fix" was the regression. It reverts,
  // and the pair is now CHECKED — which is the durable half: neither the report nor I was
  // measuring the composition the stylesheet actually builds, which is how both of us got to argue
  // confidently about a number that belonged to a different element.
  //
  // (`--sl-color-bg-accent` is not checked because nothing on this site paints text on it: its one
  // consumer is Starlight's own Banner, and src/components/Banner.astro overrides that component.)
  check('inverted ink on the accent text fill (sidebar active item, skip link)', bg, accentText, 4.5)
  for (const [k, v] of Object.entries(fam)) check(`aside copy on ${k}-low`, fg, v.low, 4.5)
  // gray-3 is TEXT, not a line: the page sidebar, the edit link, the footer, a LinkCard's
  // description, Tabs and FileTree all colour copy with it. So it is measured like text.
  check('gray-3 secondary copy on page', graph3, bg, 4.5)
  check('gray-3 secondary copy on sidebar', graph3, isDark ? surface : muted, 4.5)
  // gray-4 is a LINE, not copy, and it has TWO real compositions. ⛔ The first draft measured it
  // against `surface`, called that "the search panel", and pinned a debt on it — but Starlight
  // paints the search-result tree connector on --sl-color-black (Search.astro:370-374), which
  // this file emits as `bg`, NOT on `surface`. So the pinned debt excused a composition the CSS
  // never creates, while the one that really falls short went unmeasured. A gate that measures
  // surfaces the stylesheet does not build is not a gate, it is an alibi. Both real pairs now:
  check('gray-4 tree connector on the search result surface', graph4, bg, 3)
  check('gray-4 hover border on the FileTree container', graph4, graph6, 3)

  return {
    bg, surface, muted, fg, mutedFg, bodyText, border, borderStrong,
    accent, accentText, accentFg, accentSoft, accentSoftFg, fam,
    accentLine: accentStrong,
    // The sidebar is the one surface where the two themes disagree, and the file being
    // replaced had already decided it: raised on dark (surface), recessed on light (muted).
    // A generator inherits a decision like that; it does not get to re-take it.
    sidebar: isDark ? surface : muted,
  }
}

// The graphite ramp is theme-independent (primitives), and Starlight's gray-1..7 runs from
// "closest to the text ink" to "closest to the page", which INVERTS between themes.
const G = (n) => tok(prim, `graphite-${n}`, 'primitives')
const darkRamp = [G(100), G(300), G(400), G(500), G(600), G(800), G(900)]
// ⛔ gray-4 on LIGHT was graphite-400 (#9c9ca3) in the first draft and that REGRESSED a real
// element: Starlight paints the search-result tree connector with `background:
// var(--sl-color-gray-4)` (components/Search.astro:444) and FileTree's border with it
// (user-components/FileTree.astro:55). Measured against the 3:1 non-text threshold, ours vs
// Starlight's own default for the same element:
//        dark   ours #71717a 3.04 on the page / 2.76 on the panel   vs default 2.53 / 2.15
//        light  ours #9c9ca3 2.73 on white     / 2.61 on the canvas vs default 3.37 / 3.14
// Dark improved on the baseline; LIGHT made it worse, and a change that lowers a ratio the
// theme already met is a regression no matter how the rest of the ramp reads. graphite-500
// restores it. The ramp then steps 900-700-600-500-300, skipping 400: at the light end the
// perceptual gap between adjacent graphite steps is small, and the duty of this step (a line
// that has to be SEEN) outranks an even distribution.
const lightRamp = [G(900), G(700), G(600), G(500), G(300), G(200), G(100)]

const D = theme(dark, true, G(300), G(400), G(500), G(800))
const L = theme(light, false, null, G(600), G(500), G(200))

// ── emit ──────────────────────────────────────────────────────────────────────────────────
const rows = (t, graph) => `
  /* Neutral ramp — the brand's near-neutral graphite, not Starlight's hsl(224°) blue-tinted
     grey. primitives.tokens.json:2 re-derived these on purpose; this is where that lands. */
  --sl-color-white: ${t.fg};
  --sl-color-gray-1: ${graph[0]};
  --sl-color-gray-2: ${graph[1]};
  --sl-color-gray-3: ${graph[2]};
  --sl-color-gray-4: ${graph[3]};
  --sl-color-gray-5: ${graph[4]};
  --sl-color-gray-6: ${graph[5]};
  --sl-color-gray-7: ${graph[6]};
  --sl-color-black: ${t.bg};

  /* Surfaces */
  --sl-color-bg: ${t.bg};
  --sl-color-bg-nav: ${t.surface};
  --sl-color-bg-sidebar: ${t.sidebar};
  --sl-color-bg-inline-code: ${t.muted};
  --sl-color-backdrop-overlay: ${t.overlay};

  /* Text. --sl-color-text-accent is EVERY in-content link (markdown.css:101), the active
     table-of-contents item, the sidebar marker, the site title and the search UI. It is the
     TEXT role, so it takes --accent-text, not the fill. */
  --sl-color-text: ${t.bodyText};
  --sl-color-text-accent: ${t.accentText};
  /* The ink on the two surfaces that invert: the sidebar's active item and the skip link. BOTH
     pair it with --sl-color-text-accent as the fill (SidebarSublist.astro:133-134,
     SkipLink.astro:21-22) — NOT with --sl-color-accent, and that distinction is the whole story
     below. */
  --sl-color-text-invert: ${t.bg};

  /* Lines */
  --sl-color-hairline-light: ${t.borderStrong};
  --sl-color-hairline: ${t.border};
  --sl-color-hairline-shade: ${t.border};

  /* The single brand orange, split by role exactly as the tokens split it: --sl-color-accent is
     the LINE (search outline, mobile-TOC border, expressive-code active tab), --sl-color-bg-accent
     is the FILL and stays the brand orange #f08000 in both themes, and the TEXT role is above. */
  --sl-color-accent-low: ${t.accentSoft};
  --sl-color-accent: ${t.accentLine};
  --sl-color-accent-high: ${t.accentSoftFg};
  --sl-color-bg-accent: ${t.accent};

  /* Callouts and badges — the product's OWN semantic axis (success / warning / danger / info
     + the confidence teal), so a warning in the documentation is the same colour as a warning
     in the console. Starlight's stock hues are what these replace: its --sl-color-orange
     (#eebd53) was a second orange on 490 :::caution blocks. */
  --sl-color-orange-low: ${t.fam.orange.low};
  --sl-color-orange: ${t.fam.orange.base};
  --sl-color-orange-high: ${t.fam.orange.high};
  --sl-color-red-low: ${t.fam.red.low};
  --sl-color-red: ${t.fam.red.base};
  --sl-color-red-high: ${t.fam.red.high};
  --sl-color-blue-low: ${t.fam.blue.low};
  --sl-color-blue: ${t.fam.blue.base};
  --sl-color-blue-high: ${t.fam.blue.high};
  --sl-color-purple-low: ${t.fam.purple.low};
  --sl-color-purple: ${t.fam.purple.base};
  --sl-color-purple-high: ${t.fam.purple.high};
  --sl-color-green-low: ${t.fam.green.low};
  --sl-color-green: ${t.fam.green.base};
  --sl-color-green-high: ${t.fam.green.high};`

D.overlay = tok(dark, 'overlay', 'dark')
L.overlay = tok(light, 'overlay', 'light')

const css = `/* SPDX-FileCopyrightText: 2026 Olivares.AI */
/* SPDX-License-Identifier: AGPL-3.0-only */

/*
 * GENERATED FILE — DO NOT EDIT. Run \`node docs-site/scripts/gen-brand-css.mjs\`.
 * Source of truth: web/tokens/*.tokens.json (DTCG), the same files the console
 * (web/src/styles/tokens.css), the release diagrams (scripts/gen-release-diagrams.mjs)
 * and the transactional emails derive from. Move the token; this moves with it.
 *
 * Olivares AI docs — the brand layer over Starlight. brandv4 (2026-06-10): near-neutral
 * charcoal/off-white surfaces and the SINGLE brand orange — the Ledger O's flagged WRITE
 * row — with the product's own semantic axis for callouts. Every text pair below was
 * measured against the surface it sits on at generation time; see the script header.
 */

/* Dark theme (default — the primary operator surface). */
:root,
:root[data-theme='dark'] {${rows(D, darkRamp)}
}

/* Light theme. */
:root[data-theme='light'] {${rows(L, lightRamp)}
}

/* Typefaces. --olv-font-sans / --olv-font-mono / --olv-font-display are EMITTED BY ASTRO's font
 * pipeline (astro.config.mjs \`fonts:\`), which resolves the same @fontsource-variable packages
 * the console pins, self-hosts them with no CDN, and derives a metric-matched fallback so the
 * swap costs no layout shift. This file only points Starlight's two variables at them.
 *
 * Before: --sl-font was never declared and --sl-font-mono named a family nothing loaded, so the
 * documentation rendered in the visitor's operating-system font while the product rendered in
 * Inter. The family names below are NOT authored here either — they are the stacks Astro built.
 *
 * The primitives carry the same three families for the console
 * (primitives.tokens.json font-sans / font-mono / font-display); they are the same typefaces,
 * reached through each surface's own loader. */
:root {
  --sl-font: var(--olv-font-sans);
  --sl-font-mono: var(--olv-font-mono);

  /* Motion, from primitives.tokens.json — the curve the console animates on. */
  --olv-ease-out: ${tok(prim, 'ease-out', 'primitives')};
}

/*
 * Machine-translation honesty banner. Rendered by src/components/Banner.astro on every
 * translated/fallback page: English is authoritative, native review pending. Uses the low-accent
 * surface so it reads as informational, not error.
 */
.sl-mt-banner {
  display: flex;
  flex-wrap: wrap;
  gap: 0.5rem 1rem;
  align-items: baseline;
  justify-content: space-between;
  margin: 0 0 1.25rem;
  padding: 0.55rem 0.85rem;
  border: 1px solid var(--sl-color-hairline-shade);
  border-inline-start: 3px solid var(--sl-color-accent);
  border-radius: ${tok(prim, 'radius-md', 'primitives')};
  background: var(--sl-color-accent-low);
  color: var(--sl-color-text);
  font-size: var(--sl-text-xs);
  line-height: 1.4;
}
.sl-mt-banner a {
  white-space: nowrap;
  font-weight: 600;
  /* ⛔ There was no colour here at all. This banner is an <aside> OUTSIDE .sl-markdown-content, so
     Starlight's link rule (style/markdown.css:100-104) never reaches it and the link fell back to
     the user agent's blue — and to its visited purple — on a brand-tinted surface, in every
     translated page of six locales. The contrast routine was measuring the accent on this fill
     while the CSS set no such colour: it was validating a pair the stylesheet did not create. */
  color: var(--sl-color-text-accent);
}
`

// ── verdict ───────────────────────────────────────────────────────────────────────────────
const fmt = (p) => `    ${p.ratio.toFixed(2)}:1 (min ${p.min}) ${p.fg} on ${p.bg} — ${p.label}`

// Three ways the debt table can be wrong, and all three are failures.
const failures = []
for (const p of pairs) {
  const d = DEBT[p.key]
  // A dispensation only applies to the exact pair it was granted for.
  const debt = d && d.fg === p.fg && d.bg === p.bg && d.min === p.min ? d : undefined
  if (d && !debt) {
    failures.push({ ...p, label: `${p.label} — DEUDA QUE YA NO CASA: se concedió para ${d.fg} sobre ${d.bg} (min ${d.min})` })
    continue
  }
  if (p.ratio >= p.min) {
    // A pinned pair that now passes is STALE: retire it, do not let it sit there excusing nothing.
    if (debt) failures.push({ ...p, label: `${p.label} — DEUDA CADUCA: ya pasa (${p.ratio.toFixed(2)} >= ${p.min}), retírala de DEBT` })
    continue
  }
  if (!debt) {
    failures.push(p)
  } else if (p.ratio < debt.ratio - 0.005) {
    failures.push({ ...p, label: `${p.label} — DEUDA EMPEORADA: ${p.ratio.toFixed(2)} < ${debt.ratio} declarado` })
  }
}
const pinned = pairs.filter((p) => DEBT[p.key] && p.ratio < p.min).length
const unknown = Object.keys(DEBT).filter((k) => !pairs.some((p) => p.key === k))

process.stdout.write(
  `gen-brand-css: ${pairs.length} pares medidos, ${failures.length} por debajo del umbral, ${pinned} de deuda declarada\n`
)
// A DEBT entry that names a pair this run never measured is a waiver for something that no longer
// exists — it would keep excusing a name nobody checks.
if (unknown.length) {
  process.stderr.write('gen-brand-css: entradas de DEBT que no casan con ningún par medido:\n  ' + unknown.join('\n  ') + '\n')
  found(`${unknown.length} entradas de DEBT huérfanas`)
}
if (failures.length) {
  process.stderr.write('gen-brand-css: pares que NO llegan:\n' + failures.map(fmt).join('\n') + '\n')
  found(`${failures.length} pares de contraste por debajo del umbral — no escribo`)
}

if (process.argv.includes('--check')) {
  let onDisk
  try {
    onDisk = fs.readFileSync(OUT, 'utf8')
  } catch (e) {
    cannotLook(`no leo ${OUT} (${e.code || e.message})`)
  }
  if (onDisk === css) {
    process.stdout.write('gen-brand-css: brand.css coincide con los tokens.\n')
    process.exit(0)
  }
  found(`brand.css NO coincide con web/tokens/*.tokens.json — corre \`node ${path.relative(process.cwd(), fileURLToPath(import.meta.url))}\``)
}

// A failed write is "no he podido", not a finding — and an unguarded writeFileSync also exits 1
// while potentially leaving the file truncated.
try {
  fs.writeFileSync(OUT, css)
} catch (e) {
  cannotLook(`no puedo escribir ${OUT} (${e.code || e.message})`)
}
process.stdout.write(`gen-brand-css: escrito ${path.relative(process.cwd(), OUT)}\n`)
