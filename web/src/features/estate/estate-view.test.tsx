// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'
import { useModulesStore } from '@/stores/modules'
import type { EstateFamily, EstatePage } from './types'
import type { FeatureView } from '@/features/registry'
import i18n from 'i18next'

const wire = vi.hoisted(() => ({
  views: undefined as FeatureView[] | undefined,
  read: vi.fn(),
  tenant: 'tenant-a',
  lifetime: 1,
  allowed: new Set<string>(),
  start: vi.fn(),
  navigate: vi.fn(),
}))
vi.mock('@/features/registry', async (original) => {
  const registry = await original<typeof import('@/features/registry')>()
  return {
    ...registry,
    get FEATURE_VIEWS() {
      return wire.views ?? registry.FEATURE_VIEWS
    },
  }
})
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
  useRouterState: (await import('@/test/fake-router')).fakeRouterModule()
    .useRouterState,
  Link: (await import('@/test/fake-router')).fakeRouterModule().Link,
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
function show(withLaunchHost = true) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const element = () => (
    <QueryClientProvider client={client}>
      <EstateView />
      {withLaunchHost && <NewSessionHost />}
    </QueryClientProvider>
  )
  return { ...render(element()), element }
}

beforeEach(() => {
  useNewSessionDialog.setState({ open: false, opener: null, advanced: null })
  wire.views = undefined
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
  it('opens the installed Organization directly and withdraws the link without permission or the view', async () => {
    i18n.addResource('en', 'nav', 'items.organization', 'Organization')
    wire.views = [
      {
        id: 'organization',
        path: '/organization',
        permission: 'tenant:read',
      } as FeatureView,
    ]
    wire.allowed.add('tenant:read')
    const view = show(false)
    const link = await screen.findByRole('link', { name: 'Organization' })
    expect(link).toHaveAttribute('href', '/organization')
    await userEvent.click(link)
    wire.allowed.delete('tenant:read')
    view.rerender(view.element())
    expect(
      screen.queryByRole('link', { name: 'Organization' }),
    ).not.toBeInTheDocument()
    wire.allowed.add('tenant:read')
    wire.views = []
    view.rerender(view.element())
    expect(
      screen.queryByRole('link', { name: 'Organization' }),
    ).not.toBeInTheDocument()
  })

  it('keeps native session launch in the page header and withdraws it when permission is revoked', async () => {
    wire.allowed.add('sessions:run:write')
    wire.allowed.add('orchestration:schedule:read')
    const view = show()
    await screen.findByRole('button', { name: /Disconnected folder/ })
    const header = screen
      .getByRole('heading', { name: 'Configured resources', level: 1 })
      .closest('[data-slot="page-header"]')
    const start = screen.getByRole('button', { name: 'Start session' })
    expect(header).toContainElement(start)
    // Automations are a Business page: Community offers no way into it.
    expect(
      screen.queryByRole('link', { name: 'Open automations' }),
    ).not.toBeInTheDocument()
    await userEvent.click(start)
    expect(await screen.findByRole('dialog')).not.toHaveTextContent(
      'Every other launch choice is under Advanced options.',
    )
    await userEvent.click(
      await screen.findByRole('combobox', { name: 'Folder' }),
    )
    await userEvent.click(
      await screen.findByRole('option', {
        name: 'A new folder for this session',
      }),
    )
    await userEvent.click(
      await screen.findByRole('button', { name: 'Start session' }),
    )
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
    await screen.findByRole('button', { name: 'Start session' })
    wire.allowed.delete('sessions:run:write')
    view.rerender(view.element())
    expect(
      screen.queryByRole('button', { name: 'Start session' }),
    ).not.toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
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
    // The page's own door is withdrawn: no Start session outside the dialog, looked for
    // behind the modal too, since the dialog's primary bears the same words.
    const dialog = screen.getByRole('dialog')
    expect(
      screen
        .queryAllByRole('button', { name: 'Start session', hidden: true })
        .filter((button) => !dialog.contains(button)),
    ).toHaveLength(0)
    expect(dialog).toBeInTheDocument()
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
    expect(screen.getByRole('link', { name: 'Open Folder' })).toHaveAttribute(
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

  it('draws its filters with the console select, and a truncated list says how many it shows and which resource Open goes to', async () => {
    wire.read.mockImplementation(async (family: EstateFamily) =>
      family === 'folder'
        ? { ...page('Disconnected folder'), hasMore: true, cursor: 'next' }
        : empty,
    )
    show()
    for (const name of ['Workspace', 'Resource type'])
      expect(await screen.findByRole('combobox', { name })).toHaveAttribute(
        'data-slot',
        'select-trigger',
      )
    const row = (
      await screen.findByRole('button', { name: 'Next page' })
    ).closest('[role="status"]') as HTMLElement
    expect(row).toHaveTextContent('Showing 1. More are available.')
    expect(row).not.toHaveTextContent(/native/i)
    expect(within(row).getByRole('link', { name: 'Open Folder' })).toBeVisible()
  })

  it('groups resources per kind, each row with its helper line and state as dot and word', async () => {
    wire.read.mockImplementation(async (family: EstateFamily) =>
      family === 'folder'
        ? {
            ...empty,
            nodes: [
              { kind: 'folder', ref: 'f1', label: 'Repo', status: 'active' },
            ],
          }
        : family === 'session'
          ? {
              ...empty,
              nodes: [
                {
                  kind: 'session',
                  ref: 'r1',
                  label: 'Docs run',
                  status: 'running',
                  folderRef: 'f1',
                },
              ],
            }
          : empty,
    )
    show()
    const folders = await screen.findByRole('region', { name: 'Folders' })
    expect(within(folders).getByText('1')).toBeInTheDocument()
    const sessions = screen.getByRole('region', { name: 'Sessions' })
    const row = within(sessions).getByRole('listitem')
    // The name opens the resource, 44 px high on a phone, inside the row's own padding.
    const name = within(row).getByRole('button', { name: 'Docs run' })
    expect(name).toHaveClass('max-md:min-h-11')
    // The helper names the folder, and the state is dot plus word.
    expect(row).toHaveTextContent('Repo')
    expect(row).toHaveTextContent('Working')
    expect(row.querySelector('[data-slot="status-dot"]')).not.toBeNull()
  })

  it('names an empty estate once instead of an empty list per kind', async () => {
    wire.read.mockResolvedValue(empty)
    show()
    expect(
      await screen.findByText('No resources configured yet'),
    ).toBeInTheDocument()
    expect(screen.queryByRole('region')).toBeNull()
  })

  it('says how many rows the filters hide and clears them', async () => {
    const user = userEvent.setup()
    show()
    await screen.findByRole('button', { name: /Disconnected folder/ })
    await user.type(screen.getByRole('searchbox'), 'missing')
    expect(await screen.findByText('No resources match.')).toBeInTheDocument()
    expect(
      screen.getByText('1 item is hidden by the filters.'),
    ).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Clear filters' }))
    expect(
      await screen.findByRole('button', { name: /Disconnected folder/ }),
    ).toBeInTheDocument()
  })

  it('offers Retry when no family can be read and reads them again', async () => {
    const user = userEvent.setup()
    wire.read.mockRejectedValue(new Error('fixture failure'))
    show()
    const alert = await screen.findByRole('alert')
    const reads = wire.read.mock.calls.length
    wire.read.mockImplementation(async (family: EstateFamily) =>
      family === 'folder' ? page('Back again') : empty,
    )
    await user.click(within(alert).getByRole('button', { name: 'Retry' }))
    expect(
      await screen.findByRole('button', { name: /Back again/ }),
    ).toBeInTheDocument()
    expect(wire.read.mock.calls.length).toBeGreaterThan(reads)
  })

  it('shows a refused read as no access, never as a failure', async () => {
    wire.read.mockRejectedValue(new ApiError(403, 'forbidden', 'denied'))
    show()
    expect(
      await screen.findByText('You do not have access to this.'),
    ).toBeVisible()
    expect(screen.queryByRole('alert')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull()
  })

  it('counts nothing beside a family that could not be read', async () => {
    wire.read.mockImplementation(async (family: EstateFamily) => {
      if (family === 'session') throw new Error('fixture failure')
      return family === 'folder' ? page('Repo') : empty
    })
    show()
    const group = await screen.findByRole('region', { name: 'Sessions' })
    expect(within(group).getByRole('alert')).toBeVisible()
    expect(
      within(group).getByText('Could not load this resource family.'),
    ).toBeVisible()
    expect(within(group).queryByText('0')).toBeNull()
  })

  it('says a family refused by role is refused, with no Retry in it', async () => {
    wire.read.mockImplementation(async (family: EstateFamily) => {
      if (family === 'session') throw new ApiError(403, 'forbidden', 'denied')
      return family === 'folder' ? page('Repo') : empty
    })
    show()
    const group = await screen.findByRole('region', { name: 'Sessions' })
    expect(
      within(group).getByText('You do not have access to this.'),
    ).toBeVisible()
    expect(within(group).queryByRole('button', { name: 'Retry' })).toBeNull()
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
    await userEvent.click(
      await screen.findByRole('combobox', { name: 'Workspace' }),
    )
    await userEvent.click(await screen.findByRole('option', { name: 'Alpha' }))
    expect(
      screen.queryByRole('region', { name: 'Workspace contents' }),
    ).not.toBeInTheDocument()
    expect(contents).not.toHaveBeenCalled()
    viewer.unmount()

    wire.allowed.add('tenant:admin')
    show()
    await userEvent.click(
      await screen.findByRole('combobox', { name: 'Workspace' }),
    )
    await userEvent.click(await screen.findByRole('option', { name: 'Alpha' }))
    const region = await screen.findByRole('region', {
      name: 'Workspace contents',
    })
    expect(
      await within(region).findByRole('row', { name: 'Wsc item 3' }),
    ).toBeInTheDocument()
    expect(contents).toHaveBeenCalledWith(
      'ws-a',
      expect.objectContaining({ tenant: 'tenant-a' }),
    )
  })
})
