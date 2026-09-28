// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Every destination of the sidebar's journeys and of the phone bar comes from the
// registry: its id, its path, its icon and its permission. A destination written by hand
// in the shell would be a door that no census, no permission check and no route test sees.
//
// And a permitted leaf opens by direct link with no parent permission: an area is the
// union of the leaves the principal may open, never a gate of its own.
import { screen, within } from '@testing-library/react'
import type { ComponentProps } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { renderIntel } from '@/test/intel'

const routerState = vi.hoisted(() => ({ pathname: '/' }))
vi.mock('@tanstack/react-router', () => ({
  useRouterState: ({
    select,
  }: {
    select?: (s: { location: { pathname: string } }) => unknown
  } = {}) =>
    select ? select({ location: { pathname: routerState.pathname } }) : '',
  useNavigate: () => () => undefined,
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
    void activeOptions
    const active = routerState.pathname === to
    return (
      <a href={to} {...(active ? activeProps : {})} {...props}>
        {children}
      </a>
    )
  },
}))

const canMock = vi.fn((_permission: string) => true)
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    can: (p: string) => canMock(p),
    grants: [],
    isSuperadmin: false,
    activeTenant: null,
    setActiveTenant: () => {},
  }),
}))

import { FEATURE_VIEWS, NAV_AREAS } from './registry'
import { authorizedSections, permissionGate } from './navigation/model'
import {
  JOURNEY_IDS,
  PHONE_BAR_IDS,
  authorizedAreas,
  journeyDestinations,
  phoneBarDestinations,
} from '@/components/layout/shell-destinations'
import { JourneyNav } from '@/components/layout/journey-nav'
import { PhoneBar } from '@/components/layout/phone-bar'
import { AreasTree } from '@/components/layout/sidebar'

afterEach(() => {
  routerState.pathname = '/'
  canMock.mockReset()
  canMock.mockReturnValue(true)
})

const viewOf = (id: string) => FEATURE_VIEWS.find((v) => v.id === id)
const allowAll = permissionGate(() => true)

describe('the shell destinations come from the registry', () => {
  it('names only registry views that navigation may list', () => {
    for (const id of [...JOURNEY_IDS, ...PHONE_BAR_IDS]) {
      const view = viewOf(id)
      expect(view, id).toBeDefined()
      expect(view?.hideInNav, id).not.toBe(true)
      expect(view?.navigation.kind, id).not.toBe('detail')
    }
  })

  it('takes each path and icon from the registry entry, in the pinned order', () => {
    const journeys = journeyDestinations(allowAll)
    expect(journeys.map((d) => d.id)).toEqual([...JOURNEY_IDS])
    for (const d of [...journeys, ...phoneBarDestinations(allowAll)]) {
      const view = viewOf(d.id)
      expect(d.path).toBe(view?.path)
      expect(d.icon).toBe(view?.icon)
    }
  })

  it('renders the sidebar journeys as links to registry paths and nothing else', () => {
    renderIntel(<JourneyNav />)
    const nav = screen.getByRole('navigation', { name: 'Journeys' })
    const hrefs = within(nav)
      .getAllByRole('link')
      .map((a) => a.getAttribute('href'))
    expect(hrefs).toEqual(JOURNEY_IDS.map((id) => viewOf(id)?.path))
  })

  it('renders the phone bar links from registry paths, beside New and More', () => {
    renderIntel(<PhoneBar />)
    const bar = screen.getByRole('navigation', { name: 'Main' })
    const hrefs = within(bar)
      .getAllByRole('link')
      .map((a) => a.getAttribute('href'))
    expect(hrefs).toEqual(PHONE_BAR_IDS.map((id) => viewOf(id)?.path))
    expect(
      within(bar).getByRole('button', { name: 'New session' }),
    ).toBeInTheDocument()
    expect(
      within(bar).getByRole('button', { name: 'More: all areas' }),
    ).toBeInTheDocument()
  })

  it('hides a journey the principal may not open, and never shows a hand-made one', () => {
    canMock.mockImplementation((p) => p !== viewOf('deploy')?.permission)
    renderIntel(<JourneyNav />)
    const nav = screen.getByRole('navigation', { name: 'Journeys' })
    const hrefs = within(nav)
      .getAllByRole('link')
      .map((a) => a.getAttribute('href'))
    expect(hrefs).not.toContain('/deploy')
    expect(hrefs).toContain('/')
  })
})

describe('a permitted leaf opens by direct link with no parent permission', () => {
  const work = viewOf('work')!

  it('offers the area of the one permitted leaf, and the leaf itself', () => {
    // Only the leaf's own permission: nothing about its area, its section or a hub.
    canMock.mockImplementation((p) => p === work.permission)
    const onlyWork = permissionGate((p) => p === work.permission)
    // The areas open to anyone (a leaf that needs no permission), and the leaf's own.
    const open = new Set(
      NAV_AREAS.filter(
        (a) =>
          authorizedSections(
            a.id,
            permissionGate(() => false),
          ).length > 0,
      ).map((a) => a.id),
    )
    expect(open.has('work-communications')).toBe(false)
    expect(authorizedAreas(onlyWork).map((a) => a.id)).toEqual(
      NAV_AREAS.filter(
        (a) => a.id === 'work-communications' || open.has(a.id),
      ).map((a) => a.id),
    )
    // Inside its area, the leaf is the only door: nothing about the area opened it.
    expect(
      authorizedSections('work-communications', onlyWork).flatMap((s) =>
        s.views.map((v) => v.id),
      ),
    ).toEqual(['work'])

    routerState.pathname = work.path
    renderIntel(<AreasTree />)
    const links = screen.getAllByRole('link').map((a) => a.getAttribute('href'))
    expect(links).toContain(work.path)
    expect(links).toContain('/areas/work-communications')
    // No other area is offered without a leaf of its own.
    for (const a of NAV_AREAS.filter(
      (x) => x.id !== 'work-communications' && !open.has(x.id),
    ))
      expect(links).not.toContain(a.path)
    // The direct link is the current page.
    expect(
      screen
        .getAllByRole('link')
        .find((a) => a.getAttribute('href') === work.path),
    ).toHaveAttribute('aria-current', 'page')
  })

  it('keeps the journeys that need no permission when the leaf is the only grant', () => {
    canMock.mockImplementation((p) => p === work.permission)
    renderIntel(<JourneyNav />)
    const nav = screen.getByRole('navigation', { name: 'Journeys' })
    const hrefs = within(nav)
      .getAllByRole('link')
      .map((a) => a.getAttribute('href'))
    expect(hrefs).toEqual(
      JOURNEY_IDS.map((id) => viewOf(id)!)
        .filter((v) => !v.permission)
        .map((v) => v.path),
    )
  })
})
