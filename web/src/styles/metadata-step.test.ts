// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// WHERE THE METADATA STEP MAY BE USED.
//
// The type ladder ends in two small sizes: `text-metadata` (11 px) for metadata and
// `text-metadata-s` (10 px) for counters and key hints. They are the quiet end of the
// ladder, and a size that small is only calm while it stays rare: the moment a sentence a
// person has to read is set in it, the console is dense in the wrong way. So the files that
// may use them are written down here, each with its reason. A new use is a row added in a
// review, never a class added in passing.
//
// Comments are stripped before the scan (the census's `stripComments`): the doc comments
// that explain the step quote its name.
/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { relative, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { stripComments, walk } from '@/test/empty-state-census'

const SRC = resolve(__dirname, '..')

type Step = 'metadata' | 'metadata-s'

/** The files that may use a step, relative to `web/src`, with the steps and the reason. */
export const MAY_USE: Readonly<
  Record<string, { steps: readonly Step[]; why: string }>
> = {
  'components/ui/count-badge.tsx': {
    steps: ['metadata-s'],
    why: 'a counter',
  },
  'components/ui/kbd.tsx': { steps: ['metadata-s'], why: 'a key hint' },
  'components/ui/command.tsx': {
    steps: ['metadata-s'],
    why: "the palette's key hint",
  },
  'components/layout/status-bar.tsx': {
    steps: ['metadata'],
    why: "the status bar's secondary facts",
  },
  'features/home/now-queue.tsx': {
    steps: ['metadata-s'],
    why: 'the count beside the Needs you heading',
  },
  'features/api-playground/api-playground-view.tsx': {
    steps: ['metadata-s'],
    why: 'the response-history counter',
  },
  'features/access-map/edges.tsx': {
    steps: ['metadata-s'],
    why: 'the access-edge token counter',
  },
  'features/home/live-cards.tsx': {
    steps: ['metadata'],
    why: "the live card's facts line",
  },
}

/** A step name as a class: not part of a longer name, with or without a variant prefix. */
const STEP_CLASS = /(?<![\w-])text-(metadata-s|metadata)(?![\w-])/g

/** The steps a source file names as classes, comments aside. */
export function stepsUsed(source: string): Set<Step> {
  const found = new Set<Step>()
  for (const m of stripComments(source).matchAll(STEP_CLASS))
    found.add(m[1] as Step)
  return found
}

/** Raw 10 and 11 px sizes: the values the two steps stand for. */
const RAW_SMALL = /text-\[(?:10|11)px\]|text-\[0?\.(?:625|6875)rem\]/

const sources = walk(SRC, ['.ts', '.tsx']).filter(
  (f) => !/\.test\.tsx?$/.test(f),
)

describe('the metadata step', () => {
  it('is used only by the files listed above, and only the step each is listed for', () => {
    const misuse: string[] = []
    for (const file of sources) {
      const rel = relative(SRC, file).split('\\').join('/')
      const used = stepsUsed(readFileSync(file, 'utf8'))
      if (used.size === 0) continue
      const allowed = MAY_USE[rel]?.steps ?? []
      for (const step of used)
        if (!allowed.includes(step)) misuse.push(`${rel}: text-${step}`)
    }
    expect(misuse).toEqual([])
  })

  it('is used by every file that lists it, so a row cannot outlive its use', () => {
    const missing: string[] = []
    for (const [rel, { steps }] of Object.entries(MAY_USE)) {
      const used = stepsUsed(readFileSync(resolve(SRC, rel), 'utf8'))
      for (const step of steps)
        if (!used.has(step)) missing.push(`${rel}: text-${step}`)
    }
    expect(missing).toEqual([])
  })

  it('is written as the step, not as the pixel size it stands for, in those files', () => {
    const raw = Object.keys(MAY_USE).filter((rel) =>
      RAW_SMALL.test(stripComments(readFileSync(resolve(SRC, rel), 'utf8'))),
    )
    expect(raw).toEqual([])
  })

  it('sets every Settings key hint through Kbd', () => {
    const source = readFileSync(resolve(SRC, 'app/pages/settings.tsx'), 'utf8')
    expect(source).not.toMatch(/<kbd\b/)
    expect(source.match(/<Kbd\b/g)).toHaveLength(2)
  })

  it('sets the remaining count pills in the counter step', () => {
    for (const rel of [
      'features/api-playground/api-playground-view.tsx',
      'features/access-map/edges.tsx',
    ]) {
      const source = readFileSync(resolve(SRC, rel), 'utf8')
      expect(source, rel).toContain('text-metadata-s')
      expect(source, rel).not.toMatch(RAW_SMALL)
    }
  })

  it('sets no key cap and no counter in the machine face: neither is code', () => {
    for (const rel of [
      'components/ui/kbd.tsx',
      'components/ui/count-badge.tsx',
    ])
      expect(
        stripComments(readFileSync(resolve(SRC, rel), 'utf8')),
        rel,
      ).not.toMatch(/(?<![\w-])font-mono(?![\w-])/)
  })

  it('finds a use in code and ignores the prose that names it', () => {
    // The control positive: without it a scanner that matches nothing reads as clean.
    expect(
      stepsUsed(
        [
          '// the old text-metadata-s note',
          '/* and text-metadata here */',
          "const a = cn('md:text-metadata-s', 'text-metadata')",
          "const b = 'text-metadatafoo text-metadata-sx'",
        ].join('\n'),
      ),
    ).toEqual(new Set(['metadata-s', 'metadata']))
    expect(stepsUsed('// text-metadata only in a comment')).toEqual(new Set())
  })

  it('scans a real number of files, so an empty sweep cannot read as clean', () => {
    expect(sources.length).toBeGreaterThan(500)
  })
})
