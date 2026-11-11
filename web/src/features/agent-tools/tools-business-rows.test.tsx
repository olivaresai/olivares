// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// WHAT OLIVARES AI BUSINESS ADDS TO TOOLS: assigning providers to people and departments,
// and limits set for other people. Two closed rows the shell draws after AI tools, the
// lead page of Tools, for an administrator of an installation whose edition does not
// hold them; nobody else sees them, and no page of Tools says it again.
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from '@tanstack/react-router'
import { cleanup, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { BusinessRows } from '@/components/layout/business-rows'
import { FEATURE_VIEWS } from '@/features/registry'
import { DEFAULT_AUTH, renderIntel } from '@/test/intel'

const auth = vi.hoisted(() => ({ superadmin: true }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ ...DEFAULT_AUTH, isSuperadmin: auth.superadmin }),
}))

const server = vi.hoisted(() => ({
  answer: { isSuccess: false, data: undefined } as {
    isSuccess: boolean
    data?: Record<string, unknown>
  },
}))
vi.mock('@/lib/hooks/use-server-info', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/lib/hooks/use-server-info')>()),
  useServerInfo: () => server.answer,
}))
const answers = (data: Record<string, unknown>) => {
  server.answer = { isSuccess: true, data }
}

beforeEach(() => {
  auth.superadmin = true
  answers({ edition: 'community', license: { status: 'none' } })
})
afterEach(cleanup)

const ASSIGNING =
  'Providers assigned to people and departments Olivares AI Business'
const LIMITS =
  'Usage limits set for other people and departments Olivares AI Business'

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
  await screen.findByText('page content')
}

describe('what Olivares AI Business adds to Tools', () => {
  it('is two closed rows after AI tools, for an administrator of a Community installation', async () => {
    await at('/agent-tools')
    for (const name of [ASSIGNING, LIMITS])
      expect(screen.getByRole('link', { name })).toHaveAttribute(
        'href',
        'https://olivares.ai/business',
      )
  })

  it('is drawn on the lead page of Tools only, not again on Providers', async () => {
    await at('/providers')
    expect(screen.queryByRole('link', { name: ASSIGNING })).toBeNull()
  })

  it('is not shown to anyone who is not an administrator, whatever they manage', async () => {
    auth.superadmin = false
    await at('/agent-tools')
    expect(screen.queryByRole('link', { name: /Olivares AI/ })).toBeNull()
  })

  it('is not shown where the licence holds Olivares AI Business', async () => {
    answers({
      edition: 'enterprise',
      licensed_edition: 'business',
      license: { status: 'valid' },
    })
    await at('/agent-tools')
    expect(screen.queryByRole('link', { name: /Olivares AI/ })).toBeNull()
  })
})
