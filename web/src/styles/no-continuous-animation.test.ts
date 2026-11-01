// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// NO CONTINUOUSLY REPAINTING ANIMATION (#3135).
//
// A panel that pulses, a badge that bounces or a glow that breathes repaints the screen for
// as long as the page is open: it keeps the GPU busy to say nothing. The console's motion is a
// transition that ends. The one animation allowed to loop is a loader, and only while there
// is something to wait for:
//   - the `Spinner` primitive;
//   - a `Loader2` or `LoaderCircle` icon, which a screen renders while a request is pending;
//   - a class behind a condition, `busy && 'animate-spin'`, which stops with the wait.
// Everything else that loops is a defect: an `infinite` animation in a stylesheet, the
// `animate-pulse`, `animate-ping` and `animate-bounce` utilities, a loop written in a style
// object or a motion option, and an `animate-spin` on anything else that is always on.
//
// The scan strips comments first (the census's `stripComments`): the doc comments that explain
// the rule quote the words it looks for. Its control cases below plant each defect and the
// allowed forms, so a scanner that stopped finding things cannot read as a clean tree.
/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { relative, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { stripComments, walk } from '@/test/empty-state-census'

const SRC = resolve(__dirname, '..')

/** The one file whose job is to spin: the loader primitive. */
const LOADER_FILES = ['components/ui/spinner.tsx']

/** Icons that are a loader by their name. */
const LOADER_ICONS = new Set(['Loader2', 'LoaderCircle'])

/** Utilities and options that loop for ever, whatever they are put on. */
const ALWAYS_LOOPING: Array<[RegExp, string]> = [
  [/(?<![\w-])animate-(?:pulse|ping|bounce)(?![\w-])/g, 'a looping utility'],
  [/animation-iteration-count\s*:\s*infinite/g, 'an infinite iteration count'],
  [
    /animationIterationCount\s*:\s*['"`]infinite/g,
    'an infinite iteration count',
  ],
  [/\banimation\s*:[^;{}]*\binfinite\b/g, 'an infinite animation'],
  [
    /animate-\[[^\]]*(?<![A-Za-z])infinite(?![A-Za-z])[^\]]*\]/g,
    'an infinite arbitrary animation',
  ],
  [/\brepeat\s*:\s*(?:Number\.POSITIVE_INFINITY|Infinity)\b/g, 'a motion loop'],
  [
    /--animate-[\w-]+\s*:[^;{}]*(?<![A-Za-z])infinite(?![A-Za-z])/g,
    'an infinite theme animation',
  ],
  [/repeatCount\s*=\s*["']indefinite["']/g, 'an indefinite SVG animation'],
]

const SPIN = /(?<![\w-])animate-spin(?![\w-])/g

/** The JSX tag that opens at or before `index`, or null outside a tag. */
function enclosingTag(source: string, index: number): string | null {
  const open = source.lastIndexOf('<', index)
  if (open === -1) return null
  const name = /^<([A-Za-z][\w.]*)/.exec(source.slice(open, index))
  return name ? name[1] : null
}

/** The loops a source file starts that are not a loader waiting, as `line: what`. */
export function foreverAnimations(rawSource: string, file: string): string[] {
  const found: string[] = []
  const source = stripComments(rawSource)
  const lineOf = (i: number) => source.slice(0, i).split('\n').length
  for (const [pattern, what] of ALWAYS_LOOPING)
    for (const m of source.matchAll(pattern))
      found.push(`${file}:${lineOf(m.index)}: ${what}`)
  if (LOADER_FILES.includes(file)) return found
  for (const m of source.matchAll(SPIN)) {
    const tag = enclosingTag(source, m.index)
    const behindCondition = /(?:&&|\?)\s*['"`]\s*(?:[\w:[\]-]+\s+)*$/.test(
      source.slice(Math.max(0, m.index - 80), m.index),
    )
    if (behindCondition || (tag && LOADER_ICONS.has(tag))) continue
    found.push(`${file}:${lineOf(m.index)}: animate-spin that is always on`)
  }
  return found
}

const sources = walk(SRC, ['.ts', '.tsx', '.css']).filter(
  (f) => !/\.test\.tsx?$/.test(f),
)

describe('no animation repaints for ever', () => {
  it('starts no loop outside a loader that is waiting', () => {
    const loops = sources.flatMap((f) =>
      foreverAnimations(
        readFileSync(f, 'utf8'),
        relative(SRC, f).split('\\').join('/'),
      ),
    )
    expect(loops).toEqual([])
  })

  it('finds each kind of loop, and leaves the allowed loaders alone', () => {
    const flagged = (code: string, file = 'features/x/x.tsx') =>
      foreverAnimations(code, file).length
    // Defects.
    expect(flagged(`<div className="animate-pulse bg-surface" />`)).toBe(1)
    expect(flagged(`<span className="animate-ping" />`)).toBe(1)
    expect(flagged(`<b className={'animate-bounce'} />`)).toBe(1)
    expect(flagged(`<RefreshCw className="size-4 animate-spin" />`)).toBe(1)
    expect(
      flagged(`<div className="animate-[wiggle_1s_ease_infinite]" />`),
    ).toBe(1)
    expect(flagged(`const s = { animationIterationCount: 'infinite' }`)).toBe(1)
    expect(flagged(`animate(x, { repeat: Infinity })`)).toBe(1)
    expect(flagged(`animate(x, { repeat: Number.POSITIVE_INFINITY })`)).toBe(1)
    expect(
      flagged(`:root { --animate-glow: glow 2s ease infinite; }`, 'tokens.css'),
    ).toBe(1)
    expect(
      flagged(`<animate attributeName="r" repeatCount="indefinite" />`),
    ).toBe(1)
    // An apostrophe in JSX text does not hide what follows it on the next line.
    expect(
      flagged(
        `<p>Don't wait</p>\n<a href="https://x" className="animate-pulse" />`,
      ),
    ).toBe(1)
    expect(
      flagged(
        `.glow { animation: breathe 2s ease-in-out infinite; }`,
        'index.css',
      ),
    ).toBe(1)
    // Allowed: a loader, and a spin that stops with its condition.
    expect(flagged(`<Loader2 aria-hidden className="animate-spin" />`)).toBe(0)
    expect(flagged(`<LoaderCircle className="size-4 animate-spin" />`)).toBe(0)
    expect(
      flagged(
        `<RefreshCw className={cn('size-3.5', query.isFetching && 'animate-spin')} />`,
      ),
    ).toBe(0)
    expect(flagged(`const c = busy ? 'animate-spin' : ''`)).toBe(0)
    expect(
      flagged(`const c = 'animate-spin'`, 'components/ui/spinner.tsx'),
    ).toBe(0)
    // A transition that ends is not a loop, and prose about a loop is not one.
    expect(
      flagged(
        `.in { animation: enter 180ms var(--ease-out) both; }`,
        'index.css',
      ),
    ).toBe(0)
    expect(flagged(`// no animate-pulse here\n/* nor animate-ping */`)).toBe(0)
  })

  it('scans a real number of files, so an empty sweep cannot read as clean', () => {
    expect(sources.length).toBeGreaterThan(500)
    expect(sources.some((f) => f.endsWith('index.css'))).toBe(true)
  })
})
