// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The console design tokens are GENERATED from the DTCG sources (web/tokens/console/*.tokens.json)
// into ./tokens.css. This test pins the generated file to direction A2 under the contrast
// ruling, in BOTH themes, so a token edit can never shift it silently:
//   - the palette: four neutrals (frame, canvas, surface, raised), a well, a pane, the hover,
//     selection and pressed layers, three text tones, the single brand orange #f08000 and its
//     roles, the status colors and their soft fills, the inverse surface of a toast, the four
//     tool data colors, the nine account accent colors and the neutral shadows;
//   - the role names the components use today, each equal to the palette token it aliases;
//   - the contrast contract of the palette, measured the way the design measures it: every
//     text token on 30 backgrounds per theme (the four bases, each base under the selection
//     fill, under the hover fill and under both, and canvas and surface under each of the
//     seven soft fills), control boundaries,
//     focus and the selection border at 3:1, the ink on the orange at 4.5:1, and what A2's
//     well, pane, pressed layer and toast carry (SELF-REVIEW-A2.md) — 277 pairs per theme,
//     light, dark and black; tertiary text on a hovered, selected or soft-filled stack is
//     measured with the ink A2's ONE tertiary rule of src/index.css gives it;
//   - each status ink on its own soft fill in every row a tag can sit in (160 pairs);
//   - the tertiary rule itself, part by part, and a census of every fill the console's
//     components paint, each held to that rule;
//   - the terminal's selection, drawn the way xterm draws it, visible on every theme;
//   - the type ladder, the font stacks, radii, density and motion.
/// <reference types="node" />
import { readdirSync, readFileSync } from 'node:fs'
import { describe, expect, it, vi } from 'vitest'

import {
  FALLBACK as CHART_FALLBACK,
  type ChartTheme,
} from '@/components/charts/chart-theme'
import { METHOD_COLORS } from '@/features/api-playground/method-colors'
import { terminalTheme } from '@/features/sessions/terminal-theme'

// vitest runs with cwd = web/; read the committed generated stylesheet.
const tokensCss = readFileSync('src/styles/tokens.css', 'utf8')
const indexCss = readFileSync('src/index.css', 'utf8')

/** Whitespace-normalized value, so the formatter's line breaks do not matter. */
const norm = (v: string) =>
  v.replace(/\s+/g, ' ').replace(/\(\s+/g, '(').replace(/\s+\)/g, ')').trim()

