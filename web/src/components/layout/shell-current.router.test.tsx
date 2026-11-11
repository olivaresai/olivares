// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ONE CURRENT LINK PER SURFACE, WITH THE REAL ROUTER. TanStack's Link marks itself
// `aria-current="page"` wherever its path matches the address, after the props it is given,
// so a place of the left navigation or a member of the row above a page must hold the router
// to the exact address. Without that, Now (`/`) is current on every page and Settings under
// License. A mocked Link cannot show this; these routes are real.
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
vi.mock('./scope-switcher', () => ({ ScopeSwitcher: () => null }))

import { FEATURE_VIEWS } from '@/features/registry'
import { SETTINGS_UTILITY } from '@/features/navigation/model'
import { DestinationSections } from './destination-sections'
import { Sidebar } from './sidebar'

afterEach(cleanup)

async function currentAt(address: string) {
  const root = createRootRoute({
    component: () => (
      <>
        <Sidebar />
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
  await screen.findByRole('navigation', { name: 'Main navigation' })
  const marked = (selector: string) =>
    [...document.querySelectorAll(`${selector} [aria-current="page"]`)].map(
      (a) =>
        a.getAttribute('data-place') ?? a.getAttribute('href') ?? a.textContent,
    )
  return {
    places: marked('[data-slot="sidebar"]'),
    row: marked('[data-slot="destination-sections"]'),
  }
}

const recordings = FEATURE_VIEWS.some((v) => v.id === 'recordings')

describe('the router marks no second current link', () => {
  it.each([
    ['/now', ['now'], []],
    // The inbox is a door into the one Communications room: the room's row is the current one.
    ['/communications/inbox', ['sessions'], ['/communications']],
    // Source diff sits under the row's More: the row marks no link, the menu marks it.
    ['/console/sources/diff', ['sessions'], []],
    // A door folded into a room marks the room's place and, in its row, the room.
    ['/provider-profiles', ['tools'], ['/agent-tools']],
    ['/provider-accounts', ['tools'], ['/agent-tools']],
    // /mcp-servers now lands on MCP & skills' Servers tab.
    ['/capabilities', ['tools'], ['/capabilities']],
    ['/inventory', ['files'], ['/inventory']],
    // A deep-link detail reads as its parent's page (Recordings is a Business page).
    [
      '/session-viewer/s-1',
      recordings ? ['sessions'] : [],
      recordings ? ['/recordings'] : [],
    ],
    ['/audit', ['activity'], ['/audit']],
    ['/evals', ['activity'], ['/evals']],
    ['/permissions?tab=approvals', ['rules'], ['/permissions?tab=approvals']],
    // The bare page opens on the queue; another section of it is Permissions.
    ['/permissions', ['rules'], ['/permissions?tab=approvals']],
    ['/permissions?tab=identities', ['rules'], ['/permissions?tab=policies']],
    // Sessions has pages of its own: the top bar's row moves between them as on any place.
    ['/sessions?session=s-1', ['sessions'], ['/sessions']],
    // Settings replaces the places with its sections: the open section is the one current
    // link of the column, and no row is drawn.
    ['/settings', ['General'], []],
    ['/settings?section=edition', ['Edition'], []],
  ])('at %s', async (address, places, row) => {
    const current = await currentAt(address)
    expect(current.places).toEqual(places)
    expect(current.row).toEqual(row)
  })
})
