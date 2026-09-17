// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// D21 — the empty-state census, driven by MUTANTS before it is believed about the tree.
//
// D15 published a list of 94 `<EmptyState>` call sites carrying neither a description
// nor an action. A list is a starting point, not a guarantee: the 95th is written the
// day after the census. `src/test/empty-state-census.ts` turns it into an instrument,
// and this file is the battery that decides whether the instrument can fail at all.
//
// The mutants run FIRST for a reason: a scanner that miscounts is worse than no
// scanner, because its zero would be believed. The tree-wide assertion follows them.
/// <reference types="node" />
import { describe, expect, it } from 'vitest'
import {
  bareStates,
  censusEmptyStates,
  describesNothing,
  endOfOpeningTag,
  stripComments,
  topLevelProps,
} from '@/test/empty-state-census'

describe('the empty-state census instrument (mutants first)', () => {
  it('finds the end of an opening tag past a nested `>` inside an expression', () => {
    // The mutant that matters: React code writes `>` inside JSX expressions all the
    // time (`count > 0 ? …`). A scanner that stops at the first `>` would cut the
    // attribute list in half and report a described state as bare.
    const src = `<EmptyState title={n > 0 ? a : b} description={d} />`
    const end = endOfOpeningTag(src, 0)
    expect(end).toBe(src.length)
    expect(topLevelProps(src.slice('<EmptyState'.length, end - 1))).toEqual([
      'title',
      'description',
    ])
  })

  it('ignores a `>` inside a string attribute', () => {
    const src = `<EmptyState title="a > b" description={d} />x`
    expect(endOfOpeningTag(src, 0)).toBe(src.length - 1)
  })

  it('reports an unterminated tag instead of guessing', () => {
    expect(endOfOpeningTag('<EmptyState title={a', 0)).toBe(-1)
  })

  it('does not read prop names out of nested elements', () => {
    // `action={<Button onClick={…} title="x" />}` must contribute `action` and
    // NOTHING else: a nested `title` would make a bare state look described.
    const attrs = ` title={t('x')} action={<Button title="y" onClick={go} />} `
    expect(topLevelProps(attrs)).toEqual(['title', 'action'])
  })

  it('sees an empty description in both forms a human writes', () => {
    expect(describesNothing(` title={t} description="" `)).toBe(true)
    expect(describesNothing(` title={t} description={''} `)).toBe(true)
    expect(describesNothing(` title={t} description={t('x')} `)).toBe(false)
  })

  it('blanks comments but keeps every line number, so prose is never counted', () => {
    // MEASURED, not imagined: the first run of this census reported two bare empty
    // states inside the doc comment of `empty-state.tsx` that explains the rule.
    const src = [
      'const a = 1',
      '/* <EmptyState title={x} /> in prose */',
      "const url = 'https://example.invalid/x' // <EmptyState />",
      '<EmptyState title={y} description={z} />',
    ].join('\n')
    const out = stripComments(src)
    expect(out.split('\n')).toHaveLength(4)
    expect(out.split('\n')[3]).toBe(src.split('\n')[3])
    expect(out.match(/<EmptyState/g)).toHaveLength(1)
    // The `//` inside the string literal is NOT a comment, so the line survives.
    expect(out).toContain("'https://example.invalid/x'")
  })
})

describe('the console has no bare empty state', () => {
  const census = censusEmptyStates('src')

  it('parsed every `<EmptyState>` opening tag it found', () => {
    // A tag the scanner could not close is a failure of the INSTRUMENT, and it fails
    // the gate rather than shrinking the denominator in silence.
    expect(census.unparsed).toEqual([])
  })

  it('every empty state says what the surface will show', () => {
    const bare = bareStates(census).map((s) => `${s.file}:${s.line}`)
    // The message carries the LIST, never just the count: CLAUDE.md's backlog rule is
    // that a row demanding work names file:line, and this is that row.
    expect(bare, `bare empty states:\n${bare.join('\n')}`).toEqual([])
  })

  it('is still looking at the whole console, not at a shrunken tree', () => {
    // Non-vacuity, in the direction that matters. A refactor that renamed the
    // component, or a walk that stopped recursing, would make the assertion above
    // pass by measuring nothing. 306 call sites were found on 2026-09-17; the floor
    // is deliberately far below that, because this guards against zero, not drift.
    expect(census.sites.length).toBeGreaterThan(200)
  })
})
