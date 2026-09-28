// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The console design tokens are GENERATED from the DTCG sources (web/tokens/*.tokens.json)
// into ./tokens.css. This test pins the generated file to the v26.10 visual system in BOTH
// themes, so a token edit can never shift it silently:
//   - the palette: four layered neutrals (frame, canvas, surface, raised), three text tones,
//     the single brand orange #f08000 and its roles, the status colors and their soft fills;
//   - the role names the components use today, each equal to the palette token it aliases;
//   - the contrast contract of the palette, measured the way the design measures it: every
//     text token on 22 backgrounds per theme (the four bases, each base under the selection
//     fill, and canvas and surface under each of the seven soft fills), control boundaries,
//     focus and the selection border at 3:1, the ink on the orange at 4.5:1 — 372 pairs;
//   - the type ladder, the font stacks, radii, density and motion.
/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

import {
  FALLBACK as CHART_FALLBACK,
  type ChartTheme,
} from '@/components/charts/chart-theme'

// vitest runs with cwd = web/; read the committed generated stylesheet.
const tokensCss = readFileSync('src/styles/tokens.css', 'utf8')

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
const PALETTE_DARK: Record<string, string> = {
  frame: '#0a0a0c',
  canvas: '#111113',
  surface: '#17171a',
  raised: '#1d1d21',
  hover: 'rgba(255, 255, 255, 0.045)',
  active: 'rgba(255, 255, 255, 0.08)',
  line: 'rgba(255, 255, 255, 0.08)',
  'line-strong': 'rgba(255, 255, 255, 0.14)',
  'ctl-border': '#6e6e72',
  scrim: 'rgba(5, 5, 6, 0.62)',
  text: '#f3f2ef',
  'text-2': '#b0aea8',
  'text-3': '#9a9892',
  accent: '#f08000',
  'on-accent': '#1a1206',
  'accent-text': '#ff9b3d',
  'accent-soft': 'rgba(240, 128, 0, 0.13)',
  'accent-line': 'rgba(240, 128, 0, 0.42)',
  'accent-border': '#f08000',
  focus: '#ffb366',
  ok: '#52c98f',
  'ok-soft': 'rgba(82, 201, 143, 0.12)',
  warn: '#e8c14a',
  'warn-soft': 'rgba(232, 193, 74, 0.12)',
  bad: '#f47a70',
  'bad-soft': 'rgba(244, 122, 112, 0.12)',
  info: '#72aef5',
  'info-soft': 'rgba(114, 174, 245, 0.12)',
  'diff-add': 'rgba(82, 201, 143, 0.13)',
  'diff-del': 'rgba(244, 122, 112, 0.13)',
  'shadow-pop':
    '0 18px 48px -12px rgba(0, 0, 0, 0.72), 0 2px 6px rgba(0, 0, 0, 0.4)',
  'shadow-card': 'none',
  ember:
    'radial-gradient(720px 260px at 50% -110px, rgba(240, 128, 0, 0.12), transparent 70%)',
}

const PALETTE_LIGHT: Record<string, string> = {
  frame: '#efeee9',
  canvas: '#ffffff',
  surface: '#f8f7f4',
  raised: '#ffffff',
  hover: 'rgba(20, 18, 14, 0.04)',
  active: 'rgba(20, 18, 14, 0.075)',
  line: 'rgba(20, 18, 14, 0.09)',
  'line-strong': 'rgba(20, 18, 14, 0.16)',
  'ctl-border': '#8a8883',
  scrim: 'rgba(26, 25, 23, 0.32)',
  text: '#1a1917',
  'text-2': '#4a4742',
  'text-3': '#5f5c55',
  accent: '#f08000',
  'on-accent': '#1a1206',
  'accent-text': '#9c4a00',
  'accent-soft': 'rgba(240, 128, 0, 0.12)',
  'accent-line': 'rgba(194, 96, 0, 0.45)',
  'accent-border': '#c26000',
  focus: '#b45500',
  ok: '#18693f',
  'ok-soft': 'rgba(24, 105, 63, 0.09)',
  warn: '#7d5400',
  'warn-soft': 'rgba(125, 84, 0, 0.09)',
  bad: '#a8231c',
  'bad-soft': 'rgba(168, 35, 28, 0.08)',
  info: '#1c58a3',
  'info-soft': 'rgba(28, 88, 163, 0.08)',
  'diff-add': 'rgba(29, 122, 75, 0.1)',
  'diff-del': 'rgba(179, 38, 30, 0.09)',
  'shadow-pop':
    '0 18px 48px -14px rgba(30, 24, 16, 0.28), 0 2px 6px rgba(30, 24, 16, 0.08)',
  'shadow-card': '0 1px 2px rgba(30, 24, 16, 0.05)',
  ember:
    'radial-gradient(720px 260px at 50% -110px, rgba(240, 128, 0, 0.09), transparent 70%)',
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
  'elev-xs': 'shadow-card',
  'elev-sm': 'shadow-card',
  'elev-md': 'shadow-card',
  'elev-lg': 'shadow-pop',
  'elev-xl': 'shadow-pop',
}

