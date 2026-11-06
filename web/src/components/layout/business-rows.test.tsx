// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE CLOSED BUSINESS ROWS IN THE SHELL: after the page's own content, on the page the list
// names, one link per row that says Business. Real routes, as the shell renders them.
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from '@tanstack/react-router'
import { cleanup, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { BUSINESS_BUILD } from '@/features/business-build'
import { FEATURE_VIEWS } from '@/features/registry'
import { renderIntel } from '@/test/intel'
import { BusinessRows } from './business-rows'

afterEach(cleanup)

const pathOf = (id: string) => FEATURE_VIEWS.find((v) => v.id === id)!.path

async function at(address: string) {
  const root = createRootRoute({
    component: () => (
      <>
        <Outlet />
        <BusinessRows />
      </>
    ),
  })
  const router = createRouter({
    routeTree: root.addChildren(
      [...new Set(FEATURE_VIEWS.map((v) => v.path))].map((path) =>
        createRoute({
          getParentRoute: () => root,
          path,
          component: () => <p>page content</p>,
        }),
      ),
    ),
    history: createMemoryHistory({ initialEntries: [address] }),
  })
  renderIntel(<RouterProvider router={router} />)
  await router.load()
}

describe.skipIf(BUSINESS_BUILD)('closed Business rows (Community)', () => {
  it('follow the page content of their page, each a link named with Business', async () => {
    await at(pathOf('security'))
    const page = await screen.findByText('page content')
    const rows = [
      screen.getByRole('link', {
        name: 'Compliance frameworks and evidence Business',
      }),
      screen.getByRole('link', { name: 'Red team runs and scoring Business' }),
    ]
    expect(screen.getAllByRole('link', { name: /Business/ })).toEqual(rows)
    for (const row of rows) {
      expect(row.getAttribute('href')).toBe('https://olivares.ai/business')
      expect(
        page.compareDocumentPosition(row) & Node.DOCUMENT_POSITION_FOLLOWING,
      ).toBeTruthy()
      expect(row.className).not.toMatch(/accent/)
    }
  })

  it('say on Retention that only Business applies its schedules', async () => {
    await at(pathOf('retention'))
    await screen.findByText('page content')
    expect(
      screen.getByRole('link', { name: /applied only in Olivares Business/ }),
    ).toHaveAttribute('href', 'https://olivares.ai/business')
  })

  it('draw nothing on a page the list does not name', async () => {
    await at(pathOf('audit'))
    await screen.findByText('page content')
    expect(screen.queryByRole('link', { name: /Business/ })).toBeNull()
  })
})
