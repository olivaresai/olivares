// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE ROW OF A PLACE: at most five of its pages and one More menu for the rest, and on the
// work surface (where no row is drawn) the same pages behind the list's heading. Real routes,
// so the router's own current link is what the test reads.
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from '@tanstack/react-router'
import {
  act,
  cleanup,
  fireEvent,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import type { ReactNode } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { TooltipProvider } from '@/components/ui/tooltip'
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

import { SETTINGS_UTILITY } from '@/features/navigation/model'
import { FEATURE_VIEWS } from '@/features/registry'
import { SessionsListHeader } from '@/features/sessions/list-header'
import { DestinationSections } from './destination-sections'

afterEach(cleanup)

const pathOf = (id: string) => FEATURE_VIEWS.find((v) => v.id === id)!.path

async function at(address: string, page: ReactNode = null) {
  const root = createRootRoute({
    component: () => (
      <TooltipProvider>
        <DestinationSections />
        {page}
        <Outlet />
      </TooltipProvider>
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
  await router.load()
}

const hrefs = (links: HTMLElement[]) => links.map((a) => a.getAttribute('href'))

async function openWithKeyboard(button: HTMLElement) {
  button.focus()
  act(() => {
    fireEvent.keyDown(button, { key: 'Enter' })
  })
  return screen.findByRole('menu')
}

describe('the row above a page', () => {
  it('draws at most five pages of the place and one More menu with the rest, in place order', async () => {
    await at('/audit')
    const row = await screen.findByRole('navigation', { name: 'Audit' })
    expect(hrefs(within(row).getAllByRole('link'))).toEqual(
      ['audit', 'accessMap', 'logs', 'evals'].map(pathOf),
    )
    // Links, each its own page, drawn as 32 px pills two pixels apart: the current one is
    // bold as well as filled, and the focus ring sits inside the pill.
    expect(row).toHaveClass('gap-0.5')
    const current = within(row).getByRole('link', { current: 'page' })
    expect(current).toHaveClass(
      'h-8',
      'aria-[current=page]:bg-active',
      'aria-[current=page]:font-bold',
      'focus-visible:-outline-offset-2',
    )
    expect(current.className).toContain('active:bg-[var(--pressed)]')
    const more = within(row).getByRole('button', { name: 'More' })
    expect(more).toHaveAttribute('aria-haspopup', 'menu')
    const menu = await openWithKeyboard(more)
    expect(hrefs(within(menu).getAllByRole('menuitem'))).toEqual(
      ['observability', 'dashboards', 'retention', 'reach'].map(pathOf),
    )
  })

  it('marks the current page inside More; arrows move, Escape closes and returns focus', async () => {
    await at(pathOf('retention'))
    const row = await screen.findByRole('navigation', { name: 'Audit' })
    expect(row.querySelector('[aria-current="page"]')).toBeNull()
    const more = within(row).getByRole('button', { name: 'More' })
    const menu = await openWithKeyboard(more)
    const items = within(menu).getAllByRole('menuitem')
    expect(
      items.filter((i) => i.getAttribute('aria-current') === 'page'),
    ).toEqual([items[2]])
    act(() => {
      fireEvent.keyDown(document.activeElement!, { key: 'ArrowDown' })
    })
    expect(items).toContain(document.activeElement)
    act(() => {
      fireEvent.keyDown(document.activeElement!, { key: 'Escape' })
    })
    expect(screen.queryByRole('menu')).toBeNull()
    // The menu hands focus back one tick after it unmounts.
    await waitFor(() => expect(document.activeElement).toBe(more))
  })
})

describe('the Sessions place in the shell header', () => {
  it('keeps Sessions, Work and Channels in the row, with less-used pages under More', async () => {
    await at(
      '/sessions',
      <SessionsListHeader
        count={0}
        countLabel=""
        views={[]}
        onView={() => {}}
        streamFailed={false}
      />,
    )
    const row = document.querySelector(
      '[data-slot="destination-sections"]',
    ) as HTMLElement
    expect(within(row).getByRole('link', { name: 'Sessions' })).toHaveAttribute(
      'href',
      '/sessions',
    )
    expect(within(row).getByRole('link', { name: 'Work' })).toHaveAttribute(
      'href',
      '/work',
    )
    expect(within(row).getByRole('link', { name: 'Channels' })).toHaveAttribute(
      'href',
      '/communications',
    )
    const menu = await openWithKeyboard(
      within(row).getByRole('button', { name: 'More' }),
    )
    expect(
      within(menu).getByRole('menuitem', { name: 'Voice' }),
    ).toHaveAttribute('href', '/voice')
    expect(
      within(menu).getByRole('menuitem', { name: 'Sandbox' }),
    ).toHaveAttribute('href', '/sandbox')
  })
})
