// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, expect, it, vi } from 'vitest'
import { useWorkspaceStore } from '@/stores/workspace'
import { WorkspaceSwitcher } from './workspace-switcher'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 'tenant-one' }),
}))
const { listWorkspaces } = vi.hoisted(() => ({ listWorkspaces: vi.fn() }))
vi.mock('@/features/console/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/console/api')>()
  return { ...actual, consoleApi: { ...actual.consoleApi, listWorkspaces } }
})

function show(variant: 'row' | 'card' = 'card') {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>
      <WorkspaceSwitcher variant={variant} />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  useWorkspaceStore.getState().clear()
  listWorkspaces.mockReset().mockResolvedValue({
    items: [
      {
        id: 'opaque-workspace-17',
        name: 'Default',
        slug: 'default',
        status: 'active',
        is_default: true,
        tenant_id: 'tenant-one',
        created_at: '2026-09-30T00:00:00Z',
        updated_at: '2026-09-30T00:00:00Z',
        version: 1,
      },
    ],
    has_more: false,
  })
})

it.each(['card', 'row'] as const)(
  'makes the sole workspace selectable in the %s control',
  async (variant) => {
    const user = userEvent.setup()
    show(variant)
    const trigger = await screen.findByRole('button')
    expect(useWorkspaceStore.getState().activeWorkspace).toBeNull()
    await user.click(trigger)
    await user.click(await screen.findByRole('menuitem', { name: /^Default/ }))
    expect(useWorkspaceStore.getState().activeWorkspace).toBe(
      'opaque-workspace-17',
    )
    expect(useWorkspaceStore.getState().activeWorkspaceName).toBe('Default')
  },
)

it('keeps All workspaces as an explicit null scope with a single workspace', async () => {
  const user = userEvent.setup()
  useWorkspaceStore
    .getState()
    .setActiveWorkspace('opaque-workspace-17', 'Default')
  show()
  await user.click(await screen.findByRole('button'))
  await user.click(
    await screen.findByRole('menuitem', { name: /^All workspaces/ }),
  )
  expect(useWorkspaceStore.getState().activeWorkspace).toBeNull()
  expect(useWorkspaceStore.getState().activeWorkspaceName).toBeNull()
})

it('does not offer an inactive workspace for selection', async () => {
  listWorkspaces.mockResolvedValue({
    items: [
      {
        id: 'inactive-id',
        name: 'Default',
        slug: 'default',
        status: 'disabled',
        is_default: true,
        tenant_id: 'tenant-one',
        created_at: '2026-09-30T00:00:00Z',
        updated_at: '2026-09-30T00:00:00Z',
        version: 1,
      },
    ],
    has_more: false,
  })
  const user = userEvent.setup()
  show()
  await user.click(await screen.findByRole('button'))
  expect(
    screen.queryByRole('menuitem', { name: /^Default/ }),
  ).not.toBeInTheDocument()
  expect(useWorkspaceStore.getState().activeWorkspace).toBeNull()
})
