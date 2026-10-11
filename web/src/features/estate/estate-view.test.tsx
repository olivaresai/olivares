// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useModulesStore } from '@/stores/modules'
import type { EstateFamily, EstatePage } from './types'

const wire = vi.hoisted(() => ({
  read: vi.fn(),
  tenant: 'tenant-a',
  lifetime: 1,
  allowed: new Set<string>(),
  start: vi.fn(),
  navigate: vi.fn(),
}))
vi.mock('./api', () => ({ readEstateFamily: wire.read }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: wire.tenant,
    isSuperadmin: false,
    can: (permission: string) => wire.allowed.has(permission),
  }),
}))
vi.mock('@/lib/auth/capabilities', async (original) => ({
  ...(await original<typeof import('@/lib/auth/capabilities')>()),
  useCapabilityPreflight: () => {
    const context = {
      principalKind: 'user',
      actor: 'fixture-actor',
      tenant: wire.tenant,
      workspace: null,
      credentialGeneration: 0,
      lifetime: wire.lifetime,
    }
    return {
      context,
      live: () => ({
        ...context,
        tenant: wire.tenant,
        lifetime: wire.lifetime,
      }),
    }
  },
}))
vi.mock('@tanstack/react-router', async (original) => ({
  ...(await original<typeof import('@tanstack/react-router')>()),
  useNavigate: () => wire.navigate,
}))
vi.mock('@/features/agentops/session-launch', async (original) => ({
  ...(await original<typeof import('@/features/agentops/session-launch')>()),
  launchSession: wire.start,
}))
import { agentOpsApi } from '@/features/agentops/api'
import { readinessOf } from '@/features/first-hour/readiness.fixture'
import { workspaceDashboardApi } from '@/features/workspace-dashboard/api'
import { NewSessionHost } from '@/features/first-hour/new-session-host'
import { useNewSessionDialog } from '@/features/first-hour/new-session-store'
import EstateView from './estate-view'

const page = (label: string): EstatePage => ({
  nodes: [{ kind: 'folder', ref: label, label, status: 'active' }],
  hasMore: false,
  readAt: '2026-10-02T12:00:00Z',
})
const empty: EstatePage = {
  nodes: [],
  hasMore: false,
  readAt: '2026-10-02T12:00:00Z',
}
function show() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const element = () => (
    <QueryClientProvider client={client}>
      <EstateView />
      <NewSessionHost />
    </QueryClientProvider>
  )
  return { ...render(element()), element }
}

beforeEach(() => {
  useNewSessionDialog.setState({ open: false, opener: null, advanced: null })
  wire.read.mockReset()
  wire.tenant = 'tenant-a'
  wire.lifetime = 1
  wire.allowed = new Set(['sessions:run:read', 'sessions:workspace:read'])
  wire.navigate.mockReset()
  wire.start.mockReset().mockResolvedValue({ run_ref: 'run-a' })
  vi.spyOn(agentOpsApi, 'toolsReadiness').mockResolvedValue(
    readinessOf({ claude: 'own_login' }),
  )
  useModulesStore.getState().setOff([])
  wire.read.mockImplementation(async (family: EstateFamily) =>
    family === 'folder' ? page('Disconnected folder') : empty,
  )
})

