// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Issue #1788: the Workspace page showed only the organization workspace. The folders
// sessions work in are registered by the sessions module; the page lists them and says
// how they differ from the workspace, where Sessions opens them.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { agentOpsApi } from '@/features/agentops/api'
import type { WorkspaceDTO } from '@/features/agentops/types'
import { useModulesStore } from '@/stores/modules'
import { useWorkspaceStore } from '@/stores/workspace'

vi.mock('@tanstack/react-router', () => ({
  Link: ({
    to,
    search,
    children,
  }: {
    to: string
    search?: Record<string, string>
    children: ReactNode
  }) => (
    <a href={search ? `${to}?${new URLSearchParams(search)}` : to}>
      {children}
    </a>
  ),
  useNavigate: () => () => {},
  useRouterState: () => '/',
}))

const granted = new Set<string>()
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    can: (permission: string) => granted.has(permission),
  }),
}))

vi.mock('@/features/console/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/console/api')>()
  return {
    ...actual,
    consoleApi: {
      ...actual.consoleApi,
      listWorkspaces: () => Promise.resolve({ items: [], has_more: false }),
    },
  }
})

vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return {
    ...actual,
    workspaceDashboardApi: {
      summary: () =>
        Promise.resolve({
          id: 'w1',
          name: 'Default',
          slug: 'default',
          is_default: true,
          agent_count: 0,
          session_count: 0,
          resource_count: 0,
          group_count: 0,
        }),
      agents: () => Promise.resolve({ items: [], has_more: false }),
      groups: () => Promise.resolve({ items: [], has_more: false }),
      contents: () => Promise.resolve({ workspace_id: 'w1', kinds: [] }),
    },
  }
})

const { WorkspaceDashboardView } = await import('./workspace-dashboard-view')

const folder = (
  ref: string,
  name: string | undefined,
  path: string,
): WorkspaceDTO => ({
  workspace_ref: ref,
  name,
  root_path: path,
  mount_mode: 'rw',
  max_read_bytes: 0,
  dlp_mode: 'label',
  state: 'active',
})

function folders(items: WorkspaceDTO[], has_more = false) {
  return vi
    .spyOn(agentOpsApi, 'listWorkspaces')
    .mockResolvedValue({ items, has_more })
}

function show() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <WorkspaceDashboardView />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.restoreAllMocks()
  useWorkspaceStore.getState().clear()
  granted.clear()
  granted.add('sessions:run:read')
  granted.add('sessions:workspace:read')
})

afterEach(() => useModulesStore.getState().setOff([]))

describe('session folders on the Workspace page', () => {
  it('lists the folders sessions use, beside the organization workspace', async () => {
    useWorkspaceStore.getState().setActiveWorkspace('w1')
    const read = folders([
      folder('ws_1', 'payments-api', '/srv/work/payments-api'),
      folder('ws_2', undefined, '/srv/work/web-app'),
    ])
    show()
    expect(
      await screen.findByRole('heading', { name: 'Session folders' }),
    ).toBeVisible()
    expect(
      await screen.findByRole('row', {
        name: /^payments-api \/srv\/work\/payments-api/,
      }),
    ).toBeVisible()
    // A folder registered without a name shows the last segment of its path.
    expect(
      screen.getByRole('row', { name: /^web-app \/srv\/work\/web-app/ }),
    ).toBeVisible()
    expect(read).toHaveBeenCalledWith(
      expect.objectContaining({ state: 'active' }),
      expect.objectContaining({ tenant: 't1' }),
    )
    // The Workspaces tab opens for a principal who may read runs (the /agentops door),
    // whether or not they may observe live sessions.
    expect(
      screen.getByRole('link', { name: 'Manage folders' }),
    ).toHaveAttribute('href', '/agentops?tab=workspaces')
    expect(screen.getByText(/separate from workspaces/)).toBeVisible()
  })

  it('shows them on the selection page too, when no workspace is chosen', async () => {
    folders([folder('ws_1', 'payments-api', '/srv/work/payments-api')])
    show()
    expect(await screen.findByText('No workspace selected')).toBeVisible()
    expect(
      await screen.findByRole('row', {
        name: /^payments-api \/srv\/work\/payments-api/,
      }),
    ).toBeVisible()
  })

  it('says where a folder comes from when none is registered yet', async () => {
    useWorkspaceStore.getState().setActiveWorkspace('w1')
    folders([])
    show()
    expect(await screen.findByText(/No session folders yet/)).toBeVisible()
  })

  it('says when the list is cut short', async () => {
    useWorkspaceStore.getState().setActiveWorkspace('w1')
    folders([folder('ws_1', 'a', '/srv/a')], true)
    show()
    expect(
      await screen.findByText(/Loaded 1 rows; there are more/),
    ).toBeVisible()
  })

  it('says it is reading them, not that there are none', async () => {
    useWorkspaceStore.getState().setActiveWorkspace('w1')
    vi.spyOn(agentOpsApi, 'listWorkspaces').mockReturnValue(
      new Promise(() => {}),
    )
    show()
    expect(await screen.findByText('Reading session folders…')).toBeVisible()
    expect(screen.queryByText(/No session folders yet/)).toBeNull()
  })

  it('says it cannot read them, not that there are none', async () => {
    useWorkspaceStore.getState().setActiveWorkspace('w1')
    vi.spyOn(agentOpsApi, 'listWorkspaces').mockRejectedValue(
      new Error('offline'),
    )
    show()
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Session folders are unavailable.',
    )
    expect(screen.queryByText(/No session folders yet/)).toBeNull()
  })

  // THE MUTATION: drop the gate. A principal who cannot read folders, or an install with the
  // sessions module off, would send a read the engine refuses and see an error panel.
  describe.each([
    [
      'the sessions module is off',
      () => useModulesStore.getState().setOff(['sessions']),
    ],
    [
      'the principal cannot read folders',
      () => granted.delete('sessions:workspace:read'),
    ],
  ])('when %s', (_, deny) => {
    it('offers no folders and reads none', async () => {
      useWorkspaceStore.getState().setActiveWorkspace('w1')
      deny()
      const read = folders([folder('ws_1', 'a', '/srv/a')])
      show()
      // The overview has rendered: its slug, not the title the "Default" badge repeats.
      await screen.findByText('default')
      expect(screen.queryByText('Session folders')).toBeNull()
      expect(read).not.toHaveBeenCalled()
    })
  })

  // The link opens the Workspaces tab, which needs sessions:run:read. Observing live
  // sessions (sessions:live:read) opens Sessions without that tab.
  it.each([
    ['cannot read runs', () => granted.delete('sessions:run:read')],
    [
      'can only observe live sessions',
      () => {
        granted.delete('sessions:run:read')
        granted.add('sessions:live:read')
      },
    ],
  ])(
    'lists the folders without a link when the principal %s',
    async (_, deny) => {
      useWorkspaceStore.getState().setActiveWorkspace('w1')
      deny()
      folders([folder('ws_1', 'a', '/srv/a')])
      show()
      expect(
        await screen.findByRole('row', { name: /^a \/srv\/a/ }),
      ).toBeVisible()
      expect(screen.queryByRole('link', { name: 'Manage folders' })).toBeNull()
    },
  )
})
