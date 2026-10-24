// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ONE CURRENT LINK PER SURFACE, WITH THE REAL ROUTER. TanStack's Link marks itself
// `aria-current="page"` wherever its path matches the address, after the props it is given,
// so a top bar place, a phone-bar link or a member of the row above a page must hold the
// router to the exact address. Without that, /communications/inbox marks both Communications
// and Inbox in its row. A mocked Link cannot show this; these routes are real.
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from '@tanstack/react-router'
import { cleanup, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { renderIntel } from '@/test/intel'

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

import { FEATURE_VIEWS } from '@/features/registry'
import { SETTINGS_UTILITY } from '@/features/navigation/model'
import { DestinationSections } from './destination-sections'
import { PlacesNav } from './places-nav'
import { PhoneBar } from './phone-bar'

afterEach(cleanup)

async function currentAt(address: string) {
  const root = createRootRoute({
    component: () => (
      <>
        <PlacesNav />
        <PhoneBar />
        <DestinationSections />
        <Outlet />
      </>
    ),
  })
  const paths = new Set([
    ...FEATURE_VIEWS.map((v) => v.path),
    SETTINGS_UTILITY.path,
  ])
  const router = createRouter({
    routeTree: root.addChildren(
      [...paths].map((path) =>
        createRoute({
          getParentRoute: () => root,
          path,
          component: () => null,
        }),
      ),
    ),
    history: createMemoryHistory({ initialEntries: [address] }),
  })
  renderIntel(<RouterProvider router={router} />)
  await screen.findByRole('navigation', { name: 'Journeys' })
  const marked = (selector: string) =>
    [...document.querySelectorAll(`${selector} [aria-current="page"]`)].map(
      (a) => a.getAttribute('href'),
    )
  return {
    places: marked('nav[aria-label="Journeys"]'),
    phone: marked('nav[aria-label="Main"]'),
    row: marked('[data-slot="destination-sections"]'),
  }
}

describe('the router marks no second current link', () => {
  it.each([
    ['/communications/inbox', [], ['/communications/inbox']],
    ['/console/sources/diff', [], ['/console/sources/diff']],
    // A page folded into a place (#2240) marks that place, in the top bar and in its row.
    ['/provider-profiles', ['/agent-tools'], ['/agent-tools']],
    ['/provider-bindings', ['/agent-tools'], ['/agent-tools']],
    ['/provider-accounts', ['/agent-tools'], ['/agent-tools']],
    // A page whose section holds no place marks none; its row still moves between its section.
    ['/mcp-servers', ['/agent-tools'], ['/mcp-servers']],
    ['/inventory', [], ['/inventory']],
    // A deep-link detail reads as its parent's page, under the Ledger with the evidence.
    // (Recordings is a Business page: Community has no parent row to mark.)
    [
      '/session-viewer/s-1',
      FEATURE_VIEWS.some((v) => v.id === 'recordings') ? ['/audit'] : [],
      FEATURE_VIEWS.some((v) => v.id === 'recordings') ? ['/recordings'] : [],
    ],
    ['/audit', ['/audit'], ['/audit']],
    ['/evals', ['/audit'], ['/evals']],
    ['/permissions?tab=approvals', ['/permissions?tab=approvals'], []],
    ['/sessions?session=s-1', ['/sessions'], []],
    ['/', ['/'], []],
  ])('at %s', async (address, places, row) => {
    const current = await currentAt(address)
    expect(current.places).toEqual(places)
    expect(current.row).toEqual(row)
    // The phone bar is the first three places, current by the same rule.
    expect(current.phone).toEqual(
      places.filter((href) =>
        ['/', '/sessions', '/agent-tools'].includes(href),
      ),
    )
  })
})