/** Parse a `selector { --a: x; --b: y; }` block into a name->value map. */
function parseBlock(rawCss: string, selector: string): Record<string, string> {
  // Comments first, so a selector named in prose cannot be taken for the real block.
  const css = rawCss.replace(/\/\*[\s\S]*?\*\//g, '')
  // A selector list may be broken over lines by the formatter.
  const head = selector
    .replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
    .replace(/,\s*/g, ',\\s*')
  const re = new RegExp(
    `(?:^|\\n)\\s*${head}\\s*\\{([\\s\\S]*?)\\n\\s*\\}`,
    'm',
  )
  const body = css.match(re)?.[1]
  if (!body) throw new Error(`block not found: ${selector}`)
  const out: Record<string, string> = {}
  for (const m of body.matchAll(/--([\w-]+):\s*([^;]+);/g))
    out[m[1]] = norm(m[2])
  return out
}

// ---------------------------------------------------------------- the palette
// The values of direction A2 (TOKENS-V2.md), which keeps every token name of the approved
// concept (scripts/check-brand-parity.mjs pins the subset the website shares against this
// generated file). The hover and selection fills are translucent: the contract below measures
// each one composited on what lies under it. The soft fills are opaque, A2's tint composited on
// the surface, so a tag reads the same in a resting, hovered or selected row. The contrast ruling
// (Supervisor, 2026-11-10) moves A2 where the contract fails: the hover and selection fills
// at 0.75 of A2's alpha in both themes, and five inks by the least OKLCH lightness that
// passes (light accent-text and warn, dark accent-text, bad and info).
const PALETTE_DARK: Record<string, string> = {
  frame: '#1b1b1e',
  canvas: '#28282b',
  surface: '#343437',
  raised: '#38383c',
  hover: 'rgba(255, 255, 255, 0.0525)',
  active: 'rgba(255, 255, 255, 0.075)',
  line: 'rgba(255, 255, 255, 0.07)',
  'line-strong': 'rgba(255, 255, 255, 0.13)',
  'ctl-border': '#8f8c88',
  scrim: 'rgba(0, 0, 0, 0.55)',
  well: '#202023',
  pane: '#242427',
  pressed: 'rgba(255, 255, 255, 0.16)',
  text: '#fafaf9',
  'text-2': '#cfcac5',
  'text-3': '#aaa59f',
  accent: '#f08000',
  'on-accent': '#1a1206',
  'accent-text': '#ffb277',
  'accent-soft': '#52402e',
  'accent-line': 'rgba(240, 128, 0, 0.42)',
  'accent-border': '#ffa257',
  focus: '#f08000',
  ok: '#86dfa0',
  'ok-soft': '#3f4c46',
  warn: '#f2d04f',
  'warn-soft': '#4f4a3a',
  bad: '#ffaead',
  'bad-soft': '#504346',
  info: '#9ec7f7',
  'info-soft': '#424851',
  'diff-add': 'rgba(134, 223, 160, 0.14)',
  'diff-del': 'rgba(255, 161, 161, 0.14)',
  inverse: '#fafaf9',
  'on-inverse': '#28282b',
  'inverse-accent': '#984700',
  'tool-claude': '#3987e5',
  'tool-codex': '#199e70',
  'tool-opencode': '#9085e9',
  'tool-gemini': '#d55181',
  'account-orange': '#ff9831',
  'account-green': '#7cd06a',
  'account-amber': '#d6b529',
  'account-red': '#ff7881',
  'account-blue': '#66b9ff',
  'account-purple': '#d296ff',
  'account-pink': '#ff82c7',
  'account-teal': '#55cec0',
  'account-slate': '#adb9c8',
  'shadow-raised':
    'inset 0 1px 0 rgba(255, 255, 255, 0.06), 0 0 0 1px rgba(0, 0, 0, 0.3), 0 2px 4px rgba(0, 0, 0, 0.26), 0 16px 40px -16px rgba(0, 0, 0, 0.65)',
  'shadow-pop':
    'inset 0 1px 0 rgba(255, 255, 255, 0.08), 0 0 0 1px rgba(0, 0, 0, 0.4), 0 8px 20px -6px rgba(0, 0, 0, 0.5), 0 32px 72px -24px rgba(0, 0, 0, 0.85)',
  'shadow-card':
    'inset 0 1px 0 rgba(255, 255, 255, 0.055), 0 0 0 1px rgba(0, 0, 6, 0.22), 0 1px 2px rgba(0, 0, 6, 0.24), 0 3px 8px -2px rgba(0, 0, 6, 0.3)',
  'shadow-well':
    'inset 0 1px 2px rgba(0, 0, 0, 0.42), inset 0 0 0 1px rgba(0, 0, 0, 0.2)',
}

const PALETTE_LIGHT: Record<string, string> = {
  frame: '#ecebe9',
  canvas: '#fafaf9',
  surface: '#ffffff',
  raised: '#ffffff',
  hover: 'rgba(40, 40, 43, 0.045)',
  active: 'rgba(40, 40, 43, 0.075)',
  line: 'rgba(40, 40, 43, 0.09)',
  'line-strong': 'rgba(40, 40, 43, 0.16)',
  'ctl-border': '#85807a',
  scrim: 'rgba(28, 28, 31, 0.32)',
  well: '#f0efed',
  pane: '#f6f6f4',
  pressed: 'rgba(40, 40, 43, 0.15)',
  text: '#28282b',
  'text-2': '#4a4540',
  'text-3': '#6b665f',
  accent: '#f08000',
  'on-accent': '#1a1206',
  'accent-text': '#954600',
  'accent-soft': '#fdeddb',
  'accent-line': 'rgba(194, 96, 0, 0.45)',
  'accent-border': '#c26000',
  focus: '#984700',
  ok: '#146a3a',
  'ok-soft': '#e8f0eb',
  warn: '#7a5600',
  'warn-soft': '#f1ede3',
  bad: '#ae241c',
  'bad-soft': '#f8ebeb',
  info: '#1759a8',
  'info-soft': '#eaf0f7',
  'diff-add': 'rgba(20, 106, 58, 0.12)',
  'diff-del': 'rgba(174, 36, 28, 0.1)',
  inverse: '#28282b',
  'on-inverse': '#fafaf9',
  'inverse-accent': '#ffa257',
  'tool-claude': '#2a78d6',
  'tool-codex': '#1baf7a',
  'tool-opencode': '#6250d6',
  'tool-gemini': '#d55181',
  'account-orange': '#a63f00',
  'account-green': '#227702',
  'account-amber': '#7e5f00',
  'account-red': '#ba022f',
  'account-blue': '#0060c1',
  'account-purple': '#7c3eb3',
  'account-pink': '#a82571',
  'account-teal': '#00776b',
  'account-slate': '#5a6472',
  'shadow-raised':
    '0 0 0 1px rgba(28, 28, 31, 0.05), 0 1px 2px rgba(28, 28, 31, 0.06), 0 12px 32px -14px rgba(28, 28, 31, 0.18)',
  'shadow-pop':
    '0 0 0 1px rgba(28, 28, 31, 0.06), 0 4px 14px -4px rgba(28, 28, 31, 0.14), 0 28px 56px -20px rgba(28, 28, 31, 0.32)',
  'shadow-card':
    '0 0 0 1px rgba(28, 28, 31, 0.07), 0 1px 2px rgba(28, 28, 31, 0.06), 0 3px 8px -2px rgba(28, 28, 31, 0.07)',
  'shadow-well':
    'inset 0 1px 2px rgba(28, 28, 31, 0.1), inset 0 0 0 1px rgba(28, 28, 31, 0.05)',
}

// ------------------------------------------------ the role names, as aliases
// Every role the current components paint with, and the palette token it IS. The value
// is resolved at build time, so the generated file holds the palette value itself.
const ROLE_ALIASES: Record<string, string> = {
  background: 'canvas',
  elevated: 'raised',
  overlay: 'scrim',
  'muted-foreground': 'text-2',
  foreground: 'text',
  border: 'line',
  'border-strong': 'line-strong',
  ring: 'focus',
  'accent-foreground': 'on-accent',
  'accent-soft-foreground': 'accent-text',
  'accent-strong': 'accent-border',
  success: 'ok',
  warning: 'warn',
  danger: 'bad',
  'success-soft': 'ok-soft',
  'warning-soft': 'warn-soft',
  'danger-soft': 'bad-soft',
  'elev-lg': 'shadow-raised',
  'elev-xl': 'shadow-pop',
}

// Roles the palette does not define. Each keeps a measured value, and the checks below hold
// it to the same contrast rule as the palette.
const OWN_DARK: Record<string, string> = {
  // A2: a card draws --shadow-card; the three small elevations draw nothing.
  'elev-xs': 'none',
  'elev-sm': 'none',
  'elev-md': 'none',
  // The selection tone as a fill of its own: the same value as `active`.
  muted: 'rgba(255, 255, 255, 0.075)',
  // The primary button's hover: the B2 fill hover, the same in both themes.
  'accent-hover': '#df6d00',
  'accent-active': '#d87000',
  'danger-solid': '#c5362f',
  'danger-solid-foreground': '#fff1ef',
  'confidence-attributed': '#5be0d8',
  'confidence-approximate': '#9aa3b0',
}
const OWN_LIGHT: Record<string, string> = {
  'elev-xs': 'none',
  'elev-sm': 'none',
  'elev-md': 'none',
  muted: 'rgba(40, 40, 43, 0.075)',
  'accent-hover': '#df6d00',
  'accent-active': '#cc6a00',
  'danger-solid': '#ae241c',
  'danger-solid-foreground': '#fff8f0',
  'confidence-attributed': '#0a7c77',
  'confidence-approximate': '#5b6470',
}

// Hairlines mixed at runtime from the theme's own status color; defined once in :root.
const RUNTIME_LINES: Record<string, string> = {
  'success-line': 'color-mix(in oklab, var(--success) 32%, var(--surface))',
  'warning-line': 'color-mix(in oklab, var(--warning) 32%, var(--surface))',
  'danger-line': 'color-mix(in oklab, var(--danger) 32%, var(--surface))',
  'info-line': 'color-mix(in oklab, var(--info) 32%, var(--surface))',
}

// ---------------------------------------------------------------- color math
type RGBA = [number, number, number, number]

function parseColor(v: string): RGBA {
  const hex = /^#([0-9a-f]{6})$/i.exec(v.trim())
  if (hex)
    return [0, 1, 2]
      .map((i) => parseInt(hex[1].slice(i * 2, i * 2 + 2), 16))
      .concat(1) as RGBA
  const rgba =
    /^rgba\(\s*([\d.]+),\s*([\d.]+),\s*([\d.]+),\s*([\d.]+)\s*\)$/i.exec(
      v.trim(),
    )
  if (rgba) return [1, 2, 3, 4].map((i) => Number(rgba[i])) as RGBA
  throw new Error(`not a hex or rgba() color: ${v}`)
}

/** What is painted when `fg` (possibly translucent) lies on the opaque `bg`. */
function over(fg: RGBA, bg: RGBA): RGBA {
  const a = fg[3]
  return [0, 1, 2].map((i) => fg[i] * a + bg[i] * (1 - a)).concat(1) as RGBA
}

function luminance([r, g, b]: RGBA): number {
  const f = (c: number) => {
    const s = c / 255
    return s <= 0.04045 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4)
  }
  return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b)
}

