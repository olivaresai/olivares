// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  createMemoryHistory,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { beforeEach, expect, it, vi } from 'vitest'

const transport = vi.hoisted(
  (): { unavailable: boolean; attempts: number; error: unknown } => ({
    unavailable: true,
    attempts: 0,
    error: new Error('Shell chunk transport fixture'),
  }),
)
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
        if (transport.unavailable) throw transport.error
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

beforeEach(() => {
  vi.resetModules()
  transport.unavailable = true
  transport.attempts = 0
  transport.error = new Error('Shell chunk transport fixture')
  vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
})

it.each([
  ['Error', new Error('Shell chunk transport fixture')],
  ['string', 'private transport detail'],
  ['object', { message: 'private transport detail' }],
])(
  'offers translated recovery and reloads the document after a shell import throws an %s',
  async (_, error) => {
    transport.error = error
    const { routeTree } = await import('./routes')
    const user = userEvent.setup()
    const reload = vi.fn()
    const realLocation = window.location
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: { ...realLocation, reload },
    })
    try {
      const router = createRouter({
        routeTree,
        history: createMemoryHistory({
          initialEntries: ['/settings?tab=profile'],
        }),
        defaultPendingMs: 0,
        defaultPendingMinMs: 0,
      })
      await router.load()
      render(<RouterProvider router={router} />)
      expect(
        await screen.findByRole('heading', { name: 'This view crashed' }),
      ).toBeInTheDocument()
      expect(screen.queryByText('Settings screen')).not.toBeInTheDocument()
      expect(screen.getByRole('alert')).toHaveTextContent('This view crashed')
      expect(
        screen.queryByText(
          /private transport detail|Shell chunk transport fixture/,
        ),
      ).not.toBeInTheDocument()
      await user.tab()
      expect(screen.getByRole('button', { name: 'Retry' })).toHaveFocus()
      await user.keyboard('{Enter}')
      expect(reload).toHaveBeenCalledTimes(1)
      expect(transport.attempts).toBe(1)
      expect(router.state.location.href).toBe('/settings?tab=profile')
    } finally {
      Object.defineProperty(window, 'location', {
        configurable: true,
        value: realLocation,
      })
      vi.restoreAllMocks()
    }
  },
)
