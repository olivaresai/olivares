// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The boot splash, while the shell waits for the sign-in and the engine's tool table: the
// frame of the shell it opens, the brand mark and one status line, never a spinner. A load
// faster than 300 ms shows only the frame's color, so nothing blinks away.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen } from '@testing-library/react'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@tanstack/react-router', async (orig) => ({
  ...((await orig()) as Record<string, unknown>),
  useRouterState: ({
    select,
  }: {
    select: (s: { location: { pathname: string } }) => unknown
  }) => select({ location: { pathname: '/audit' } }),
  useNavigate: () => vi.fn(),
  useRouter: () => ({ state: { location: { href: '/audit' } } }),
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ status: 'loading', activeTenant: null, grants: [] }),
}))
vi.mock('@/lib/hooks/use-server-info', () => ({
  useServerInfo: () => ({ isPending: true, data: undefined }),
}))

import { AppLayout } from './app-layout'

function Providers({ children }: { children: ReactNode }) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>
}

beforeEach(() => vi.useFakeTimers())
afterEach(() => vi.useRealTimers())

describe('the boot splash', () => {
  it('paints only the frame for its first 300 ms, then the mark and one status line', () => {
    const { container } = render(<AppLayout />, { wrapper: Providers })
    const splash = container.firstElementChild as HTMLElement
    expect(splash).toHaveClass('bg-frame')
    expect(splash).toHaveAttribute('aria-busy', 'true')
    expect(screen.getByRole('status').textContent).toBe('')
    expect(splash.querySelector('svg')).toBeNull()

    act(() => vi.advanceTimersByTime(300))
    expect(screen.getByRole('status')).toHaveTextContent('Loading…')
    expect(splash.querySelector('svg')).toHaveAttribute('aria-hidden', 'true')
  })

  it('never spins: nothing in it moves, so reduced motion has nothing to stop', () => {
    const { container } = render(<AppLayout />, { wrapper: Providers })
    act(() => vi.advanceTimersByTime(300))
    expect(container.querySelector('[class*="animate-"]')).toBeNull()
    expect(screen.getAllByRole('status')).toHaveLength(1)
  })
})
