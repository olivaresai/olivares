// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE RAIL FROM A KEYBOARD, IN A RENDERED SIDEBAR.
//
// `rail-keys.test.ts` drives the RULES without a DOM. This file proves the rules are
// actually wired to one: the tab stops, the roving index, the arrows, and the pin — which
// is the half a pure test cannot see and the half that broke twice while it was written.
import { fireEvent, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { ComponentProps } from 'react'
import { renderIntel } from '@/test/intel'

const routerState = vi.hoisted(() => ({ pathname: '/' }))
vi.mock('@tanstack/react-router', () => ({
  useRouterState: ({
    select,
  }: {
    select?: (s: { location: { pathname: string } }) => unknown
  } = {}) =>
    select ? select({ location: { pathname: routerState.pathname } }) : '',
  Link: ({
    children,
    to,
    activeProps,
    activeOptions,
    ...props
  }: ComponentProps<'a'> & {
    to?: string
    activeProps?: Record<string, string>
    activeOptions?: { exact?: boolean }
  }) => {
    const active = activeOptions?.exact
      ? routerState.pathname === to
      : routerState.pathname === to || routerState.pathname.startsWith(`${to}/`)
    return (
      <a
        href={to}
        data-status={active ? 'active' : undefined}
        {...(active ? activeProps : {})}
        {...props}
      >
        {children}
      </a>
    )
  },
}))

// `grants: []` / `isSuperadmin: false`: the rail's scope block renders
// nothing without a membership to choose, which keeps these keyboard tests about the
// navigation rows they are named for.
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    can: () => true,
    grants: [],
    isSuperadmin: false,
    activeTenant: null,
    setActiveTenant: () => {},
  }),
}))

/** A personal partition that exists, so the pin is offered and its writes are observable. */
const favorites = vi.hoisted(() => ({ ids: [] as string[] }))
const setFavorite = vi.hoisted(() => vi.fn())
vi.mock('@/features/navigation/personal-navigation', () => ({
  usePersonalNavigation: () => ({
    available: true,
    temporary: false,
    visible: () => true,
    favorites: favorites.ids.map((id) => ({ kind: 'feature', id })),
    recents: [],
    setFavorite,
    removeRecent: () => {},
    clearRecents: () => {},
    clearFavorites: () => {},
    recordVisit: () => {},
  }),
}))

import { Sidebar } from './sidebar'
import {
  DEFAULT_AREA_EXPANSION,
  usePreferencesStore,
} from '@/stores/preferences'

afterEach(() => {
  routerState.pathname = '/'
  favorites.ids = []
  setFavorite.mockReset()
  usePreferencesStore.setState({
    collapsedNavGroups: [],
    sidebarCollapsed: false,
    navAreas: DEFAULT_AREA_EXPANSION,
  })
})

const rows = () =>
  Array.from(document.querySelectorAll<HTMLElement>('aside [data-nav-row]'))
const tabbable = () => rows().filter((r) => r.tabIndex === 0)

describe('the navigation rail from a keyboard', () => {
  it('is ONE tab stop, not eighty-five', async () => {
    renderIntel(<Sidebar />)
    // Measured 2026-09-18 on the live console before this change: 85 focusable elements
    // in the sidebar. The rail is a composite now, so exactly one of its rows is tabbable
    // and the arrows do the rest.
    expect(rows().length).toBeGreaterThan(8)
    expect(tabbable()).toHaveLength(1)
  })

  it('puts the tab stop on the row that names the current page', () => {
    routerState.pathname = '/'
    renderIntel(<Sidebar />)
    expect(tabbable()[0].getAttribute('aria-current')).toBe('page')
  })

  it('moves the focus and the tab stop together with ArrowDown', async () => {
    renderIntel(<Sidebar />)
    const start = tabbable()[0]
    start.focus()
    fireEvent.keyDown(start, { key: 'ArrowDown' })
    const after = tabbable()
    expect(after).toHaveLength(1)
    expect(after[0]).not.toBe(start)
    expect(document.activeElement).toBe(after[0])
  })

  it('reaches the last row with End and the first with Home', () => {
    renderIntel(<Sidebar />)
    const start = tabbable()[0]
    start.focus()
    fireEvent.keyDown(start, { key: 'End' })
    expect(document.activeElement).toBe(rows()[rows().length - 1])
    fireEvent.keyDown(document.activeElement!, { key: 'Home' })
    expect(document.activeElement).toBe(rows()[0])
  })

  it('opens a folded area with ArrowRight and folds it again with ArrowLeft', () => {
    renderIntel(<Sidebar />)
    const area = document.querySelector<HTMLElement>(
      'aside [data-nav-area-row][data-area-open="false"]',
    )!
    const id = area.getAttribute('data-nav-area-row')!
    area.focus()
    fireEvent.keyDown(area, { key: 'ArrowRight' })
    expect(
      document.querySelector(`aside [data-nav-area-row="${id}"]`),
    ).toHaveAttribute('data-area-open', 'true')
    const open = document.querySelector<HTMLElement>(
      `aside [data-nav-area-row="${id}"]`,
    )!
    open.focus()
    fireEvent.keyDown(open, { key: 'ArrowLeft' })
    expect(
      document.querySelector(`aside [data-nav-area-row="${id}"]`),
    ).toHaveAttribute('data-area-open', 'false')
  })

  it('pins the focused row with `p`, through the row control the menu path uses', async () => {
    renderIntel(<Sidebar />)
    const row = rows().find((r) => r.getAttribute('data-pin-id') === 'home')!
    row.focus()
    fireEvent.keyDown(row, { key: 'p' })
    // ONE implementation: the key clicks the row's own button, so the key and the named
    // control cannot become two behaviours.
    expect(setFavorite).toHaveBeenCalledTimes(1)
    expect(setFavorite.mock.calls[0][0]).toMatchObject({ id: 'home' })
    expect(setFavorite.mock.calls[0][1]).toBe(true)
  })

  it('offers the pin as a NAMED control that says which key does it', async () => {
    renderIntel(<Sidebar />)
    const pin = document.querySelector<HTMLButtonElement>(
      'aside [data-rail-pin="home"]',
    )!
    // Named before it happens, and the name carries the key, which is what
    // makes the two equal paths rather than a key nobody can discover.
    expect(pin.getAttribute('aria-label')).toMatch(/\(p\)/)
    expect(pin.getAttribute('aria-pressed')).toBe('false')
    // Not a tab stop: the rail owns the one.
    expect(pin.tabIndex).toBe(-1)
    await userEvent.click(pin)
    expect(setFavorite).toHaveBeenCalledTimes(1)
  })

  it('leaves the filter field its own keys', () => {
    renderIntel(<Sidebar />)
    const field = screen.getAllByRole('searchbox')[0]
    const before = tabbable()[0]
    fireEvent.keyDown(field, { key: 'ArrowDown' })
    // A keystroke inside the field is the field's. Stealing ArrowDown there would stop
    // an operator moving the caret while they filter.
    expect(tabbable()[0]).toBe(before)
  })

  it('cuts no label: no navigation row truncates its text', () => {
    renderIntel(<Sidebar />)
    // Measured 2026-09-18: the rail cut 65 labels across the seven console languages.
    // jsdom has no layout, so the property held here is the CAUSE — no row applies
    // `truncate` — and the live spec measures the effect in a real browser.
    for (const row of rows())
      expect(row.querySelector('.truncate'), row.textContent ?? '').toBeNull()
  })
})
