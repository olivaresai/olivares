// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The kill switch on every page: one pill in the top bar while a stop is engaged, nothing
// otherwise, and no read for a principal who may not open the kill switch.
import type { ComponentProps } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen, DEFAULT_AUTH } from '@/test/intel'
import { killswitchApi } from '@/features/killswitch/api'
import type { KillSwitchDTO } from '@/features/killswitch/types'
import { KillSwitchStatus } from './killswitch-status'

const auth = vi.hoisted(() => ({ allowed: true }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    ...DEFAULT_AUTH,
    activeTenant: 't-demo',
    can: () => auth.allowed,
  }),
}))
vi.mock('@tanstack/react-router', () => ({
  Link: ({ children, to, ...rest }: ComponentProps<'a'> & { to: string }) => (
    <a href={to} {...rest}>
      {children}
    </a>
  ),
}))

const stop = (over: Partial<KillSwitchDTO>): KillSwitchDTO => ({
  id: 'ks-1',
  scope_kind: 'agent',
  status: 'active',
  source: 'operator',
  engaged_aal: 3,
  engage_audit_seq: 1,
  revoked_approvals: 0,
  reviewed: false,
  ...over,
})

afterEach(() => {
  auth.allowed = true
  vi.restoreAllMocks()
})

describe('KillSwitchStatus', () => {
  it('names an estate stop in the danger role and opens the kill switch', async () => {
    vi.spyOn(killswitchApi, 'state').mockResolvedValue({
      estate_stopped: true,
      active: [stop({ scope_kind: 'estate' })],
    })
    renderIntel(<KillSwitchStatus />)
    const pill = await screen.findByTestId('killswitch-status')
    expect(pill).toHaveTextContent('ALL AGENTS STOPPED')
    expect(pill).toHaveAttribute('href', '/killswitch')
    expect(pill.className).toContain('text-danger')
    // The live region wraps the link; a link may not take the status role itself.
    expect(pill).not.toHaveAttribute('role')
    expect(pill.parentElement).toHaveAttribute('role', 'status')
  })

  it('counts agent stops in the warning role when the estate runs', async () => {
    vi.spyOn(killswitchApi, 'state').mockResolvedValue({
      estate_stopped: false,
      active: [stop({ id: 'a' }), stop({ id: 'b' })],
    })
    renderIntel(<KillSwitchStatus />)
    const pill = await screen.findByTestId('killswitch-status')
    expect(pill).toHaveTextContent('2 agent stops active')
    expect(pill.className).toContain('text-warning')
  })

  it('draws nothing when no stop is active', async () => {
    const read = vi
      .spyOn(killswitchApi, 'state')
      .mockResolvedValue({ estate_stopped: false, active: [] })
    renderIntel(<KillSwitchStatus />)
    await vi.waitFor(() => expect(read).toHaveBeenCalled())
    expect(screen.queryByTestId('killswitch-status')).toBeNull()
  })

  it('reads nothing for a principal who may not open the kill switch', () => {
    auth.allowed = false
    const read = vi.spyOn(killswitchApi, 'state')
    renderIntel(<KillSwitchStatus />)
    expect(read).not.toHaveBeenCalled()
    expect(screen.queryByTestId('killswitch-status')).toBeNull()
  })
})
