// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Stop all is a safety control, so it never does nothing (#3161). The status bar loads the
// kill switch's confirmation on demand; when that chunk cannot load (a tab left open across
// an upgrade), the click opens the kill switch page, whose own Stop all is there.
import { within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { renderIntel, screen } from '@/test/intel'

const navigate = vi.hoisted(() => vi.fn())

vi.mock('@/features/killswitch/engage-card', () => {
  throw new Error('Failed to fetch dynamically imported module')
})
vi.mock('@/lib/hooks/use-server-info', () => ({
  useServerInfo: () => ({ data: { version: '1.0' }, isError: false }),
}))
vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => navigate,
  useRouterState: ({ select }: { select: (s: unknown) => unknown }) =>
    select({ location: { pathname: '/console', search: {} } }),
  Link: ({ children }: { children?: ReactNode }) => <a>{children}</a>,
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    status: 'authenticated',
    principal: {
      actor: 'user:operator',
      superadmin: false,
      grants: [{ tenant: 't1', role: 'owner' }],
    },
    grants: [{ tenant: 't1', role: 'owner' }],
    activeTenant: 't1',
    activeRole: 'owner',
    isSuperadmin: false,
    isAuthenticated: true,
    can: () => true,
  }),
}))
vi.mock('./use-session-rail', () => ({
  useSessionRail: () => ({
    sessionCounts: { needsYou: 0, live: 2, running: 2 },
  }),
}))
vi.mock('./use-killswitch-state', () => ({
  useKillSwitchState: () => ({
    permitted: true,
    posture: { estate: false, active: 0, agentStops: 0 },
  }),
}))

import { StatusBar } from './status-bar'

describe('Stop all when its confirmation cannot load', () => {
  it('opens the kill switch page instead of doing nothing', async () => {
    const user = userEvent.setup()
    renderIntel(<StatusBar />)
    const bar = screen.getByRole('contentinfo', { name: 'Status' })
    await user.click(within(bar).getByRole('button', { name: 'Stop all' }))
    await vi.waitFor(() =>
      expect(navigate).toHaveBeenCalledWith({ to: '/killswitch' }),
    )
  })
})