// Roles the palette does not define. Each keeps a measured value, and the checks below hold
// it to the same contrast rule as the palette.
const OWN_DARK: Record<string, string> = {
  // The solid well: the selection fill (`active`) painted on the canvas, as one opaque color.
  muted: '#242426',
  // The primary button's hover: the orange at brightness 1.06, as the design renders it.
  'accent-hover': '#fe8800',
  'accent-active': '#d87000',
  'danger-solid': '#c5362f',
  'danger-solid-foreground': '#fff1ef',
  'confidence-attributed': '#5be0d8',
  'confidence-approximate': '#9aa3b0',
}
const OWN_LIGHT: Record<string, string> = {
  muted: '#ededed',
  'accent-hover': '#fe8800',
  'accent-active': '#cc6a00',
  'danger-solid': '#b0201b',
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

interface Pair {
  theme: string
  fg: string
  bg: string
  ratio: number
  required: number
}

/** The design's contrast method: 186 required pairs per theme. */
function requiredPairs(theme: string, t: Record<string, string>): Pair[] {
  const c = (n: string) => parseColor(t[n])
  const bases: Record<string, RGBA> = {}
  for (const b of ['canvas', 'surface', 'raised', 'frame']) bases[b] = c(b)
  const bgs: Record<string, RGBA> = { ...bases }
  for (const b of ['canvas', 'surface', 'raised', 'frame'])
    bgs[`${b}+selected`] = over(c('active'), bases[b])
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
        ratio: contrast(c(fg), bv),
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
  return pairs
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

  it('passes all 372 required contrast pairs (186 per theme)', () => {
    const pairs = [
      ...requiredPairs('dark', dark),
      ...requiredPairs('light', light),
    ]
    expect(pairs).toHaveLength(372)
    const failing = pairs
      .filter((p) => p.ratio < p.required)
      .map(
        (p) =>
          `${p.theme} ${p.fg} on ${p.bg}: ${show(p.ratio)} < ${p.required}`,
      )
    expect(failing).toEqual([])
  })

  it('reproduces the minimums the design measured', () => {
    // The worst background per text token (DESIGN §3.3.2): a drift of the method, not only
    // of a value, turns this red.
    const min = (t: string, fg: string) =>
      Math.min(
        ...requiredPairs(t, t === 'dark' ? dark : light)
          .filter((p) => p.fg === fg)
          .map((p) => p.ratio),
      )
    const MEASURED: [string, string, number][] = [
      ['dark', 'text', 11.9],
      ['light', 'text', 12.98],
      ['dark', 'text-2', 6.0],
      ['light', 'text-2', 6.83],
      ['dark', 'text-3', 4.62],
      ['light', 'text-3', 4.93],
      ['dark', 'accent-text', 6.35],
      ['light', 'accent-text', 4.57],
      ['dark', 'ok', 6.41],
      ['light', 'ok', 4.96],
      ['dark', 'warn', 7.71],
      ['light', 'warn', 4.94],
      ['dark', 'bad', 4.99],
      ['light', 'bad', 5.31],
      ['dark', 'info', 5.76],
      ['light', 'info', 5.22],
      ['dark', 'ctl-border', 3.31],
      ['light', 'ctl-border', 3.31],
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
      const well = parseColor(t.muted)
      for (const fg of ['foreground', 'muted-foreground', 'accent-text']) {
        const r = contrast(parseColor(t[fg]), well)
        expect(
          r,
          `${name}: --${fg} on --muted is ${show(r)}`,
        ).toBeGreaterThanOrEqual(4.5)
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
    expect(r).toBeCloseTo(2.69, 2)
    expect(
      contrast(parseColor(light['accent-border']), parseColor(light.canvas)),
    ).toBeGreaterThanOrEqual(3)
    expect(light.accent).toBe(dark.accent)
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
      border: 'border',
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
      ...Object.keys(PALETTE_DARK).filter(
        (n) => !['shadow-pop', 'shadow-card', 'ember'].includes(n),
      ),
      ...Object.keys(ROLE_ALIASES).filter((n) => !n.startsWith('elev-')),
      ...Object.keys(OWN_DARK),
      ...Object.keys(RUNTIME_LINES),
    ]
    for (const n of colors)
      expect(theme[`color-${n}`], `--color-${n}`).toBe(`var(--${n})`)
    for (const s of ['xs', 'sm', 'md', 'lg', 'xl'])
      expect(theme[`shadow-${s}`]).toBe(`var(--elev-${s})`)
  })

  it('sets the interface in Geist and machine text in JetBrains Mono', () => {
    expect(theme['font-sans']).toMatch(/^'Geist', /)
    expect(theme['font-mono']).toMatch(/^'JetBrains Mono', /)
    // One interface family: weight carries the hierarchy, not a second face.
    expect(theme['font-display']).toBe(theme['font-sans'])
    for (const old of ['Inter', 'Space Grotesk'])
      expect(tokensCss).not.toContain(old)
  })

  it('gives each step of the type ladder the design value of its role', () => {
    // [step, size, line height, tracking, weight] — the design name of the role in the
    // comment. Steps without tracking or weight inherit the page's.
    const LADDER: [string, string, string, string?, string?][] = [
      ['hero', '2.5rem', '2.75rem', '-0.035em', '600'], // hero 40/44
      ['display-lg', '1.875rem', '2.25rem', '-0.03em', '600'], // display 30/36
      ['display', '1.375rem', '1.75rem', '-0.02em', '600'], // title-l 22/28 (figures)
      ['title', '1.375rem', '1.75rem', '-0.02em', '600'], // title-l 22/28 (page title)
      ['heading', '1rem', '1.375rem', '-0.01em', '600'], // title 16/22
      ['body-l', '0.9375rem', '1.5rem'], // body-l 15/24
      ['body', '0.875rem', '1.25rem'], // body 14/20
      ['caption', '0.8125rem', '1.125rem'], // small 13/18
      ['overline', '0.75rem', '1rem', undefined, '500'], // micro 12/16
      ['mono', '0.8125rem', '1.25rem'], // mono 13/20
      ['mono-s', '0.75rem', '1.125rem'], // mono-s 12/18
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
  })

  it('uses the design radii for controls, cards and panels', () => {
    expect(theme['radius-ctl']).toBe('0.4375rem')
    expect(theme['radius-card']).toBe('0.625rem')
    expect(theme['radius-panel']).toBe('0.875rem')
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
    expect(base['duration-fast']).toBe('120ms')
    expect(base['duration-panel']).toBe('180ms')
    expect(base['duration-sheet']).toBe('240ms')
    expect(base['motion-shift']).toBe('0.375rem')
    expect(theme['ease-out']).toBe('cubic-bezier(0.2, 0.8, 0.2, 1)')
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
    expect(comfortable['density-row-list']).toBe('3.25rem') // 52 px
    expect(comfortable['density-row-table']).toBe('2.75rem') // 44 px
    expect(comfortable['density-control']).toBe('2rem') // 32 px
    expect(parseBlock(tokensCss, "[data-density='compact']")).toEqual({
      'density-row-list': '2.5rem', // 40 px
      'density-row-table': '2.25rem', // 36 px
      'density-control': '1.75rem', // 28 px
    })
    // Type does not change with density.
    expect(tokensCss.replace(/\/\*[\s\S]*?\*\//g, '')).not.toMatch(
      /\[data-density='compact'\][^{]*\{[^}]*--text-/,
    )
  })
})
