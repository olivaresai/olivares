// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// brand.mjs — resolve the EMAIL subset of the brand from the DTCG sources.
//
// THE DEFECT THIS CLOSES. Until 2026-08-06 every outgoing email wrote its own
// brand by hand: `background:#f08000` at portal/handler.ts:98, `font-family:
// system-ui` at :94, the same pair again in delivery/resend.ts:77-87. Those hexes
// were CORRECT — and that was the problem. They were correct by coincidence,
// copied once from a palette they had no link to, sitting outside tokens:check.
// Move the brand and the console moves; the emails keep the old colour until a
// customer notices. What is closed here is the DERIVATION, not the value.
//
// SOURCE OF TRUTH: web/tokens/*.tokens.json (DTCG 2025.10), the same files
// tokens/build.mjs turns into the console's tokens.css. Values are read VERBATIM
// out of the DTCG `$value` scalars — no Style Dictionary run, no resolution step,
// so this module has zero dependencies and cannot drift from what build.mjs sees.
//
// WHAT IS DELIBERATELY OUT:
//   * derived.tokens.json — every one of its values is a `color-mix()` expression.
//     No mail client resolves CSS colour functions, and half of them do not resolve
//     custom properties either. An email needs literal hex, so the soft/line
//     derivations simply have no email equivalent.
//   * elevation — shadows do not survive Outlook's Word rendering engine, and a
//     shadow that renders in two clients out of ten is decoration, not brand.
//   * the graphite ramp and the semantic axis (success/warning/danger/info) —
//     transactional mail carries no status chrome. Emitting tokens no template
//     uses would make the gate assert a derivation nobody exercises.

import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const HERE = dirname(fileURLToPath(import.meta.url))
export const TOKENS_DIR = join(HERE, '..', 'web', 'tokens')

/** The seven product locales, in the order the console declares them. */
export const LOCALES = ['en', 'es', 'zh', 'ja', 'de', 'ru', 'fr']

// The mapping IS the contract, and it lives here in the job (the
// check-brand-parity.mjs pattern). Each entry: the email-side role, the DTCG file
// group, and the leaf token name. A role no template uses does not belong here.
const COLOR_ROLES = [
  // page canvas behind the card — the only surface a mail client paints edge to edge
  ['canvas', 'background'],
  // the card itself
  ['surface', 'surface'],
  // body copy
  ['text', 'foreground'],
  // secondary copy: footer, fallback URL, the ignore notice
  ['textMuted', 'muted-foreground'],
  // hairlines: the rule under the wordmark and above the footer
  ['border', 'border'],
  // THE single orange. Brand rule BRAND-02(d), "naranja escaso": exactly one use
  // per surface, and in an email that use is the one action the message asks for.
  ['accent', 'accent'],
  ['accentText', 'accent-foreground'],
  // the well behind a licence key or a shell command
  ['well', 'muted'],
]

const FONT_ROLES = [
  ['sans', 'font-sans'],
  ['mono', 'font-mono'],
  ['display', 'font-display'],
]

const RADIUS_ROLES = [
  ['sm', 'radius-sm'],
  ['md', 'radius-md'],
  ['lg', 'radius-lg'],
]

function readTokens(file) {
  return JSON.parse(readFileSync(join(TOKENS_DIR, file), 'utf8'))
}

/**
 * Pull one leaf `$value` out of a DTCG group, failing loudly when it is absent.
 * A missing token must stop the build: emitting `undefined` into a stylesheet
 * would ship an email with no colour and a green gate.
 */
function leaf(doc, group, name, where) {
  const g = doc[group]
  if (!g || typeof g !== 'object')
    throw new Error(`email/brand: ${where} has no "${group}" group`)
  const t = g[name]
  if (!t || typeof t.$value !== 'string')
    throw new Error(`email/brand: ${where} has no ${group}.${name}.$value`)
  return t.$value
}

/**
 * A font stack authored for the console is authored across several lines, because
 * that is how it reads inside a CSS block. Inside an HTML `style` attribute a
 * newline is legal but ugly and some clients' sanitisers rewrite it, so the stack
 * is collapsed to one line here. This is a whitespace normalisation and nothing
 * else: the families, their order and their quoting are untouched.
 */
function oneLine(stack) {
  return stack.replace(/\s*\n\s*/g, ' ').trim()
}