describe('estate list and inspector', () => {
  it('keeps native session launch in the page header and withdraws it when permission is revoked', async () => {
    wire.allowed.add('sessions:run:write')
    wire.allowed.add('orchestration:schedule:read')
    const view = show()
    await screen.findByRole('button', { name: /Disconnected folder/ })
    const header = screen
      .getByRole('heading', { name: 'Estate', level: 1 })
      .closest('[data-slot="page-header"]')
    const start = screen.getByRole('button', { name: 'Start session' })
    expect(header).toContainElement(start)
    expect(header).toContainElement(
      screen.getByRole('link', { name: 'Open automations' }),
    )
    await userEvent.click(start)
    expect(await screen.findByRole('dialog')).not.toHaveTextContent(
      'Provider profile',
    )
    await userEvent.click(await screen.findByRole('button', { name: 'Start' }))
    expect(wire.start).toHaveBeenCalledWith(
      {
        quick: {
          driver: 'claude',
          folder: '',
          permission: 'editsAndCommands',
          secretEnv: [],
        },
        message: '',
      },
      expect.objectContaining({
        signal: expect.any(AbortSignal),
        dispatchGuard: expect.any(Function),
      }),
    )
    await waitFor(() =>
      expect(wire.navigate).toHaveBeenCalledWith({
        to: '/sessions',
        search: { session: 'run:run-a', pane: 'narrative' },
      }),
    )
    await userEvent.click(screen.getByRole('button', { name: 'Start session' }))
    await screen.findByRole('button', { name: 'Start' })
    wire.allowed.delete('sessions:run:write')
    view.rerender(view.element())
    expect(
      screen.queryByRole('button', { name: 'Start session' }),
    ).not.toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(
      screen.getByRole('link', { name: 'Open automations' }),
    ).toHaveAttribute('href', '/automations')
  })

  it('reads the list again when the New session form it opened closes', async () => {
    const user = userEvent.setup()
    wire.allowed.add('sessions:run:write')
    show()
    await screen.findByRole('button', { name: /Disconnected folder/ })
    const reads = wire.read.mock.calls.length
    await user.click(screen.getByRole('button', { name: 'Start session' }))
    await screen.findByRole('dialog')
    expect(wire.read.mock.calls.length).toBe(reads)
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await waitFor(() =>
      expect(wire.read.mock.calls.length).toBeGreaterThan(reads),
    )
  })

  it('keeps a New session form it did not open when its content is replaced', async () => {
    wire.allowed.add('sessions:run:write')
    const view = show()
    await screen.findByRole('button', { name: /Disconnected folder/ })
    // Opened from another door (the sidebar, the N key) while on Estate.
    act(() => useNewSessionDialog.getState().setOpen(true))
    await screen.findByRole('dialog')
    wire.allowed.delete('sessions:run:write')
    view.rerender(view.element())
    expect(
      screen.queryByRole('button', { name: 'Start session' }),
    ).not.toBeInTheDocument()
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })

  it('retains a disconnected resource, searches it and opens its inspector by keyboard', async () => {
    const user = userEvent.setup()
    show()
    await screen.findByRole('button', { name: /Disconnected folder/ })
    await user.type(screen.getByRole('searchbox'), 'missing')
    expect(
      screen.queryByRole('button', { name: /Disconnected folder/ }),
    ).not.toBeInTheDocument()
    await user.clear(screen.getByRole('searchbox'))
    const row = screen.getByRole('button', { name: /Disconnected folder/ })
    row.focus()
    await user.keyboard('{Enter}')
    expect(await screen.findByRole('dialog')).toHaveTextContent(
      'Disconnected folder',
    )
    expect(screen.getByRole('link', { name: 'Open resource' })).toHaveAttribute(
      'href',
      '/agentops',
    )
    await user.keyboard('{Escape}')
    await waitFor(() => expect(row).toHaveFocus())
  })

  it('performs no projection reads when sessions are disabled', async () => {
    useModulesStore.getState().setOff(['sessions'])
    show()
    expect(screen.getByRole('status')).toHaveTextContent('This module is off.')
    expect(wire.read).not.toHaveBeenCalled()
  })

  it('shows an owner refusal as unavailable and drops its stale resource and inspector', async () => {
    show()
    await userEvent.click(
      await screen.findByRole('button', { name: /Disconnected folder/ }),
    )
    await screen.findByRole('dialog')
    wire.read.mockRejectedValue(new Error('fixture owner refusal'))
    // Refresh is behind the modal: close it before the list action.
    await userEvent.keyboard('{Escape}')
    await userEvent.click(screen.getByRole('button', { name: 'Refresh' }))
    await screen.findAllByRole('alert')
    expect(screen.queryByText('Disconnected folder')).not.toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(screen.queryByText('No resources match.')).not.toBeInTheDocument()
  })

  it('discards late tenant reads and starts the next tenant with no previous selection', async () => {
    let finish!: (value: EstatePage) => void
    wire.read.mockImplementation(
      (family: EstateFamily, scope: { tenant: string }) =>
        family === 'folder' && scope.tenant === 'tenant-a'
          ? new Promise<EstatePage>((resolve) => {
              finish = resolve
            })
          : Promise.resolve(
              family === 'folder' ? page('Tenant B folder') : empty,
            ),
    )
    const view = show()
    await waitFor(() => expect(finish).toBeDefined())
    wire.tenant = 'tenant-b'
    wire.lifetime = 2
    view.rerender(view.element())
    await screen.findByRole('button', { name: /Tenant B folder/ })
    finish(page('Late tenant A folder'))
    await waitFor(() =>
      expect(
        screen.queryByText('Late tenant A folder'),
      ).not.toBeInTheDocument(),
    )
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('uses a native continuation without accumulating pages beyond the initial bound', async () => {
    wire.read.mockImplementation(
      async (family: EstateFamily, _scope: unknown, cursor?: string) =>
        family !== 'folder'
          ? empty
          : cursor
            ? page('Next folder')
            : { ...page('First folder'), hasMore: true, cursor: 'opaque-next' },
    )
    show()
    await userEvent.click(
      await screen.findByRole('button', { name: 'Next page' }),
    )
    await screen.findByRole('button', { name: /Next folder/ })
    expect(
      screen.queryByRole('button', { name: /First folder/ }),
    ).not.toBeInTheDocument()
    expect(wire.read).toHaveBeenCalledWith(
      'folder',
      expect.objectContaining({ tenant: 'tenant-a' }),
      'opaque-next',
    )
  })

  it('shows the selected workspace contents from the engine read, for admins only', async () => {
    wire.allowed.add('tenant:read')
    wire.read.mockImplementation(async (family: EstateFamily) =>
      family === 'workspace'
        ? {
            ...empty,
            nodes: [
              {
                kind: 'workspace',
                ref: 'ws-a',
                label: 'Alpha',
                status: 'active',
              },
            ],
          }
        : empty,
    )
    const contents = vi
      .spyOn(workspaceDashboardApi, 'contents')
      .mockResolvedValue({
        workspace_id: 'ws-a',
        kinds: [{ kind: 'wsc.item', count: 3, capped: false }],
      })
    const viewer = show()
    await userEvent.selectOptions(
      await screen.findByRole('combobox', { name: 'Workspace' }),
      await screen.findByRole('option', { name: 'Alpha' }),
    )
    expect(
      screen.queryByRole('region', { name: 'Workspace contents' }),
    ).not.toBeInTheDocument()
    expect(contents).not.toHaveBeenCalled()
    viewer.unmount()

    wire.allowed.add('tenant:admin')
    show()
    await userEvent.selectOptions(
      await screen.findByRole('combobox', { name: 'Workspace' }),
      await screen.findByRole('option', { name: 'Alpha' }),
    )
    const region = await screen.findByRole('region', {
      name: 'Workspace contents',
    })
    expect(
      await within(region).findByRole('row', { name: 'wsc.item 3' }),
    ).toBeInTheDocument()
    expect(contents).toHaveBeenCalledWith(
      'ws-a',
      expect.objectContaining({ tenant: 'tenant-a' }),
    )
  })
})
