// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  createMemoryHistory,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { expect, it, vi } from 'vitest'

const transport = vi.hoisted(() => ({ unavailable: true, attempts: 0 }))
vi.mock('@tanstack/react-router', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@tanstack/react-router')>()
  return {
    ...actual,
    // Fail the import transport, retaining the installed router's real lazy
    // component, route matching, pending state and error boundary.
    lazyRouteComponent: (
      importer: () => Promise<{ AppLayout: () => ReactNode }>,
      name: 'AppLayout',
    ) =>
      actual.lazyRouteComponent(async () => {
        transport.attempts++
        if (transport.unavailable)
          throw new Error('Shell chunk transport fixture')
        return importer()
      }, name),
  }
})
vi.mock('@/components/layout/app-layout', async () => {
  const { Outlet } = await import('@tanstack/react-router')
  return {
    AppLayout: () => (
      <>
        <p>Shell ready</p>
        <Outlet />
      </>
    ),
  }
})
vi.mock('./pages/settings', () => ({
  SettingsPage: () => <p>Settings screen</p>,
}))

import { routeTree } from './routes'

it('offers translated recovery and retries the shell import before resetting the boundary', async () => {
  const user = userEvent.setup()
  vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: ['/settings?tab=profile'] }),
    defaultPendingMs: 0,
    defaultPendingMinMs: 0,
  })
  await router.load()
  render(<RouterProvider router={router} />)
  expect(
    await screen.findByRole('heading', { name: 'This view crashed' }),
  ).toBeInTheDocument()
  expect(screen.queryByText('Settings screen')).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Retry' }))
  await waitFor(() => expect(transport.attempts).toBe(2))
  expect(
    screen.getByRole('heading', { name: 'This view crashed' }),
  ).toBeInTheDocument()
  transport.unavailable = false
  await user.click(screen.getByRole('button', { name: 'Retry' }))
  expect(await screen.findByText('Shell ready')).toBeInTheDocument()
  expect(await screen.findByText('Settings screen')).toBeInTheDocument()
  expect(router.state.location.href).toBe('/settings?tab=profile')
})