/** WCAG contrast ratio, RAW: decide on the raw ratio, round only what is printed. */
function contrast(fg: RGBA, bg: RGBA): number {
  const a = luminance(fg)
  const b = luminance(bg)
  return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05)
}

const show = (ratio: number) =>
  `${(Math.round(ratio * 100) / 100).toFixed(2)}:1`

/** Distance between two opaque colors in OKLab: how far apart two dots look. */
function okDistance(a: RGBA, b: RGBA): number {
  const lab = ([r, g, bl]: RGBA) => {
    const lin = (c: number) => {
      const s = c / 255
      return s <= 0.04045 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4)
    }
    const [lr, lg, lb] = [r, g, bl].map(lin)
    const l = Math.cbrt(
      0.4122214708 * lr + 0.5363325363 * lg + 0.0514459929 * lb,
    )
    const m = Math.cbrt(
      0.2119034982 * lr + 0.6806995451 * lg + 0.1073969566 * lb,
    )
    const s = Math.cbrt(
      0.0883024619 * lr + 0.2817188376 * lg + 0.6299787005 * lb,
    )
    return [
      0.2104542553 * l + 0.793617785 * m - 0.0040720468 * s,
      1.9779984951 * l - 2.428592205 * m + 0.4505937099 * s,
      0.0259040371 * l + 0.7827717662 * m - 0.808675766 * s,
    ]
  }
  const [x, y] = [lab(a), lab(b)]
  return Math.hypot(x[0] - y[0], x[1] - y[1], x[2] - y[2])
}

/** The selector of A2's ONE tertiary rule (`--text-3: var(--text-2)`) in src/index.css, or ''. */
const TERTIARY_RULE = (() => {
  const css = indexCss.replace(/\/\*[\s\S]*?\*\//g, '')
  const rules = [...css.matchAll(/([^{}]+)\{([^{}]*)\}/g)].filter((m) =>
    /--text-3:\s*var\(--text-2\)/.test(m[2]),
  )
  return rules.length === 1 ? rules[0][1] : ''
})()

/** A list split at its commas outside parentheses and brackets, each item whitespace-normalized. */
function splitTop(list: string): string[] {
  const out: string[] = []
  let depth = 0
  let from = 0
  for (let i = 0; i < list.length; i++) {
    if ('(['.includes(list[i])) depth++
    else if (')]'.includes(list[i])) depth--
    else if (list[i] === ',' && depth === 0) {
      out.push(list.slice(from, i))
      from = i + 1
    }
  }
  out.push(list.slice(from))
  return out.map(norm).filter(Boolean)
}

/**
 * The rule, part by part: `:is(<class tests>)` matches a fill painted at rest,
 * `:is(<class tests>):is(<states>)` a fill behind a variant, in the state it is painted in.
 */
interface RulePart {
  attrs: string[]
  states: string[] | null
}
function ruleParts(): RulePart[] {
  return splitTop(TERTIARY_RULE).map((part) => {
    const m = /^:is\(([^()]*)\)(?::is\(([^()]*)\))?$/.exec(part)
    if (!m) throw new Error(`unknown part of the tertiary rule: ${part}`)
    return {
      attrs: splitTop(m[1]),
      states: m[2] === undefined ? null : splitTop(m[2]),
    }
  })
}

/** Does `[class<op>='v']` match an element whose class attribute is `cls`? */
function attrMatches(sel: string, cls: string): boolean {
  const m = /^\[class([~^*])='([^']*)'\]$/.exec(sel)
  if (!m) throw new Error(`not a class attribute test: ${sel}`)
  const [, op, v] = m
  if (op === '~') return cls.split(/\s+/).includes(v)
  return op === '^' ? cls.startsWith(v) : cls.includes(v)
}

const PSEUDO_VARIANTS = [
  'hover',
  'focus',
  'focus-visible',
  'focus-within',
  'active',
  'disabled',
]

/**
 * The state a Tailwind variant paints in, as the CSS that selects it; null for a condition that
 * holds at rest (a breakpoint, a container, the theme); 'elsewhere' for a variant that paints
 * another element (`[&_th]:`, `group-hover:`, `peer-*:`), which a rule on this element cannot reach.
 */
