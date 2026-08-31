// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactElement, ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import './i18n'

const { api, authState } = vi.hoisted(() => ({
  api: {
    listGroups: vi.fn(),
    setGroupRole: vi.fn(),
    setGroupParent: vi.fn(),
  },
  authState: {
    activeTenant: 't1' as string | null,
    activeRole: 'admin' as string | null,
    isSuperadmin: false,
    principal: { aal: 3 } as { aal?: number } | null,
    can: (_p: string): boolean => true,
  },
}))

vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('@/features/identity/assurance', () => ({
  AAL: { PASSWORD: 1, MFA: 2, HARDWARE: 3 },
  RequireAssurance: ({ children }: { children: ReactNode }) => <>{children}</>,
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, consoleApi: api }
})

import { ApiError } from '@/lib/api/errors'
import { GroupHierarchySection } from './roles-groups-section'

function wrap(ui: ReactElement) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

const engineering = {
  id: 'g-eng',
  display_name: 'Engineering',
  external_id: 'ent-1',
  mapped_role: '',
  parent_group_id: '',
  members: 42,
}

beforeEach(() => {
  vi.clearAllMocks()
  api.listGroups.mockResolvedValue({ groups: [engineering] })
  api.setGroupRole.mockResolvedValue({
    id: 'g-eng',
    display_name: 'Engineering',
    mapped_role: 'editor',
  })
})

describe('group role mapping', () => {
  it('states the blast radius before the operator commits', async () => {
    const user = userEvent.setup()
    wrap(<GroupHierarchySection canAdmin />)

    await user.click(await screen.findByRole('button', { name: 'Map role' }))
    // Mapping a role grants it to every current member at once. The count must
    // be in front of the operator BEFORE they choose, not discovered after.
    expect(
      await screen.findByText(/All 42 of its current members gain this role/i),
    ).toBeInTheDocument()
  })

  it('maps a role and clears it through the same verb', async () => {
    const user = userEvent.setup()
    wrap(<GroupHierarchySection canAdmin />)

    await user.click(await screen.findByRole('button', { name: 'Map role' }))
    await user.click(screen.getByRole('combobox', { name: /tenant role/i }))
    await user.click(screen.getByRole('option', { name: 'editor' }))
    await user.click(screen.getByRole('button', { name: 'Save mapping' }))

    await waitFor(() =>
      expect(api.setGroupRole).toHaveBeenCalledWith('g-eng', 'editor'),
    )

    // "No mapping" must reach the server as the empty role the clear path
    // expects — never as the picker's internal sentinel.
    await user.click(await screen.findByRole('button', { name: 'Map role' }))
    await user.click(screen.getByRole('combobox', { name: /tenant role/i }))
    await user.click(screen.getByRole('option', { name: 'No mapping' }))
    await user.click(screen.getByRole('button', { name: 'Save mapping' }))
    await waitFor(() =>
      expect(api.setGroupRole).toHaveBeenLastCalledWith('g-eng', ''),
    )
  })

  it('explains a role-ceiling refusal instead of blaming the operator permissions', async () => {
    // The server refuses a rank above the actor's own with its own code. That
    // is NOT a missing permission: telling an admin they are "not authorized"
    // sends them chasing access they already hold, when what they need is a
    // more senior human. The message here is deliberately worded differently
    // from the server's, so a client that matched prose would fail this.
    api.setGroupRole.mockRejectedValue(
      new ApiError(403, 'role_ceiling', 'server prose that may change'),
    )
    const user = userEvent.setup()
    wrap(<GroupHierarchySection canAdmin />)

    await user.click(await screen.findByRole('button', { name: 'Map role' }))
    await user.click(screen.getByRole('combobox', { name: /tenant role/i }))
    await user.click(screen.getByRole('option', { name: 'owner' }))
    await user.click(screen.getByRole('button', { name: 'Save mapping' }))

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(/cannot grant a role above your own rank/i)
    expect(alert).toHaveTextContent(/not a missing permission/i)
  })
})
