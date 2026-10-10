// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { beforeEach, expect, it, vi } from 'vitest'
import { useWorkspaceStore } from '@/stores/workspace'
import { WorkspaceDashboardView } from './workspace-dashboard-view'

vi.mock('@tanstack/react-router', () => ({
  Link: ({ to, children }: { to: string; children: ReactNode }) => (
    <a href={to}>{children}</a>
  ),
  useNavigate: () => () => {},
  useRouterState: () => '/',
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 'tenant-one',
    can: (permission: string) => permission === 'inventory:catalog:read',
  }),
}))
const { listWorkspaces, summary } = vi.hoisted(() => ({
  listWorkspaces: vi.fn(),
  summary: vi.fn(),
}))
vi.mock('@/features/console/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/console/api')>()
  return { ...actual, consoleApi: { ...actual.consoleApi, listWorkspaces } }
})
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return {
    ...actual,
    workspaceDashboardApi: {
      summary,
      agents: () => Promise.resolve({ items: [], has_more: false }),
      groups: () => Promise.resolve({ items: [], has_more: false }),
    },
  }
})
function show() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return {
    client,
    ...render(
      <QueryClientProvider client={client}>
        <WorkspaceDashboardView />
      </QueryClientProvider>,
    ),
  }
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
  summary.mockReset().mockResolvedValue({
    workspace_id: 'opaque-workspace-17',
    name: 'Default',
    slug: 'default',
    is_default: true,
    agent_count: 0,
    session_count: 0,
    resource_count: 0,
    group_count: 0,
  })
})
it('opens the sole active workspace overview without asking the user to select it', async () => {
  show()
  await waitFor(() =>
    expect(summary).toHaveBeenCalledWith('opaque-workspace-17'),
  )
  expect(useWorkspaceStore.getState().activeWorkspace).toBeNull()
  expect(
    await screen.findByRole('heading', { level: 1, name: 'Default' }),
  ).toBeVisible()
  expect(screen.queryByText('No workspace selected')).not.toBeInTheDocument()
})
it('does not turn an inactive workspace named Default into an available selection', async () => {
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
  const { client } = show()
  await waitFor(() => expect(client.isFetching()).toBe(0))
  await screen.findByRole('link', { name: 'Open inventory' })
  expect(
    screen.queryByRole('button', { name: 'Open Default' }),
  ).not.toBeInTheDocument()
  expect(useWorkspaceStore.getState().activeWorkspace).toBeNull()
  expect(summary).not.toHaveBeenCalled()
})

it.each([0, 2])(
  'keeps the inventory action without implicitly choosing from %i workspaces',
  async (count) => {
    listWorkspaces.mockResolvedValue({
      items: Array.from({ length: count }, (_, index) => ({
        id: `workspace-${index}`,
        name: index === 0 ? 'Default' : 'Engineering',
        slug: `workspace-${index}`,
        status: 'active',
        is_default: index === 0,
        tenant_id: 'tenant-one',
        created_at: '2026-09-30T00:00:00Z',
        updated_at: '2026-09-30T00:00:00Z',
        version: 1,
      })),
      has_more: false,
    })
    const { client } = show()
    await waitFor(() => expect(client.isFetching()).toBe(0))
    await screen.findByRole('link', { name: 'Open inventory' })
    expect(
      screen.queryByRole('button', { name: 'Open Default' }),
    ).not.toBeInTheDocument()
    expect(useWorkspaceStore.getState().activeWorkspace).toBeNull()
    expect(summary).not.toHaveBeenCalled()
  },
)

it('keeps an existing workspace selection', async () => {
  useWorkspaceStore.getState().setActiveWorkspace('selected-id', 'Engineering')
  show()
  await waitFor(() => expect(summary).toHaveBeenCalledWith('selected-id'))
  expect(listWorkspaces).not.toHaveBeenCalled()
  expect(useWorkspaceStore.getState().activeWorkspace).toBe('selected-id')
})

it('does not auto-select from an incomplete list, but Open still opens its overview', async () => {
  const user = userEvent.setup()
  const page = await listWorkspaces()
  listWorkspaces.mockResolvedValue({ ...page, has_more: true })
  show()
  const open = await screen.findByRole('button', { name: 'Open Default' })
  expect(useWorkspaceStore.getState().activeWorkspace).toBeNull()
  expect(summary).not.toHaveBeenCalled()
  await user.click(open)
  expect(
    await screen.findByRole('heading', { level: 1, name: 'Default' }),
  ).toBeVisible()
  expect(summary).toHaveBeenCalledWith('opaque-workspace-17')
})

it('does not invent a workspace when the workspace list fails', async () => {
  listWorkspaces.mockRejectedValue(new Error('Workspace list unavailable'))
  const { client } = show()
  await waitFor(() => expect(listWorkspaces).toHaveBeenCalled())
  await waitFor(() => expect(client.isFetching()).toBe(0))
  expect(screen.getByText('No workspace selected')).toBeVisible()
  expect(useWorkspaceStore.getState().activeWorkspace).toBeNull()
  expect(summary).not.toHaveBeenCalled()
})

it('keeps an explicit All workspaces scope while showing the sole workspace overview', async () => {
  useWorkspaceStore
    .getState()
    .setActiveWorkspace('opaque-workspace-17', 'Default')
  show()
  expect(
    await screen.findByRole('heading', { level: 1, name: 'Default' }),
  ).toBeVisible()
  act(() => useWorkspaceStore.getState().setActiveWorkspace(null))
  await waitFor(() => expect(listWorkspaces).toHaveBeenCalled())
  expect(
    await screen.findByRole('heading', { level: 1, name: 'Default' }),
  ).toBeVisible()
  expect(useWorkspaceStore.getState().activeWorkspace).toBeNull()
})
