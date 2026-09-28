// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A palette action the principal cannot run stays on screen. It is not a secret
// and it is not a button: aria-disabled, with the permission and who can grant it.
import { QueryClient } from '@tanstack/react-query'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { renderIntel } from '@/test/intel'
import { queryKeys } from '@/lib/api/query'

const navigateMock = vi.fn()
vi.mock('@tanstack/react-router', () => ({
  useRouterState: () => '',
  useNavigate: () => navigateMock,
}))

const authState = vi.hoisted(() => ({
  activeTenant: 'tnt-1' as string | null,
  can: (_permission: string): boolean => true,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))

import { CommandMenu } from './command-menu'
import { useCommandStore } from '@/stores/command'
import { useTenantStore } from '@/stores/tenant'

const READS = [
  'eventing:subscription:read',
  'notify:route:read',
  'orchestration:graph:read',
]
const WRITES = [
  'eventing:subscription:write',
  'notify:route:write',
  'orchestration:schedule:write',
]

function holding(permissions: string[]) {
  const held = new Set(permissions)
  authState.can = (permission: string) =>
    held.has(permission) ||
    (!READS.includes(permission) && !WRITES.includes(permission))
}

function client() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  qc.setQueryData(queryKeys.whoami, {
    kind: 'user',
    user_id: 'u-1',
    actor: 'u-1',
    display_name: 'Ada',
    superadmin: false,
    grants: [],
  })
  return qc
}

afterEach(() => {
  navigateMock.mockReset()
  authState.activeTenant = 'tnt-1'
  authState.can = () => true
  useTenantStore.setState({ activeTenant: 'tnt-1' })
  useCommandStore.setState({ pendingAction: null, opener: null, open: false })
})

describe('a denied palette action', () => {
  it('stays visible, aria-disabled, with the reason and who can help', async () => {
    const user = userEvent.setup()
    holding(READS)
    useCommandStore.getState().setOpen(true)
    renderIntel(<CommandMenu />, { queryClient: client() })
    await user.type(screen.getByRole('combobox'), 'New alert route')

    const option = screen.getByRole('option', { name: /New alert route/ })
    expect(option).toHaveAttribute('aria-disabled', 'true')
    expect(option).toHaveTextContent('notify:route:write')
    expect(option).toHaveTextContent(/administrator/i)
    await user.click(option)
    expect(useCommandStore.getState().pendingAction).toBeNull()
    expect(navigateMock).not.toHaveBeenCalled()
  })
})
