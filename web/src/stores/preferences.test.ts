// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// DENSITY_ROW is the one place the density preference becomes a dimension; the
// virtualizer estimates with `px` while the DOM gets `className`. A drift between
// the two would make the scroll position lie until each row is measured, so the
// pair is pinned here rather than trusted.
import { describe, expect, it } from 'vitest'
import {
  DENSITY_ROW,
  densityHeightToken,
  usePreferencesStore,
  type Density,
} from './preferences'

describe('DENSITY_ROW', () => {
  it('maps each density to a spacing-scale height whose pixel value is the estimate', () => {
    for (const [density, row] of Object.entries(DENSITY_ROW)) {
      const token = densityHeightToken(density as Density)
      expect(row.className.split(' '), density).toContain(token)
      const step = Number(token.replace(/^h-/, ''))
      expect(Number.isInteger(step), density).toBe(true)
      // Tailwind spacing scale: 1 step = 0.25 rem = 4 px at the 16 px root.
      expect(row.px, density).toBe(step * 4)
    }
  })

  it('makes the header strip one row tall INCLUDING the rule it carries', () => {
    for (const [density, row] of Object.entries(DENSITY_ROW)) {
      // `border-collapse` draws the hairline under the header inside the strip's own
      // box, so a header given the row's height measures one pixel more than a row.
      expect(row.headPx, density).toBe(row.px - 1)
      expect(row.headClassName, density).toBe(`h-[${row.headPx}px]`)
    }
  })

  it('gives comfortable rows vertical padding and compact rows none', () => {
    expect(DENSITY_ROW.comfortable.className.split(' ')).toContain('py-1')
    expect(DENSITY_ROW.compact.className.split(' ')).toContain('py-0')
  })

  it('keeps compact strictly denser than comfortable and both above the pointer-target floor', () => {
    expect(DENSITY_ROW.compact.px).toBeLessThan(DENSITY_ROW.comfortable.px)
    // WCAG 2.5.8 Target Size (Minimum): 24 × 24 CSS px.
    expect(DENSITY_ROW.compact.px).toBeGreaterThanOrEqual(24)
  })

  it('defaults the persisted preference to comfortable', () => {
    expect(usePreferencesStore.getState().density).toBe('comfortable')
  })
})

describe('the navigation fold and the persisted blob (0.5)', () => {
  it('folds and opens the left navigation without touching density', () => {
    usePreferencesStore.setState({ density: 'compact', navCollapsed: false })
    usePreferencesStore.getState().setNavCollapsed(true)
    expect(usePreferencesStore.getState().navCollapsed).toBe(true)
    expect(usePreferencesStore.getState().density).toBe('compact')
    usePreferencesStore.getState().setNavCollapsed(false)
    usePreferencesStore.setState({ density: 'comfortable' })
  })

  it('rehydrates the rest of the blob and drops the retired navigation fields', () => {
    const merge = usePreferencesStore.persist.getOptions().merge as unknown as (
      persisted: unknown,
      current: unknown,
    ) => Record<string, unknown>
    expect(typeof merge).toBe('function')
    const current = usePreferencesStore.getState()
    const merged = merge(
      {
        density: 'compact',
        reduceMotion: true,
        collapsedNavGroups: ['operate'],
        sidebarCollapsed: true,
        sidebarChoices: {
          'user:fixture-a': { collapsed: true, pending: true },
        },
        sidebarUnsent: true,
        navAreas: { v: 1, open: ['system'], closed: ['ai'] },
        navCollapsed: 'yes',
      },
      current,
    )
    expect(merged.density).toBe('compact')
    expect(merged.reduceMotion).toBe(true)
    expect(merged.collapsedNavGroups).toEqual(['operate'])
    // Anything but `true` keeps the navigation open.
    expect(merged.navCollapsed).toBe(false)
    for (const retired of [
      'sidebarCollapsed',
      'sidebarChoices',
      'sidebarUnsent',
      'navAreas',
    ])
      expect(retired in merged, retired).toBe(false)
    expect(merge({ navCollapsed: true }, current).navCollapsed).toBe(true)
  })
})

describe('the title style (0.5)', () => {
  it('defaults to the engine’s own default and keeps the one chosen', () => {
    usePreferencesStore.setState({ titleStyle: 'sentence' })
    expect(usePreferencesStore.getState().titleStyle).toBe('sentence')
    usePreferencesStore.getState().setTitleStyle('short')
    expect(usePreferencesStore.getState().titleStyle).toBe('short')
  })

  it('takes the default for anything a stored blob holds that the engine does not know', () => {
    const merge = usePreferencesStore.persist.getOptions().merge as unknown as (
      persisted: unknown,
      current: unknown,
    ) => Record<string, unknown>
    const current = usePreferencesStore.getState()
    expect(merge({ titleStyle: 'poetic' }, current).titleStyle).toBe('sentence')
    expect(merge({ titleStyle: 7 }, current).titleStyle).toBe('sentence')
    expect(merge({}, current).titleStyle).toBe('sentence')
    expect(merge({ titleStyle: 'long' }, current).titleStyle).toBe('long')
  })
})