/**
 * Resolve the whole email brand. Returns a plain, JSON-serialisable object; the
 * emitted manifest is exactly `JSON.stringify` of this, so what the gate pins and
 * what the templates paint are the same object.
 */
export function resolveBrand() {
  const light = readTokens('theme.light.tokens.json')
  const dark = readTokens('theme.dark.tokens.json')
  const prim = readTokens('primitives.tokens.json')

  const theme = (doc, where) => {
    const out = {}
    for (const [role, token] of COLOR_ROLES)
      out[role] = leaf(doc, 'color', token, where)
    return out
  }

  const font = {}
  for (const [role, token] of FONT_ROLES)
    font[role] = oneLine(leaf(prim, 'font', token, 'primitives.tokens.json'))

  const radius = {}
  for (const [role, token] of RADIUS_ROLES)
    radius[role] = leaf(prim, 'radius', token, 'primitives.tokens.json')

  return {
    $description:
      'GENERATED — DO NOT EDIT (node email/build.mjs). The EMAIL subset of the ' +
      'Olivares brand, resolved verbatim from the DTCG sources in web/tokens/ ' +
      'that also generate the console. `task lint:email-brand` re-resolves it on ' +
      'every push and fails if this file, or either generated template bundle, no ' +
      'longer matches: an email cannot wear a brand the console has stopped wearing.',
    brand: 'brandv4 (2026-06-10)',
    source: 'web/tokens/*.tokens.json',
    roles: {
      color: Object.fromEntries(COLOR_ROLES),
      font: Object.fromEntries(FONT_ROLES),
      radius: Object.fromEntries(RADIUS_ROLES),
    },
    theme: {
      light: theme(light, 'theme.light.tokens.json'),
      dark: theme(dark, 'theme.dark.tokens.json'),
    },
    font,
    radius,
  }
}

// --- contrast, because "AA" is a measurement and not an intention ------------

/** sRGB hex (#rgb or #rrggbb) → relative luminance, WCAG 2.2 §relative-luminance. */
export function luminance(hex) {
  const m = /^#([0-9a-f]{3}|[0-9a-f]{6})$/i.exec(hex.trim())
  if (!m) throw new Error(`email/brand: not a plain sRGB hex: ${hex}`)
  let h = m[1]
  if (h.length === 3)
    h = h
      .split('')
      .map((c) => c + c)
      .join('')
  const ch = [0, 2, 4].map((i) => {
    const v = parseInt(h.slice(i, i + 2), 16) / 255
    return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4
  })
  return 0.2126 * ch[0] + 0.7152 * ch[1] + 0.0722 * ch[2]
}

/** WCAG 2.2 contrast ratio between two opaque sRGB hexes. */
export function contrast(a, b) {
  const la = luminance(a)
  const lb = luminance(b)
  return (Math.max(la, lb) + 0.05) / (Math.min(la, lb) + 0.05)
}

/**
 * Every foreground/background pair an email actually paints, per theme, with the
 * WCAG 2.2 threshold that applies to it. `large` marks text rendered at >= 18.66px
 * bold or >= 24px, whose AA threshold is 3.0 rather than 4.5 (§1.4.3). The heading
 * is the only large text here; the button label is 16px semibold, which is NOT
 * large by the spec's definition, so it is held to 4.5 like body copy.
 */
export function contrastPairs(brand) {
  const pairs = []
  for (const themeName of ['light', 'dark']) {
    const t = brand.theme[themeName]
    pairs.push(
      { theme: themeName, what: 'body text on card', fg: t.text, bg: t.surface, min: 4.5 },
      { theme: themeName, what: 'heading on card', fg: t.text, bg: t.surface, min: 3.0, large: true },
      { theme: themeName, what: 'muted text on card', fg: t.textMuted, bg: t.surface, min: 4.5 },
      { theme: themeName, what: 'button label on accent', fg: t.accentText, bg: t.accent, min: 4.5 },
      { theme: themeName, what: 'wordmark on card', fg: t.text, bg: t.surface, min: 4.5 },
      { theme: themeName, what: 'code in well', fg: t.text, bg: t.well, min: 4.5 },
      { theme: themeName, what: 'muted text on canvas', fg: t.textMuted, bg: t.canvas, min: 4.5 },
    )
  }
  return pairs
}
