// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactElement, ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import './i18n'

const { api, authState, panels } = vi.hoisted(() => ({
  api: {
    listGroups: vi.fn(),
    setGroupRole: vi.fn(),
  },
  panels: {} as { groupParentActions?: unknown },
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
vi.mock('@/features/extensions', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/extensions')>()
  return {
    ...actual,
    get PANEL_EXTENSIONS() {
      // Set explicitly: a Business build composes its own nesting actions.
      return {
        ...actual.PANEL_EXTENSIONS,
        groupParentActions: panels.groupParentActions,
      }
    },
  }
})
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
  delete panels.groupParentActions
  api.listGroups.mockResolvedValue({ groups: [engineering] })
  api.setGroupRole.mockResolvedValue({
    id: 'g-eng',
    display_name: 'Engineering',
    mapped_role: 'editor',
  })
})

describe('group role mapping', () => {
  it('keeps a named provisioner group read-only even for an admin', async () => {
    api.listGroups.mockResolvedValue({
      groups: [
        {
          ...engineering,
          provisioned_by: 'directory-a',
          parent_group_id: 'g-parent',
        },
      ],
    })
    wrap(<GroupHierarchySection canAdmin />)
    const row = (await screen.findByText('Engineering')).closest('tr')!
    expect(
      within(row).getByText('Managed by directory-a · read-only'),
    ).toBeInTheDocument()
    expect(within(row).queryByRole('button')).not.toBeInTheDocument()
    expect(api.setGroupRole).not.toHaveBeenCalled()
  })

  it.each(['', 'operator'])(
    'preserves console controls for origin %j',
    async (origin) => {
      api.listGroups.mockResolvedValue({
        groups: [{ ...engineering, provisioned_by: origin }],
      })
      wrap(<GroupHierarchySection canAdmin />)
      expect(
        await screen.findByRole('button', { name: 'Map role' }),
      ).toBeEnabled()
      expect(screen.queryByText(/read-only/)).not.toBeInTheDocument()
    },
  )

  // Group nesting is Business: the Community engine answers 501 to the nesting
  // route, so the Community console shows the parent read-only and offers no
  // control for it.
  it('shows the parent read-only, with no nesting control', async () => {
    api.listGroups.mockResolvedValue({
      groups: [
        { ...engineering, id: 'g-parent', display_name: 'Company' },
        { ...engineering, parent_group_id: 'g-parent' },
      ],
    })
    wrap(<GroupHierarchySection canAdmin />)
    const row = (await screen.findByText('Engineering')).closest('tr')!
    expect(within(row).getByText('Company')).toBeInTheDocument()
    expect(
      within(row).queryByRole('button', { name: /parent/i }),
    ).not.toBeInTheDocument()
    expect(within(row).getByRole('button', { name: 'Map role' })).toBeEnabled()
  })

  // A Business build adds the nesting controls through its panel extension,
  // given the row's group and the groups it may be nested under.
  it('renders the edition nesting actions on operator rows only', async () => {
    panels.groupParentActions = ({
      group,
      groups,
    }: {
      group: { display_name: string }
      groups: readonly unknown[]
    }) => (
      <button type="button">
        {`Nest ${group.display_name} among ${groups.length}`}
      </button>
    )
    api.listGroups.mockResolvedValue({
      groups: [
        engineering,
        {
          ...engineering,
          id: 'g-dir',
          display_name: 'Directory',
          provisioned_by: 'directory-a',
        },
      ],
    })
    wrap(<GroupHierarchySection canAdmin />)
    expect(
      await screen.findByRole('button', { name: 'Nest Engineering among 2' }),
    ).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: /Nest Directory/ }),
    ).not.toBeInTheDocument()
  })

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
