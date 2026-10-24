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
  AREA_EXPANSION_VERSION,
  DEFAULT_AREA_EXPANSION,
  DENSITY_ROW,
  densityHeightToken,
  isAreaExpansion,
  isAreaOpen,
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

describe('area expansion (N1)', () => {
  const reset = () =>
    usePreferencesStore.setState({ navAreas: DEFAULT_AREA_EXPANSION })

  it('starts closed except for the active area, which opens on arrival', () => {
    reset()
    const s = usePreferencesStore.getState()
    expect(s.navAreas).toEqual({
      v: AREA_EXPANSION_VERSION,
      open: [],
      closed: [],
    })
    expect(isAreaOpen(s.navAreas, 'ai', null)).toBe(false)
    expect(isAreaOpen(s.navAreas, 'ai', 'ai')).toBe(true)
    expect(isAreaOpen(s.navAreas, 'system', 'ai')).toBe(false)
  })

  it('lets the operator hold several areas open, and fold the active one without leaving it', () => {
    reset()
    const s = usePreferencesStore.getState()
    s.setAreaOpen('system', true)
    s.setAreaOpen('automation', true)
    expect(usePreferencesStore.getState().navAreas.open).toEqual([
      'system',
      'automation',
    ])
    // Folding the ACTIVE area is a conscious act: it stays folded while the route stays.
    s.setAreaOpen('ai', false)
    expect(
      isAreaOpen(usePreferencesStore.getState().navAreas, 'ai', 'ai'),
    ).toBe(false)
    // Arriving again (a route change into the area) reveals it once more.
    s.revealArea('ai')
    expect(
      isAreaOpen(usePreferencesStore.getState().navAreas, 'ai', 'ai'),
    ).toBe(true)
    // The other two are still open: nothing was reset by the reveal.
    expect(usePreferencesStore.getState().navAreas.open).toEqual([
      'system',
      'automation',
    ])
  })

  it('touches neither density nor the retired hub list', () => {
    reset()
    usePreferencesStore.setState({
      density: 'compact',
      collapsedNavGroups: ['operate'],
    })
    usePreferencesStore.getState().setAreaOpen('ai', true)
    usePreferencesStore.getState().setAreaOpen('system', false)
    usePreferencesStore.getState().revealArea('ai')
    const s = usePreferencesStore.getState()
    expect(s.density).toBe('compact')
    expect(s.collapsedNavGroups).toEqual(['operate'])
    usePreferencesStore.setState({
      density: 'comfortable',
      collapsedNavGroups: [],
    })
  })

  it('accepts only its own versioned shape when rehydrating, and falls back to the default otherwise', () => {
    expect(isAreaExpansion({ v: 1, open: ['ai'], closed: [] })).toBe(true)
    expect(isAreaExpansion({ v: 2, open: [], closed: [] })).toBe(false)
    expect(isAreaExpansion({ v: 1, open: 'ai', closed: [] })).toBe(false)
    expect(isAreaExpansion({ v: 1, open: [1], closed: [] })).toBe(false)
    expect(isAreaExpansion(null)).toBe(false)
    expect(isAreaExpansion('ai')).toBe(false)
    // The persist merge: the rest of the blob survives, a bad navAreas does not,
    // and the retired pre-Estate sidebar fields are dropped, not rehydrated.
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
        navAreas: { v: 99, open: 'x' },
      },
      current,
    )
    expect(merged.density).toBe('compact')
    expect(merged.reduceMotion).toBe(true)
    expect(merged.collapsedNavGroups).toEqual(['operate'])
    expect(merged.navAreas).toEqual(DEFAULT_AREA_EXPANSION)
    expect('sidebarCollapsed' in merged).toBe(false)
    expect('sidebarChoices' in merged).toBe(false)
    expect('sidebarUnsent' in merged).toBe(false)
    const kept = merge(
      { navAreas: { v: 1, open: ['system'], closed: ['ai'] } },
      current,
    )
    expect(kept.navAreas).toEqual({ v: 1, open: ['system'], closed: ['ai'] })
  })
})
