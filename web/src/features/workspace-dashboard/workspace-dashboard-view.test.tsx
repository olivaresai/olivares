// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// C15-P3 — the last of the 53 console features with no test at all.
//
// The two properties worth pinning are not "it renders": they are the two ways this screen can
// LIE. Without a workspace selected it must request no workspace content, and without a summary
// it must say it does not know instead of printing a zero.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ReactNode } from 'react'
import { useModulesStore } from '@/stores/modules'
import { useWorkspaceStore } from '@/stores/workspace'

// El panel pinta `<Link>` y en jsdom no hay router: sin esto, `useLinkProps` revienta con
// «Cannot read properties of null (reading 'isServer')», que no dice nada del componente.
// Degradado a un ancla, como ya hace tenant-gate.test.tsx.
vi.mock('@tanstack/react-router', () => ({
  Link: ({ to, children }: { to: string; children: ReactNode }) => (
    <a href={to}>{children}</a>
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

const summaryMock = vi.fn()
const agentsMock = vi.fn()
const groupsMock = vi.fn()
const contentsMock = vi.fn()
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return {
    ...actual,
    workspaceDashboardApi: {
      summary: (...a: unknown[]) => summaryMock(...a),
      agents: (...a: unknown[]) => agentsMock(...a),
      groups: (...a: unknown[]) => groupsMock(...a),
      contents: (...a: unknown[]) => contentsMock(...a),
    },
  }
})

const { WorkspaceDashboardView } = await import('./workspace-dashboard-view')

function show() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <WorkspaceDashboardView />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  useWorkspaceStore.getState().clear()
  summaryMock.mockReset().mockResolvedValue({
    id: 'w1',
    name: 'Engineering',
    slug: 'eng',
    is_default: true,
    agent_count: 7,
    session_count: 3,
    resource_count: 11,
    group_count: 2,
  })
  agentsMock.mockReset().mockResolvedValue({ items: [], has_more: false })
  groupsMock.mockReset().mockResolvedValue({ items: [], has_more: false })
  granted.clear()
  granted.add('inventory:catalog:read')
  contentsMock.mockReset().mockResolvedValue({
    workspace_id: 'w1',
    kinds: [{ kind: 'core.agent', count: 7, capped: false }],
  })
})

afterEach(() => useModulesStore.getState().setOff([]))

