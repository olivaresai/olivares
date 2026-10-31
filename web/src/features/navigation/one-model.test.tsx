// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ONE NAVIGATION MODEL, AS THE PERSON SEES IT. Each view sits in one place of the left
// navigation (features/navigation/places.ts), and every surface that shows where a page is
// says the same thing: the place the sidebar marks and the row of pages above it. The first
// job (the four journeys) is reachable from the first screen: a place, or a page in that
// place's row, or Settings and its row.
import { cleanup } from '@testing-library/react'
import type { ComponentProps } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { renderIntel } from '@/test/intel'

const routerState = vi.hoisted(() => ({
  pathname: '/',
  search: {} as Record<string, unknown>,
}))
vi.mock('@tanstack/react-router', () => ({
  useRouterState: ({
    select,
  }: {
    select?: (s: {
      location: { pathname: string; search: Record<string, unknown> }
    }) => unknown
  } = {}) =>
    select
      ? select({
          location: {
            pathname: routerState.pathname,
            search: routerState.search,
          },
        })
      : '',
  useNavigate: () => () => undefined,
  Link: ({
    children,
    to,
    search,
    activeProps,
    activeOptions,
    ...props
  }: ComponentProps<'a'> & {
    to?: string
    search?: Record<string, string>
    activeProps?: Record<string, string>
    activeOptions?: { exact?: boolean }
  }) => {
    void activeOptions
    const active =
      routerState.pathname === to &&
      Object.entries(search ?? {}).every(
        ([k, v]) => routerState.search[k] === v,
      )
    const href = search ? `${to}?${new URLSearchParams(search)}` : to
    return (
      <a href={href} {...props} {...(active ? activeProps : {})}>
        {children}
      </a>
    )
  },
}))

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    can: () => true,
    grants: [],
    isSuperadmin: true,
    principal: { superadmin: true, grants: [] },
    activeTenant: null,
    setActiveTenant: () => {},
  }),
}))

vi.mock('@/components/layout/scope-switcher', () => ({
  ScopeSwitcher: () => null,
}))
vi.mock('@/components/layout/use-session-rail', () => ({
  useSessionRail: () => ({
    sessionCounts: { needsYou: 0, live: 0, running: 0, open: 0 },
  }),
}))
vi.mock('@/features/governance/use-pending-approvals', async (actual) => ({
  ...(await actual<object>()),
  usePendingApprovals: () => ({ query: { data: undefined } }),
}))

import { FEATURE_VIEWS, type FeatureView } from '@/features/registry'
import { SETTINGS_UTILITY, isListed, permissionGate } from './model'
import { JOURNEYS, stepHref } from './journeys'
import { placeOfView } from './places'
import { DestinationSections } from '@/components/layout/destination-sections'
import { Sidebar } from '@/components/layout/sidebar'

afterEach(() => {
  cleanup()
  routerState.pathname = '/'
  routerState.search = {}
})

const allowAll = permissionGate(() => true)

/** What the top bar and the strip show at an address, for a principal who may open all. */
function shownAt(href: string) {
  const url = new URL(href, 'http://console.invalid')
  routerState.pathname = url.pathname
  routerState.search = Object.fromEntries(url.searchParams)
  const { container } = renderIntel(
    <>
      <Sidebar />
      <DestinationSections />
    </>,
  )
  const strip = [
    ...container.querySelectorAll<HTMLAnchorElement>(
      '[data-slot="destination-sections"] a',
    ),
  ].map((a) => a.getAttribute('href') ?? '')
  const rows = [
    ...container.querySelectorAll<HTMLAnchorElement>('a[data-place]'),
  ]
  const marked = rows
    .filter((a) => a.getAttribute('aria-current') === 'page')
    .map((a) => a.getAttribute('href') ?? '')
  const result = {
    rows: rows.map((a) => a.getAttribute('href') ?? ''),
    strip,
    marked,
  }
  cleanup()
  return result
}

const placeOf = placeOfView
const viewAt = (path: string) => FEATURE_VIEWS.find((v) => v.path === path)

/** Every address the first screen offers: its places, Settings, and the strip each of
 *  those opens on. */
function offeredFromFirstScreen(): Set<string> {
  const { rows } = shownAt('/')
  const offered = new Set(rows)
  for (const start of [...rows, SETTINGS_UTILITY.path]) {
    offered.add(start)
    for (const href of shownAt(start).strip) offered.add(href)
  }
  // A door folds into its room (`doorTo`): the room opens it (#2240: AI tools' tool menu
  // opens the provider profile, binding and account pages).
  for (const v of FEATURE_VIEWS) {
    const room = v.doorTo && FEATURE_VIEWS.find((r) => r.id === v.doorTo)
    if (room && offered.has(room.path)) offered.add(v.path)
  }
  return offered
}

describe('the first screen', () => {
  it('offers every step of the four journeys: a place, its strip, or Settings', () => {
    const offered = offeredFromFirstScreen()
    const missing = Object.entries(JOURNEYS).flatMap(([journey, steps]) =>
      steps
        .map((step) => stepHref(step))
        .filter((href) => !offered.has(href))
        .map((href) => `${journey}: ${href}`),
    )
    expect(missing).toEqual([])
  })

  it('names the places and only the places, on a new installation and any other', () => {
    expect(shownAt('/').rows).toEqual([
      '/',
      '/sessions',
      '/workspace',
      '/work',
      '/agent-tools',
      '/models',
      '/capabilities',
      '/knowledge',
      '/permissions?tab=approvals',
      // Usage & cost leads with the accounts and today, before Business's FinOps.
      '/usage',
      '/audit',
      '/access-map',
      '/identity',
      '/security',
      '/estate',
      '/settings?section=edition',
      '/settings',
    ])
    // A tool, a key or a local model, a session, its approval, stop and resume, history.
    const offered = offeredFromFirstScreen()
    for (const step of [
      ...JOURNEYS.firstHour,
      ...JOURNEYS.sessions,
      ...JOURNEYS.approvals,
    ])
      expect(offered).toContain(stepHref(step))
    // The rest of the product is in its places' rows, not in a second list of places.
    expect(shownAt('/').rows).not.toContain('/model-operations')
  })
})

describe('every page sits in one place', () => {
  const listed = FEATURE_VIEWS.filter(
    (v) => v.navigation.kind === 'feature' && isListed(v, allowAll),
  )

  it('lists only pages of its own place in the strip above it', () => {
    const strays = listed.flatMap((v) =>
      shownAt(v.path)
        .strip.filter((href) => href !== SETTINGS_UTILITY.path)
        .map((href) => viewAt(href))
        .filter(
          (member): member is FeatureView =>
            !!member && placeOf(member) !== placeOf(v),
        )
        .map(
          (member) =>
            `${v.path} (${placeOf(v)}) lists ${member.path} (${placeOf(member)})`,
        ),
    )
    expect(strays).toEqual([])
  })

  it('marks exactly one place, the one that holds the page', () => {
    const wrong = listed.flatMap((v) => {
      const { marked } = shownAt(v.path)
      const at = marked.map((href) => href.split('?')[0])
      const place =
        at.length === 1 && at[0] !== SETTINGS_UTILITY.path
          ? viewAt(at[0])
          : null
      if (place && placeOf(place) === placeOf(v)) return []
      if (at[0] === SETTINGS_UTILITY.path && placeOf(v) === 'settings')
        return []
      return [
        `${v.path} (${placeOf(v)}) marks ${marked.join(', ') || 'nothing'}`,
      ]
    })
    expect(listed.length).toBeGreaterThan(50)
    expect(wrong).toEqual([])
  })
})