function variantState(v: string): string | null {
  if (/^(\[&|group-|peer-|\*)/.test(v)) return 'elsewhere'
  if (PSEUDO_VARIANTS.includes(v)) return `:${v}`
  const attr = /^(data|aria)-\[([\w-]+)(?:=([\w-]+))?\]$/.exec(v)
  if (attr) return `[${attr[1]}-${attr[2]}${attr[3] ? `='${attr[3]}'` : ''}]`
  const aria = /^aria-([\w-]+)$/.exec(v)
  return aria ? `[aria-${aria[1]}='true']` : null
}

/** The variants of a class spelling: `data-[state=open]:hover:bg-muted` -> two variants. */
function variantsOf(spelling: string): string[] {
  const out: string[] = []
  let depth = 0
  let from = 0
  for (let i = 0; i < spelling.length; i++) {
    if (spelling[i] === '[') depth++
    else if (spelling[i] === ']') depth--
    else if (spelling[i] === ':' && depth === 0) {
      out.push(spelling.slice(from, i))
      from = i + 1
    }
  }
  return out
}

/**
 * Whether the tertiary rule gives the secondary ink where this class spelling paints its fill,
 * decided the way the browser matches the rule: on an element whose class attribute holds the
 * spelling first or after another class. A fill painted at rest must be matched at rest; a fill
 * behind a variant must be matched in that variant's state and NEVER at rest, so an element that
 * only has `hover:bg-muted` keeps its tertiary ink while nothing is painted under it.
 */
function ruleCovers(spelling: string): boolean {
  const parts = ruleParts()
  const at = [`${spelling} p-2`, `p-2 ${spelling}`]
  const hits = (p: RulePart, cls: string) =>
    p.attrs.some((a) => attrMatches(a, cls))
  const rest = (cls: string) => parts.some((p) => !p.states && hits(p, cls))
  const inState = (cls: string, on: string[]) =>
    parts.some((p) => hits(p, cls) && p.states?.some((s) => on.includes(s)))
  const states = variantsOf(spelling).map(variantState)
  if (states.includes('elsewhere')) return false
  const painted = states.filter((s): s is string => s !== null)
  if (painted.length === 0) return at.every(rest)
  return at.every((cls) => !rest(cls) && inState(cls, painted))
}

// How the product paints each fill of the contract (a soft fill also under its role name).
const FILL_SPELLINGS: Record<string, string[]> = {
  hover: ['hover:bg-hover', 'hover:bg-muted'],
  selected: ['bg-active', 'bg-muted', 'aria-pressed:bg-active'],
  'accent-soft': ['bg-accent-soft'],
  'ok-soft': ['bg-ok-soft', 'bg-success-soft'],
  'warn-soft': ['bg-warn-soft', 'bg-warning-soft'],
  'bad-soft': ['bg-bad-soft', 'bg-danger-soft'],
  'info-soft': ['bg-info-soft'],
  'diff-add': ['bg-diff-add'],
  'diff-del': ['bg-diff-del'],
}

/**
 * The ink tertiary text has on a background of the contract: the secondary ink where the rule
 * covers every way the product paints every fill of the stack, `text-3` itself on a resting
 * surface or where it does not. So the contract measures what the product renders, and goes red
 * without the rule.
 */
function tertiaryInk(bg: string): 'text-2' | 'text-3' {
  const fills = bg.split('+').slice(1)
  const covered = (f: string) =>
    f in FILL_SPELLINGS && FILL_SPELLINGS[f].every((s) => ruleCovers(s))
  return fills.length > 0 && fills.every(covered) ? 'text-2' : 'text-3'
}

interface Pair {
  theme: string
  fg: string
  bg: string
  ratio: number
  required: number
}

/** What a row can sit on: the four bases, each under the selection and hover fills. */
function rowSurfaces(t: Record<string, string>): Record<string, RGBA> {
  const c = (n: string) => parseColor(t[n])
  const bases: Record<string, RGBA> = {}
  for (const b of ['canvas', 'surface', 'raised', 'frame']) bases[b] = c(b)
  const bgs: Record<string, RGBA> = { ...bases }
  for (const b of ['canvas', 'surface', 'raised', 'frame']) {
    bgs[`${b}+selected`] = over(c('active'), bases[b])
    // A pointer over a row, and over the selected row: the hover fill stacks on both.
    bgs[`${b}+hover`] = over(c('hover'), bases[b])
    bgs[`${b}+selected+hover`] = over(c('hover'), bgs[`${b}+selected`])
  }
  return bgs
}

/** The design's contrast method: 250 required pairs per theme. */
function requiredPairs(theme: string, t: Record<string, string>): Pair[] {
  const c = (n: string) => parseColor(t[n])
  const bases: Record<string, RGBA> = {}
  for (const b of ['canvas', 'surface', 'raised', 'frame']) bases[b] = c(b)
  const bgs = rowSurfaces(t)
  for (const s of [
    'accent-soft',
    'ok-soft',
    'warn-soft',
    'bad-soft',
    'info-soft',
    'diff-add',
    'diff-del',
  ]) {
    bgs[`canvas+${s}`] = over(c(s), bases.canvas)
    bgs[`surface+${s}`] = over(c(s), bases.surface)
  }
  const pairs: Pair[] = []
  for (const fg of [
    'text',
    'text-2',
    'text-3',
    'accent-text',
    'ok',
    'warn',
    'bad',
    'info',
  ])
    for (const [bn, bv] of Object.entries(bgs))
      pairs.push({
        theme,
        fg,
        bg: bn,
        ratio: contrast(c(fg === 'text-3' ? tertiaryInk(bn) : fg), bv),
        required: 4.5,
      })
  const ui: [string, string[]][] = [
    ['ctl-border', ['canvas', 'surface', 'raised']],
    ['focus', ['canvas', 'surface', 'raised', 'frame']],
    ['accent-border', ['canvas', 'surface']],
  ]
  for (const [fg, bns] of ui)
    for (const bn of bns)
      pairs.push({
        theme,
        fg,
        bg: bn,
        ratio: contrast(c(fg), bases[bn]),
        required: 3,
      })
  pairs.push({
    theme,
    fg: 'on-accent',
    bg: 'accent',
    ratio: contrast(c('on-accent'), c('accent')),
    required: 4.5,
  })
  // A2's new surfaces (SELF-REVIEW-A2.md, contrast): the well (fields, code, diffs, the
  // terminal), the pane beside the page, the pressed layer and the toast.
  const add = (fg: string, bg: string, bv: RGBA, required: number) =>
    pairs.push({ theme, fg, bg, ratio: contrast(c(fg), bv), required })
  const well = c('well')
  const pane = c('pane')
  add('ctl-border', 'well', well, 3)
  add('ctl-border', 'pane', pane, 3)
  for (const fg of ['text', 'text-2', 'text-3', 'info', 'ok', 'bad', 'warn'])
    add(fg, 'well', well, 4.5)
  for (const [fg, s] of [
    ['text', 'diff-add'],
    ['text', 'diff-del'],
    ['ok', 'diff-add'],
    ['bad', 'diff-del'],
    // The ink of a number in code: it sits on both tints in a diff.
    ['info', 'diff-add'],
    ['info', 'diff-del'],
  ])
    add(fg, `well+${s}`, over(c(s), well), 4.5)
  for (const fg of ['text', 'text-2', 'text-3']) add(fg, 'pane', pane, 4.5)
  for (const b of ['surface', 'frame'])
    for (const fg of ['text', 'text-2'])
      add(fg, `${b}+pressed`, over(c('pressed'), bases[b]), 4.5)
  add('on-inverse', 'inverse', c('inverse'), 4.5)
  add('inverse-accent', 'inverse', c('inverse'), 4.5)
  // Three more of SELF-REVIEW-A2.md: a field's boundary on the frame, the split diff's line
  // numbers (tertiary text on the well under a 4 % tint of the text), and the toast's dismiss
  // (its text at 80 %).
  add('ctl-border', 'frame', bases.frame, 3)
  const [tr, tg, tb] = c('text')
  add('text-3', 'well+tint4', over([tr, tg, tb, 0.04], well), 4.5)
  const [ir, ig, ib] = c('on-inverse')
  const dismiss = over([ir, ig, ib, 0.8], c('inverse'))
  pairs.push({
    theme,
    fg: 'on-inverse at 80%',
    bg: 'inverse',
    ratio: contrast(dismiss, c('inverse')),
    required: 4.5,
  })
  return pairs
}

// A tag (badge, count, notice) in a row: each status ink on its own soft fill.
const TAG_FILLS: [string, string][] = [
  ['accent-text', 'accent-soft'],
  ['ok', 'ok-soft'],
  ['warn', 'warn-soft'],
  ['bad', 'bad-soft'],
  ['info', 'info-soft'],
]

/**
 * Every class spelling of the fills the tertiary rule is about (hover, selection, the soft fills
 * and the diff lines at any opacity, and the status tints `bg-warning/5`), with any variant, as
 * the console writes them (whole, with the parentheses and commas of an arbitrary variant such as
 * `[&_button:not(a,b)]:`): every .ts and .tsx file under src/, tests aside, with the files that
 * paint each one.
 */
function fillCensus(): Map<string, Set<string>> {
  const FILL =
    /(?<![\w-])(?:[^\s"'`{}]*:)?!?bg-(?:(?:hover|muted|active|diff-add|diff-del|(?:accent|ok|warn|bad|info|success|warning|danger)-soft)(?:\/[\w.[\]%]+)?|(?:accent|ok|warn|bad|info|success|warning|danger)\/[\w.[\]%]+)(?![\w-])/g
  const found = new Map<string, Set<string>>()
  for (const f of readdirSync('src', { recursive: true, encoding: 'utf8' })) {
    if (!/\.tsx?$/.test(f) || /\.test\.tsx?$/.test(f)) continue
    for (const m of readFileSync(`src/${f}`, 'utf8').matchAll(FILL))
      found.set(m[0], (found.get(m[0]) ?? new Set<string>()).add(`src/${f}`))
  }
  return found
}

describe('v26.10 design tokens — the generated file is the approved system', () => {
  const light = parseBlock(tokensCss, ':root')
  const dark = parseBlock(tokensCss, '.dark')
  const theme = parseBlock(tokensCss, '@theme inline')

  it('holds the palette verbatim in both themes', () => {
    for (const [name, want] of Object.entries(PALETTE_DARK))
      expect(dark[name], `.dark --${name}`).toBe(norm(want))
    for (const [name, want] of Object.entries(PALETTE_LIGHT))
      expect(light[name], `:root --${name}`).toBe(norm(want))
  })

  it('gives every role the value of the palette token it aliases', () => {
    for (const [role, token] of Object.entries(ROLE_ALIASES)) {
      expect(dark[role], `.dark --${role} = --${token}`).toBe(dark[token])
      expect(light[role], `:root --${role} = --${token}`).toBe(light[token])
    }
  })

  it('declares nothing else: each block is exactly palette + roles', () => {
    const want = (
      palette: Record<string, string>,
      own: Record<string, string>,
      extra: Record<string, string>,
    ) =>
      new Set([
        ...Object.keys(palette),
        ...Object.keys(ROLE_ALIASES),
        ...Object.keys(own),
        ...Object.keys(extra),
      ])
    expect(new Set(Object.keys(dark))).toEqual(want(PALETTE_DARK, OWN_DARK, {}))
    expect(new Set(Object.keys(light))).toEqual(
      want(PALETTE_LIGHT, OWN_LIGHT, RUNTIME_LINES),
    )
    for (const [name, v] of Object.entries(OWN_DARK))
      expect(dark[name], `.dark --${name}`).toBe(v)
    for (const [name, v] of Object.entries(OWN_LIGHT))
      expect(light[name], `:root --${name}`).toBe(v)
    for (const [name, v] of Object.entries(RUNTIME_LINES))
      expect(light[name], `:root --${name}`).toBe(v)
  })

  it('passes all 554 required contrast pairs (277 per theme)', () => {
    const pairs = [
      ...requiredPairs('dark', dark),
      ...requiredPairs('light', light),
    ]
    expect(pairs).toHaveLength(554)
    const failing = pairs
      .filter((p) => p.ratio < p.required)
      .map(
        (p) =>
          `${p.theme} ${p.fg} on ${p.bg}: ${show(p.ratio)} < ${p.required}`,
      )
    expect(failing).toEqual([])
  })

  it('keeps each status ink AA on its own soft fill in every row a tag sits in', () => {
    // A tag, a count or a notice in a resting, hovered, selected or keyboard-active row
    // (data-table.tsx paints the active row bg-active): its fill lies on the row's fills.
    let count = 0
    const weak: string[] = []
    for (const [name, t] of [
      ['light', light],
      ['dark', dark],
    ] as const)
      for (const [bn, bv] of Object.entries(rowSurfaces(t)))
        for (const [fg, soft] of TAG_FILLS) {
          const r = contrast(parseColor(t[fg]), over(parseColor(t[soft]), bv))
          count++
          if (r < 4.5) weak.push(`${name} ${fg}/${soft} on ${bn}: ${show(r)}`)
        }
    expect(count).toBe(160)
    expect(weak).toEqual([])
  })

  it('keeps the ONE tertiary rule whole, part by part', () => {
    // The fills painted behind a variant, the states they are painted in, and the fills
    // painted at rest (the first class, or a later one). Removing any part turns this red.
    const VARIANT_FILLS = [
      'bg-hover',
      'bg-muted',
      'bg-active',
      'bg-accent-soft',
      'bg-bad-soft',
      'bg-danger-soft',
      'bg-warning-soft',
    ]
    const STATES = [
      ':hover',
      ':focus-visible',
      ':disabled',
      '[data-highlighted]',
      "[data-state='active']",
      "[data-state='open']",
      "[aria-pressed='true']",
      "[aria-current='page']",
      "[data-selected='true']",
      '[data-current]',
    ]
    const REST_FILLS = [
      'bg-hover',
      'bg-muted',
      'bg-active',
      'bg-accent-soft',
      'bg-ok-soft',
      'bg-warn-soft',
      'bg-bad-soft',
      'bg-info-soft',
      'bg-success-soft',
      'bg-warning-soft',
      'bg-danger-soft',
      'bg-diff-',
      'bg-success/',
      'bg-warning/',
      'bg-danger/',
      'bg-warn/',
    ]
    expect(ruleParts()).toEqual([
      { attrs: VARIANT_FILLS.map((f) => `[class*=':${f}']`), states: STATES },
      { attrs: REST_FILLS.map((f) => `[class^='${f}']`), states: null },
      { attrs: REST_FILLS.map((f) => `[class*=' ${f}']`), states: null },
    ])
  })

  it('holds every fill the components paint to the tertiary rule (a census)', () => {
    // Each spelling is matched by the rule where it paints, or listed here with the reason no
    // tertiary text can sit on it.
    const ELSEWHERE: Record<string, string> = {
      '[&_tbody_tr]:hover:bg-hover':
        "static-table.tsx: the rows are the caller's; the table carries this class and is :hover whenever one of its rows is, so the rule's variant part gives the whole table the secondary ink",
      'hover:[&_button]:!bg-muted': "the graph's zoom buttons: icons, no text",
      'aria-disabled:active:bg-active':
        "button.tsx: an aria-disabled secondary button paints its rest fill back while pressed; the same element carries bg-active at rest, so the rule's rest part gives it the secondary ink in every state",
      '[&_button.bg-active:not([aria-pressed=true],[data-state=on]):hover]:bg-hover':
        "page-actions.tsx: the header bar's flat secondary button under the pointer; the button is the caller's and carries bg-active at rest, which the rule's rest part reaches, and the bar carries this class and is :hover whenever its button is",
    }
    const found = fillCensus()
    expect([...found.keys()]).toEqual(
      expect.arrayContaining(['bg-muted', 'hover:bg-hover', 'bg-active']),
    )
    // The census reads the .ts files that hold class names too, not only the components.
    for (const cls of Object.values(METHOD_COLORS).join(' ').split(' '))
      if (cls.startsWith('bg-'))
        expect([...(found.get(cls) ?? [])], cls).toContain(
          'src/features/api-playground/method-colors.ts',
        )
    const uncovered = [...found]
      .filter(([s]) => !ruleCovers(s) && !(s in ELSEWHERE))
      .map(([s, files]) => `${[...files].join(', ')}: ${s}`)
    expect(uncovered).toEqual([])
    // The list holds only what is still painted and what the rule cannot reach.
    for (const s of Object.keys(ELSEWHERE)) {
      expect(found.has(s), s).toBe(true)
      expect(ruleCovers(s), s).toBe(false)
    }
  })

  it('reproduces the minimums the design measured', () => {
    // The worst background per text token, A2 under the contrast ruling (tertiary text on a
    // hovered, selected or soft-filled stack is measured with the ink of the tertiary rule): a
    // drift of the method, not only of a value, turns this red.
    const min = (t: string, fg: string) =>
      Math.min(
        ...requiredPairs(t, t === 'dark' ? dark : light)
          .filter((p) => p.fg === fg)
          .map((p) => p.ratio),
      )
    const MEASURED: [string, string, number][] = [
      ['dark', 'text', 7.18],
      ['light', 'text', 9.33],
      ['dark', 'text-2', 4.61],
      ['light', 'text-2', 6.01],
      ['dark', 'text-3', 4.78],
      ['light', 'text-3', 4.61],
      ['dark', 'accent-text', 4.5],
      ['light', 'accent-text', 4.51],
      ['dark', 'ok', 4.95],
      ['light', 'ok', 4.51],
      ['dark', 'warn', 5.27],
      ['light', 'warn', 4.5],
      ['dark', 'bad', 4.51],
      ['light', 'bad', 4.63],
      ['dark', 'info', 4.53],
      ['light', 'info', 4.69],
      ['dark', 'ctl-border', 3.49],
      ['light', 'ctl-border', 3.28],
    ]
    for (const [t, fg, want] of MEASURED)
      expect(min(t, fg), `${t} ${fg}`).toBeCloseTo(want, 2)
  })

  it('keeps the ink AA on the orange at rest, on hover and pressed', () => {
    for (const [name, t] of [
      ['light', light],
      ['dark', dark],
    ] as const)
      for (const fill of ['accent', 'accent-hover', 'accent-active']) {
        const r = contrast(
          parseColor(t['accent-foreground']),
          parseColor(t[fill]),
        )
        expect(
          r,
          `${name}: ink on --${fill} is ${show(r)}`,
        ).toBeGreaterThanOrEqual(4.5)
      }
  })

  it('keeps the solid well and the danger fill AA for the text they carry', () => {
    for (const [name, t] of [
      ['light', light],
      ['dark', dark],
    ] as const) {
      // The well is translucent: measure it on the two bases it is painted on.
      for (const base of ['canvas', 'surface']) {
        const well = over(parseColor(t.muted), parseColor(t[base]))
        for (const fg of ['foreground', 'muted-foreground', 'accent-text']) {
          const r = contrast(parseColor(t[fg]), well)
          expect(
            r,
            `${name}: --${fg} on --muted over --${base} is ${show(r)}`,
          ).toBeGreaterThanOrEqual(4.5)
        }
      }
      const solid = contrast(
        parseColor(t['danger-solid-foreground']),
        parseColor(t['danger-solid']),
      )
      expect(
        solid,
        `${name}: danger-solid ink ${show(solid)}`,
      ).toBeGreaterThanOrEqual(4.5)
    }
  })

  it('keeps a status tag readable while it fades in (97.4% opacity frame)', () => {
    // A tag mounting with the enter animation is painted partly transparent for a few
    // frames; the browser gate once sampled one of them. The soft fill lies on a card.
    for (const [name, t] of [
      ['light', light],
      ['dark', dark],
    ] as const)
      for (const [fg, soft] of [
        ['danger', 'danger-soft'],
        ['warning', 'warning-soft'],
        ['success', 'success-soft'],
        ['info', 'info-soft'],
      ]) {
        const bg = over(parseColor(t[soft]), parseColor(t.surface))
        const f = parseColor(t[fg])
        const painted = over([f[0], f[1], f[2], 0.974], bg)
        const r = contrast(painted, bg)
        expect(
          r,
          `${name}: --${fg} on --${soft} mid-fade ${show(r)}`,
        ).toBeGreaterThanOrEqual(4.5)
      }
  })

  it('keeps the selection border at 3:1 on its neighbours and apart from focus', () => {
    for (const [name, t] of [
      ['light', light],
      ['dark', dark],
    ] as const) {
      const sel = parseColor(t['accent-strong'])
      for (const bg of ['surface', 'background', 'elevated']) {
        const r = contrast(sel, parseColor(t[bg]))
        expect(
          r,
          `${name}: selection on --${bg} ${show(r)}`,
        ).toBeGreaterThanOrEqual(3)
      }
      const onSoft = contrast(
        sel,
        over(parseColor(t['accent-soft']), parseColor(t.surface)),
      )
      expect(
        onSoft,
        `${name}: selection on --accent-soft ${show(onSoft)}`,
      ).toBeGreaterThanOrEqual(3)
      // A selected row must not look focused: the two indicators are different colors.
      expect(t['accent-strong']).not.toBe(t.ring)
    }
  })

  it('records the orange FILL on the light canvas as informative, below 3:1', () => {
    // The fill carries ink, never a boundary: the 3:1 boundary is --accent-border.
    const r = contrast(parseColor(light.accent), parseColor(light.canvas))
    expect(r).toBeCloseTo(2.58, 2)
    expect(
      contrast(parseColor(light['accent-border']), parseColor(light.canvas)),
    ).toBeGreaterThanOrEqual(3)
    expect(light.accent).toBe(dark.accent)
  })

  it('gives an account nine accent colors that hold 3:1 on every surface and stay apart', () => {
    // The server's closed list (modules/sessions/provider_account.go), in its order. The
    // dot is a non-text mark (SC 1.4.11): 3:1 on the four bases, selected, hovered and both.
    const NAMES = [
      'orange',
      'green',
      'amber',
      'red',
      'blue',
      'purple',
      'pink',
      'teal',
      'slate',
    ]
    for (const [themeName, t] of [
      ['light', light],
      ['dark', dark],
    ] as const) {
      // The theme holds these nine and no other, so a tenth cannot skip the checks below.
      expect(
        Object.keys(t)
          .filter((k) => k.startsWith('account-'))
          .map((k) => k.slice('account-'.length)),
      ).toEqual(NAMES)
      const surfaces = rowSurfaces(t)
      const weak: string[] = []
      for (const name of NAMES) {
        const dot = parseColor(t[`account-${name}`])
        for (const [bn, bv] of Object.entries(surfaces)) {
          const r = contrast(dot, bv)
          if (r < 3) weak.push(`${themeName} ${name} on ${bn}: ${show(r)}`)
        }
      }
      expect(weak).toEqual([])
      // Two dots of one list are told apart by color as well as by their label.
      for (let i = 0; i < NAMES.length; i++)
        for (let j = i + 1; j < NAMES.length; j++)
          expect(
            okDistance(
              parseColor(t[`account-${NAMES[i]}`]),
              parseColor(t[`account-${NAMES[j]}`]),
            ),
            `${themeName} ${NAMES[i]}/${NAMES[j]}`,
          ).toBeGreaterThanOrEqual(0.08)
    }
  })

  it('the chart fallback palette is the dark token set, field by field', () => {
    const MAP: Record<Exclude<keyof ChartTheme, 'series'>, string> = {
      text: 'foreground',
      mutedText: 'muted-foreground',
      grid: 'border',
      accent: 'accent',
      success: 'success',
      warning: 'warning',
      danger: 'danger',
      info: 'info',
      teal: 'confidence-attributed',
      slate: 'confidence-approximate',
      surface: 'surface',
      elevated: 'elevated',
      // The axis line is the strong line: chart-theme.ts reads --border-strong.
      border: 'border-strong',
    }
    // Coverage before values: no field of the type goes without a witness.
    expect(new Set(Object.keys(CHART_FALLBACK))).toEqual(
      new Set([...Object.keys(MAP), 'series']),
    )
    for (const [field, token] of Object.entries(MAP))
      expect(
        CHART_FALLBACK[field as keyof ChartTheme],
        `chart fallback \`${field}\` must equal --${token} in .dark`,
      ).toBe(dark[token])
    expect(CHART_FALLBACK.series).toEqual([
      dark.accent,
      dark['confidence-attributed'],
      dark.info,
      dark.warning,
      dark.success,
      theme['color-graphite-400'],
    ])
  })

  it('maps every color token to a Tailwind utility', () => {
    const colors = [
      ...Object.keys(PALETTE_DARK).filter((n) => !n.startsWith('shadow-')),
      ...Object.keys(ROLE_ALIASES).filter((n) => !n.startsWith('elev-')),
      ...Object.keys(OWN_DARK).filter((n) => !n.startsWith('elev-')),
      ...Object.keys(RUNTIME_LINES),
    ]
    for (const n of colors)
      expect(theme[`color-${n}`], `--color-${n}`).toBe(`var(--${n})`)
    for (const s of ['xs', 'sm', 'md', 'lg', 'xl'])
      expect(theme[`shadow-${s}`]).toBe(`var(--elev-${s})`)
  })

  it('sets text and figures in Adwaita Sans over Inter, code in Adwaita Mono', () => {
    expect(theme['font-sans']).toMatch(/^'Adwaita Sans', 'Inter', /)
    expect(theme['font-display']).toMatch(/^'Adwaita Sans', 'Inter', /)
    expect(theme['font-mono']).toMatch(/^'Adwaita Mono', /)
    expect(tokensCss).not.toContain('Geist')
  })

  it('has no glow: no ember, radial gradient, blur or halo token', () => {
    const css = tokensCss.replace(/\/\*[\s\S]*?\*\//g, '')
    for (const banned of [
      '--ember',
      'radial-gradient',
      'blur(',
      'glow',
      'halo',
    ])
      expect(css).not.toContain(banned)
  })

  it('has no glow or blur in any console source either', () => {
    // A blur, a glow shadow or a gradient halo written as a utility in a component
    // bypasses the tokens, so the sources are read too (tests excluded: they name the
    // classes they forbid).
    const GLOW =
      /backdrop-blur|backdrop-filter|\bblur-(?:none|xs|sm|md|lg|xl|2xl|3xl|\[)|drop-shadow|radial-gradient|var\(--ember\)|shadow-\[0_0_[1-9]/
    const hits = readdirSync('src', { recursive: true, encoding: 'utf8' })
      .filter((f) => /\.(tsx?|css)$/.test(f) && !/\.test\.tsx?$/.test(f))
      .flatMap((f) =>
        readFileSync(`src/${f}`, 'utf8')
          .split('\n')
          .flatMap((line, i) =>
            GLOW.test(line) ? [`src/${f}:${i + 1}: ${line.trim()}`] : [],
          ),
      )
    expect(hits).toEqual([])
  })

  it('gives each step of the type ladder the design value of its role', () => {
    // [step, size, line height, tracking, weight] — the design name of the role in the
    // comment. Steps without tracking or weight inherit the page's.
    const LADDER: [string, string, string, string?, string?][] = [
      ['hero', '2.25rem', '2.5rem', '-0.03em', '800'], // figure 36
      ['display-lg', '2.25rem', '2.5rem', '-0.03em', '800'], // figure 36
      ['display', '1.625rem', '2rem', '-0.022em', '800'], // title 26 (figures)
      ['title', '1.625rem', '2rem', '-0.022em', '800'], // title 26 (page title)
      ['heading', '1.0625rem', '1.375rem', '-0.01em', '700'], // head 17
      ['body-l', '0.9375rem', '1.5rem'], // body 15, a looser line
      ['body', '0.875rem', '1.25rem'], // body 14
      ['caption', '0.8125rem', '1.125rem'], // meta 13
      ['overline', '0.75rem', '1rem', undefined, '700'], // micro 12
      ['mono', '0.8125rem', '1.25rem'], // meta 13
      ['mono-s', '0.75rem', '1.125rem'], // micro 12
      ['metadata', '0.6875rem', '1rem'], // metadata 11
      ['metadata-s', '0.625rem', '0.875rem'], // counters and key hints 10
    ]
    for (const [step, size, lh, track, weight] of LADDER) {
      expect(theme[`text-${step}`], `--text-${step}`).toBe(size)
      expect(theme[`text-${step}--line-height`], step).toBe(lh)
      expect(theme[`text-${step}--letter-spacing`], step).toBe(track)
      expect(theme[`text-${step}--font-weight`], step).toBe(weight)
    }
    const steps = Object.keys(theme).filter(
      (k) => /^text-[\w-]+$/.test(k) && !k.includes('--'),
    )
    expect(new Set(steps)).toEqual(new Set(LADDER.map(([s]) => `text-${s}`)))
    // Nine sizes in all: the design's figure, title, head, body 15 and 14, meta and micro, then
    // the two metadata sizes below the micro step (see metadata-step.test.ts for where they go).
    expect(new Set(LADDER.map(([, size]) => size)).size).toBe(9)
  })

  it('uses the design radii for controls, cards and panels', () => {
    expect(theme['radius-ctl']).toBe('0.5625rem')
    expect(theme['radius-card']).toBe('0.75rem')
    expect(theme['radius-panel']).toBe('0.9375rem')
    expect(theme['radius-md']).toBe(theme['radius-ctl'])
    expect(theme['radius-lg']).toBe(theme['radius-card'])
    expect(theme['radius-xl']).toBe(theme['radius-panel'])
  })

  it('moves at the design pace, and only as far as reduced motion allows', () => {
    // The motion defaults are the :root block that declares them (the first :root of the
    // file is the light theme).
    const stripped = tokensCss.replace(/\/\*[\s\S]*?\*\//g, '')
    const blocks = [...stripped.matchAll(/(?:^|\n):root\s*\{([\s\S]*?)\n\}/g)]
    const motion = blocks.filter((m) => m[1].includes('--duration-fast'))
    expect(motion).toHaveLength(1)
    const base: Record<string, string> = {}
    for (const m of motion[0][1].matchAll(/--([\w-]+):\s*([^;]+);/g))
      base[m[1]] = norm(m[2])
    expect(Object.keys(base).sort()).toEqual(
      [
        'duration-fast',
        'duration-panel',
        'duration-sheet',
        'motion-shift',
      ].sort(),
    )
    expect(base['duration-fast']).toBe('160ms')
    expect(base['duration-panel']).toBe('200ms')
    expect(base['duration-sheet']).toBe('250ms')
    expect(base['motion-shift']).toBe('0.5rem')
    expect(theme['ease-out']).toBe('cubic-bezier(0.25, 0.46, 0.45, 0.94)')
    expect(theme['default-transition-timing-function']).toBe(theme['ease-out'])
    expect(theme['default-transition-duration']).toBe('var(--duration-fast)')
    // The operating-system preference and the console's own setting both stop movement.
    const still = {
      'duration-fast': '0ms',
      'duration-panel': '0ms',
      'duration-sheet': '0ms',
      'motion-shift': '0px',
    }
    expect(parseBlock(tokensCss, "[data-motion='reduce']")).toEqual(still)
    const media =
      /@media \(prefers-reduced-motion: reduce\)\s*\{\s*:root\s*\{([\s\S]*?)\}\s*\}/.exec(
        tokensCss.replace(/\/\*[\s\S]*?\*\//g, ''),
      )
    expect(media, 'the prefers-reduced-motion block').not.toBeNull()
    const inMedia: Record<string, string> = {}
    for (const m of media![1].matchAll(/--([\w-]+):\s*([^;]+);/g))
      inMedia[m[1]] = norm(m[2])
    expect(inMedia).toEqual(still)
  })

  it('switches density by spacing only: comfortable by default, compact on request', () => {
    const comfortable = parseBlock(
      tokensCss,
      ":root, [data-density='comfortable']",
    )
    expect(comfortable['density-row-list']).toBe('3.5rem') // 56 px
    expect(comfortable['density-row-table']).toBe('2.75rem') // 44 px
    expect(comfortable['density-control']).toBe('2.125rem') // 34 px
    expect(parseBlock(tokensCss, "[data-density='compact']")).toEqual({
      'density-row-list': '2.75rem', // 44 px
      'density-row-table': '2.25rem', // 36 px
      'density-control': '1.875rem', // 30 px
    })
    // Type does not change with density.
    expect(tokensCss.replace(/\/\*[\s\S]*?\*\//g, '')).not.toMatch(
      /\[data-density='compact'\][^{]*\{[^}]*--text-/,
    )
  })
})

describe('the Black theme: the dark theme on true black', () => {
  const dark = parseBlock(tokensCss, '.dark')
  const black = () => parseBlock(tokensCss, '.dark.black')

  it('is true black: #000 sheet, surfaces one step up, faint hairlines, #ededed text', () => {
    const t = black()
    expect(t.canvas).toBe('#000000')
    expect(t.frame).toBe('#000000')
    expect(t.surface).toBe('#0a0a0a')
    expect(t.raised).toBe('#111111')
    expect(t.line).toBe('#1f1f1f')
    expect(t['line-strong']).toBe('#262626')
    expect(t.text).toBe('#ededed')
    expect(t['text-2']).toBe('#8a8a8a')
    // The roles follow the black values, not the dark ones.
    expect(t.background).toBe(t.canvas)
    expect(t.foreground).toBe(t.text)
  })

  it('keeps the one brand orange and the status colours of the dark theme', () => {
    const t = black()
    for (const name of [
      'accent',
      'accent-text',
      'focus',
      'ok',
      'warn',
      'bad',
      'info',
    ])
      expect(t[name], `--${name}`).toBe(dark[name])
  })

  it('gives every role the black value of the token it aliases, and muted the selected fill', () => {
    const t = black()
    for (const [role, token] of Object.entries(ROLE_ALIASES))
      expect(t[role], `.dark.black --${role} = --${token}`).toBe(t[token])
    // The dark file states muted as a value, not an alias: Black must override it.
    expect(t.muted).toBe('#141414')
    expect(t.muted).toBe(t.active)
  })

  it("carries A2's seven new tokens with values of its own", () => {
    const t = black()
    expect({
      well: t.well,
      'shadow-well': t['shadow-well'],
      pane: t.pane,
      pressed: t.pressed,
      inverse: t.inverse,
      'on-inverse': t['on-inverse'],
      'inverse-accent': t['inverse-accent'],
    }).toEqual({
      well: '#000000',
      'shadow-well': 'inset 0 0 0 1px rgba(255, 255, 255, 0.1)',
      pane: '#050505',
      // Not the dark 16%: under it the muted text #8a8a8a is 3.76:1 on a pressed panel row.
      pressed: 'rgba(255, 255, 255, 0.1)',
      inverse: '#fafaf9',
      'on-inverse': '#111111',
      'inverse-accent': '#984700',
    })
  })

  it('declares exactly the names of the dark theme, so no style falls back to grey', () => {
    expect(new Set(Object.keys(black()))).toEqual(new Set(Object.keys(dark)))
  })

  it('passes the same 277 required contrast pairs as the other themes', () => {
    const pairs = requiredPairs('black', black())
    expect(pairs).toHaveLength(277)
    expect(
      pairs
        .filter((p) => p.ratio < p.required)
        .map((p) => `${p.fg} on ${p.bg}: ${show(p.ratio)} < ${p.required}`),
    ).toEqual([])
  })
})

// xterm 6 draws an opaque selection colour at 30 % over the terminal's background and leaves
// the text its own colour. An opaque soft fill all but vanishes at 30 %; the orange holds, as
// the console's own ::selection (28 % of the accent, src/index.css) does.
describe("the terminal's selection stays visible on every theme", () => {
  it.each([
    ['light', ':root'],
    ['dark', '.dark'],
    ['black', '.dark.black'],
  ])(
    '%s: the accent, drawn at 30 %%, holds 1.3:1 on the background',
    (_name, selector) => {
      const t = parseBlock(tokensCss, selector)
      vi.stubGlobal('getComputedStyle', () => ({
        getPropertyValue: (name: string) => t[name.slice(2)] ?? '',
      }))
      const term = terminalTheme()
      vi.unstubAllGlobals()
      expect(term.selectionBackground).toBe(t.accent)
      const [r, g, b, a] = parseColor(term.selectionBackground ?? '')
      const bg = parseColor(term.background ?? '')
      const drawn = over([r, g, b, a === 1 ? 0.3 : a], bg)
      const selection = contrast(drawn, bg)
      expect(selection, show(selection)).toBeGreaterThanOrEqual(1.3)
      const text = contrast(parseColor(term.foreground ?? ''), drawn)
      expect(text, show(text)).toBeGreaterThanOrEqual(4.5)
    },
  )
})