describe('WorkspaceDashboardView', () => {
  /**
   * THE CONTROL: with NO workspace selected the view reads no workspace content.
   * The authorized workspace catalog is still available for explicit selection.
   *
   * THE MUTATION: move the guard below the queries — the screen still renders, and the three
   * reads go out with an empty workspace id. That is a request the engine answers 400 for, on
   * every mount, and it looks like a broken panel rather than a screen waiting for a choice.
   *
   * THE NON-FIRING DIRECTION is the next case: with a workspace the reads MUST happen, so a
   * view that never queried would satisfy this one and fail that.
   */
  it('does not read workspace content until a workspace is chosen', async () => {
    show()
    expect(
      await screen.findByText(/Select a workspace in the navigation rail/i),
    ).toBeInTheDocument()
    expect(summaryMock).not.toHaveBeenCalled()
    expect(agentsMock).not.toHaveBeenCalled()
    expect(groupsMock).not.toHaveBeenCalled()
  })

  it('offers a next action when no workspace is selected', async () => {
    show()
    expect(
      await screen.findByRole('link', { name: 'Open inventory' }),
    ).toHaveAttribute('href', '/inventory')
    expect(screen.getByText(/or go to the tenant-wide inventory/)).toBeVisible()
  })

  it('reads the three sources once a workspace is chosen', async () => {
    useWorkspaceStore.getState().setActiveWorkspace('w1')
    show()
    await waitFor(() => expect(summaryMock).toHaveBeenCalledWith('w1'))
    expect(agentsMock).toHaveBeenCalledWith('w1')
    expect(groupsMock).toHaveBeenCalledWith('w1')
    expect(await screen.findByText('7')).toBeInTheDocument()
  })

  it('shows the contents read only to principals who hold the route tier', async () => {
    useWorkspaceStore.getState().setActiveWorkspace('w1')
    const viewer = show()
    expect(await screen.findByText('7')).toBeInTheDocument()
    expect(
      screen.queryByRole('region', { name: 'Workspace contents' }),
    ).not.toBeInTheDocument()
    expect(contentsMock).not.toHaveBeenCalled()
    viewer.unmount()

    granted.add('tenant:admin')
    show()
    expect(
      await screen.findByRole('row', { name: 'core.agent 7' }),
    ).toBeInTheDocument()
    expect(contentsMock).toHaveBeenCalledWith(
      'w1',
      expect.objectContaining({ tenant: 't1' }),
    )
  })

  /**
   * THE CONTROL: without a summary the tiles say "—", NOT zero.
   *
   * This is the difference between "I do not know" and "there are none", and printing a zero
   * for the first is the same class of defect this repository keeps closing elsewhere: a screen
   * that answers a question it could not observe. An operator reading "0 agents" on a workspace
   * that has seven acts on it.
   *
   * THE MUTATION: `value={formatInt(s?.agent_count ?? 0)}`. The tiles read 0 and this fires.
   */
  it('says it does not know, rather than printing a zero', async () => {
    useWorkspaceStore.getState().setActiveWorkspace('w1')
    summaryMock.mockRejectedValue(new Error('unavailable'))
    show()
    await waitFor(() =>
      expect(screen.getAllByText('—').length).toBeGreaterThan(0),
    )
    expect(screen.queryByText('0')).not.toBeInTheDocument()
  })

  it('mounts the agents truncation badge from the loaded rows only when has_more is true', async () => {
    useWorkspaceStore.getState().setActiveWorkspace('w1')
    agentsMock.mockResolvedValue({
      items: Array.from({ length: 7 }, (_, i) => ({
        id: `agent-${i}`,
        name: `Agent ${i}`,
        kind: 'assistant',
        status: 'active',
      })),
      has_more: true,
    })
    show()
    expect(
      await screen.findByText(/Loaded 7 rows; there are more/i),
    ).toHaveAttribute('title', expect.stringMatching(/Use View all/))
  })

  it('mounts the groups truncation badge from the loaded rows only when has_more is true', async () => {
    useWorkspaceStore.getState().setActiveWorkspace('w1')
    groupsMock.mockResolvedValue({
      items: Array.from({ length: 6 }, (_, i) => ({
        id: `group-${i}`,
        name: `Group ${i}`,
        slug: `group-${i}`,
        status: 'active',
      })),
      has_more: true,
    })
    show()
    expect(
      await screen.findByText(/Loaded 6 rows; there are more/i),
    ).toBeVisible()
  })

  it('keeps eleven complete rows when has_more is false instead of silently slicing to ten', async () => {
    useWorkspaceStore.getState().setActiveWorkspace('w1')
    agentsMock.mockResolvedValue({
      items: Array.from({ length: 11 }, (_, i) => ({
        id: `agent-${i}`,
        name: `Agent ${i}`,
        kind: 'assistant',
        status: 'active',
      })),
      has_more: false,
    })
    show()
    expect(await screen.findByText('Agent 10')).toBeVisible()
    expect(screen.queryByText(/there are more/i)).not.toBeInTheDocument()
  })

  /**
   * THE CONTROL (#475): a page link to Inventory is offered only where Inventory opens. A
   * fresh install runs without the inventory module, and /inventory then shows only the
   * not-enabled notice, so these actions led nowhere.
   *
   * THE MUTATION: drop the `navigable(INVENTORY_VIEW)` gate. The three /inventory links come
   * back and both cases fire.
   */
  describe.each([
    ['the inventory module is off', () => useModulesStore.getState().setOff(['inventory'])],
    ['the principal cannot read the inventory', () => granted.delete('inventory:catalog:read')],
  ])('when %s', (_, close) => {
    it('offers no inventory action on the selection page', async () => {
      close()
      show()
      expect(
        await screen.findByText(/Select a workspace in the navigation rail/i),
      ).toBeInTheDocument()
      expect(screen.queryByText(/inventory/i)).not.toBeInTheDocument()
      expect(screen.queryByRole('link')).not.toBeInTheDocument()
    })

    it('offers no inventory action on the workspace overview', async () => {
      close()
      useWorkspaceStore.getState().setActiveWorkspace('w1')
      agentsMock.mockResolvedValue({
        items: Array.from({ length: 7 }, (_, i) => ({
          id: `agent-${i}`,
          name: `Agent ${i}`,
          kind: 'assistant',
          status: 'active',
        })),
        has_more: true,
      })
      const page = show()
      expect(
        await screen.findByText(/Loaded 7 rows; there are more/i),
      ).toBeVisible()
      expect(screen.queryByTitle(/View all|inventory/i)).not.toBeInTheDocument()
      expect(
        screen.getAllByRole('link').map((a) => a.getAttribute('href')),
      ).toEqual(['/console', '/console'])
      page.unmount()

      agentsMock.mockResolvedValue({ items: [], has_more: false })
      show()
      expect(
        await screen.findByText('No agents in this workspace yet.'),
      ).toBeVisible()
      expect(
        screen.getAllByRole('link').map((a) => a.getAttribute('href')),
      ).toEqual(['/console', '/console'])
    })
  })

  it('offers the inventory actions on the workspace overview where Inventory opens', async () => {
    useWorkspaceStore.getState().setActiveWorkspace('w1')
    show()
    expect(
      await screen.findByRole('link', { name: 'Open inventory' }),
    ).toHaveAttribute('href', '/inventory')
    expect(
      screen.getAllByRole('link').map((a) => a.getAttribute('href')),
    ).toEqual(['/inventory', '/inventory', '/console', '/console'])
  })
})
