// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { act, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createTestQueryClient, DEFAULT_AUTH, renderIntel } from '@/test/intel'
import { http } from '@/lib/api/client'
import type {
  ToolInventory,
  ProviderSnapshot,
} from '@/features/agent-tools/api'
import { useCommandStore } from '@/stores/command'
import { useModulesStore } from '@/stores/modules'
import { useSessionStore } from '@/stores/session'
import { CommandMenu } from './command-menu'
import type { RailGroup, RailRow } from './session-rail-model'
import { useObjectActionsStore } from '@/stores/object-actions'
import { AgentToolsView } from '@/features/agent-tools/agent-tools-view'
import '@/features/agent-tools/i18n'
import {
  createRootRoute,
  createRoute,
  createRouter,
  createMemoryHistory,
} from '@tanstack/react-router'

const state = vi.hoisted(() => ({
  admin: true,
  denied: false,
  restricted: false,
  tenant: 'tenant-a',
  navigate: vi.fn(),
}))
vi.mock('@tanstack/react-router', async (original) => ({
  ...(await original<typeof import('@tanstack/react-router')>()),
  useRouterState: () => '',
  useNavigate: () => state.navigate,
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    ...DEFAULT_AUTH,
    principal: { user_id: 'operator' },
    isSuperadmin: state.admin,
    activeTenant: state.tenant,
    // Tools opens on the account read tier (#2552), kept when restricted.
    can: (permission: string) =>
      permission === 'sessions:account:read'
        ? !state.denied
        : !state.restricted,
  }),
}))
const railState = vi.hoisted(() => ({
  groups: [] as RailGroup[],
}))
vi.mock('./use-session-rail', () => ({
  useSessionRail: () => ({ groups: railState.groups }),
}))

const inventory: ToolInventory = {
  drivers: ['codex', 'claude', 'grok'],
  inventory: {
    installed: [
      {
        driver: 'codex',
        version: '1.2.3',
        state: 'installed',
        executable: '/tools/codex',
      },
      {
        driver: 'grok',
        version: '1.2.3',
        state: 'failed',
        executable: '/tools/grok',
      },
    ],
    leftovers: [],
  },
  read_only: false,
  jobs: [],
}
const snapshot: ProviderSnapshot = {
  instance: 'claude',
  driver: 'claude',
  default: true,
  config_dir: '/tools/claude-home',
  installed: true,
  state: 'not_signed_in',
  limits: [],
  models: [],
  checked_at: '2026-10-10T00:00:00Z',
  source: 'claude --version',
}

beforeEach(() => {
  state.admin = true
  state.denied = false
  state.restricted = false
  state.tenant = 'tenant-a'
  state.navigate.mockReset()
  useModulesStore.setState({ off: new Set() })
  useSessionStore.setState({ credentialGeneration: 0 })
  vi.spyOn(http, 'get').mockImplementation(async (path) => {
    if (path === '/v1/search') return { results: [], truncated: false }
    if (path === '/v1/m/agenttools/inventory') return inventory
    if (path === '/v1/m/agenttools/providers') return { providers: [snapshot] }
    throw new Error(`Unexpected read: ${path}`)
  })
  railState.groups = []
  useObjectActionsStore.setState({ current: null })
})
afterEach(() => {
  vi.restoreAllMocks()
  act(() => useCommandStore.getState().setOpen(false))
  useModulesStore.setState({ off: new Set() })
})

function open() {
  useCommandStore.getState().setOpen(true)
  return renderIntel(<CommandMenu />)
}

function actualTarget() {
  const root = createRootRoute()
  const router = createRouter({
    routeTree: root.addChildren(
      ['/sessions', '/agent-tools', '/settings'].map((path) =>
        createRoute({ getParentRoute: () => root, path }),
      ),
    ),
    history: createMemoryHistory({ initialEntries: ['/sessions'] }),
  })
  return router.buildLocation(state.navigate.mock.calls[0][0])
}

it.each([
  ['Codex', 'codex'],
  ['Claude Code', 'claude'],
])(
  'finds the actual installed %s and opens its named tool',
  async (name, driver) => {
    const user = userEvent.setup()
    open()
    await user.type(screen.getByRole('combobox'), name)
    const result = await screen.findByRole('option', {
      name: `${name}AI tools`,
    })
    await user.click(result)
    expect(state.navigate).toHaveBeenCalledWith({
      to: '/agent-tools',
      search: {},
      hash: `tool-${driver}`,
    })
    expect(useCommandStore.getState().open).toBe(false)
    expect(actualTarget().pathname).toBe('/agent-tools')
    expect(actualTarget().hash).toBe(`tool-${driver}`)
    expect(http.get).toHaveBeenCalledWith(
      '/v1/m/agenttools/providers',
      expect.objectContaining({ query: { tenant_id: 'tenant-a' } }),
    )
  },
)

it('finds the offered sign-in setting and opens its section', async () => {
  const user = userEvent.setup()
  open()
  await user.type(screen.getByRole('combobox'), 'Sign-in')
  await user.click(
    await screen.findByRole('option', { name: /Sign-in and security/ }),
  )
  expect(state.navigate).toHaveBeenCalledWith({
    to: '/settings',
    search: { section: 'signIn' },
    hash: '',
  })
  expect(actualTarget().pathname).toBe('/settings')
  expect(actualTarget().search).toEqual({ section: 'signIn' })
})

it('does not invent an installed tool from the supported-driver catalog or a failed install', async () => {
  const user = userEvent.setup()
  open()
  await user.type(screen.getByRole('combobox'), 'Grok Build')
  await waitFor(() =>
    expect(http.get).toHaveBeenCalledWith('/v1/search', expect.anything()),
  )
  expect(
    screen.queryByRole('option', { name: /Grok Build/ }),
  ).not.toBeInTheDocument()
})

it.each(['reader', 'permission', 'module'])(
  'withholds installed-tool names when %s access is denied',
  async (denial) => {
    const user = userEvent.setup()
    const view = open()
    await user.type(screen.getByRole('combobox'), 'Codex')
    await screen.findByRole('option', { name: /Codex/ })
    if (denial === 'reader') state.admin = false
    if (denial === 'permission') state.denied = true
    if (denial === 'module')
      act(() => useModulesStore.setState({ off: new Set(['sessions']) }))
    view.rerender(<CommandMenu />)
    expect(
      screen.queryByRole('option', { name: /Codex/ }),
    ).not.toBeInTheDocument()
  },
)

it('withholds administrative settings for a non-superadmin and unoffered settings for everyone', async () => {
  const user = userEvent.setup()
  state.admin = false
  open()
  await user.type(screen.getByRole('combobox'), 'Report signing')
  expect(
    screen.queryByRole('option', { name: /Report signing/ }),
  ).not.toBeInTheDocument()
  await user.clear(screen.getByRole('combobox'))
  // Updates moved to its own page (MOVED_SECTIONS), so Settings never offers it.
  await user.type(screen.getByRole('combobox'), 'Updates')
  expect(
    screen.queryByRole('option', { name: /^UpdatesSettings/ }),
  ).not.toBeInTheDocument()
  expect(http.get).not.toHaveBeenCalledWith(
    '/v1/m/agenttools/inventory',
    expect.anything(),
  )
})

it('binds the palette inventory cache entry to the active tenant', async () => {
  const queryClient = createTestQueryClient()
  useCommandStore.getState().setOpen(true)
  renderIntel(<CommandMenu />, { queryClient })
  await screen.findByRole('option', { name: /Codex/ })
  const reads = queryClient.getQueryCache().findAll({
    predicate: (query) => query.state.data === inventory,
  })
  expect(reads).toHaveLength(1)
  expect(reads[0].queryKey).toContain(state.tenant)
})

it('drops the previous tenant tool result while the next tenant read is pending', async () => {
  const user = userEvent.setup()
  const view = open()
  await user.type(screen.getByRole('combobox'), 'Codex')
  await screen.findByRole('option', { name: /Codex/ })
  vi.mocked(http.get).mockImplementation(async (path) => {
    if (path === '/v1/search') return { results: [], truncated: false }
    return new Promise(() => {})
  })
  state.tenant = 'tenant-b'
  view.rerender(<CommandMenu />)
  expect(
    screen.queryByRole('option', { name: /Codex/ }),
  ).not.toBeInTheDocument()
})

it('reports an incomplete lookup when installed-tool reads fail', async () => {
  vi.mocked(http.get).mockImplementation(async (path) => {
    if (path === '/v1/search') return { results: [], truncated: false }
    throw new Error('Tool source unavailable')
  })
  open()
  expect(await screen.findByText(/incomplet/i)).toBeInTheDocument()
  expect(
    screen.queryByRole('option', { name: /Codex/ }),
  ).not.toBeInTheDocument()
})

it('retires tool reads before a tenant round trip can revive old snapshots', async () => {
  const user = userEvent.setup()
  const view = open()
  await user.type(screen.getByRole('combobox'), 'Codex')
  await screen.findByRole('option', { name: /Codex/ })
  vi.mocked(http.get).mockImplementation(async (path) => {
    if (path === '/v1/search') return { results: [], truncated: false }
    return new Promise(() => {})
  })
  state.tenant = 'tenant-b'
  view.rerender(<CommandMenu />)
  state.tenant = 'tenant-a'
  view.rerender(<CommandMenu />)
  expect(
    screen.queryByRole('option', { name: /Codex/ }),
  ).not.toBeInTheDocument()
})

it('closing the palette preserves a mounted Tools page refresh', async () => {
  const user = userEvent.setup()
  state.restricted = true
  const queryClient = createTestQueryClient()
  renderIntel(
    <>
      <AgentToolsView />
      <CommandMenu />
    </>,
    { queryClient },
  )
  await screen.findByTestId('tool-codex')
  let resolve!: (value: ToolInventory) => void
  let refreshSignal: AbortSignal | undefined
  vi.mocked(http.get).mockImplementation(async (path, options) => {
    if (path === '/v1/m/agenttools/inventory') {
      refreshSignal = options?.signal
      return new Promise<ToolInventory>((done) => {
        resolve = done
      })
    }
    if (path === '/v1/m/agenttools/providers') return { providers: [snapshot] }
    if (path === '/v1/search') return { results: [], truncated: false }
    throw new Error(`Unexpected read: ${path}`)
  })
  await user.click(screen.getByRole('button', { name: 'Refresh' }))
  await waitFor(() => expect(refreshSignal).toBeDefined())
  const toolsSignal = refreshSignal!
  const finishRefresh = resolve
  act(() => useCommandStore.getState().setOpen(true))
  await screen.findByRole('combobox')
  act(() => useCommandStore.getState().setOpen(false))
  expect(toolsSignal.aborted).toBe(false)
  await act(async () =>
    finishRefresh({
      ...inventory,
      inventory: {
        ...inventory.inventory,
        installed: [{ ...inventory.inventory.installed[0], version: '5.6.7' }],
      },
    }),
  )
  await waitFor(() =>
    expect(screen.getByTestId('tool-codex')).toHaveTextContent('5.6.7'),
  )
})

it('leaving Tools preserves an open palette lookup', async () => {
  state.restricted = true
  const view = renderIntel(
    <>
      <AgentToolsView />
      <CommandMenu />
    </>,
  )
  await screen.findByTestId('tool-codex')
  let resolve!: (value: ToolInventory) => void
  let paletteSignal: AbortSignal | undefined
  vi.mocked(http.get).mockImplementation(async (path, options) => {
    if (path === '/v1/m/agenttools/inventory') {
      paletteSignal = options?.signal
      return new Promise<ToolInventory>((done) => {
        resolve = done
      })
    }
    if (path === '/v1/m/agenttools/providers') return { providers: [snapshot] }
    if (path === '/v1/search') return { results: [], truncated: false }
    throw new Error(`Unexpected read: ${path}`)
  })
  act(() => useCommandStore.getState().setOpen(true))
  await waitFor(() => expect(paletteSignal).toBeDefined())
  const lookupSignal = paletteSignal!
  const finishLookup = resolve
  view.rerender(
    <>
      {null}
      <CommandMenu />
    </>,
  )
  expect(lookupSignal.aborted).toBe(false)
  await act(async () => finishLookup(inventory))
  expect(
    await screen.findByRole('option', { name: 'CodexAI tools' }),
  ).toBeInTheDocument()
})

describe('object actions and scopes', () => {
  const sessionRow = (ref: string): RailRow => ({
    key: `s:${ref}`,
    kind: 'session',
    state: 'live',
    status: 'working',
    title: ref,
    reference: ref,
    meta: 'Realtime',
    minutes: 4,
    to: '/sessions',
    search: { session: `run:${ref}` },
    href: `/sessions?session=run:${ref}`,
  })

  it('lists the selected object FIRST, with its actions and their shortcuts', async () => {
    const run = vi.fn()
    act(() =>
      useObjectActionsStore.getState().set({
        label: 'Marc · Codex',
        actions: [{ id: 'approve', label: 'Approve', shortcut: 'A', run }],
      }),
    )
    const user = userEvent.setup()
    open()
    expect(await screen.findByText('For Marc · Codex')).toBeInTheDocument()
    const item = screen.getByTestId('palette-object-approve')
    expect(within(item).getByText('A')).toBeInTheDocument()
    // The object group precedes the general Actions group.
    const actionsHeading = await screen.findByText('Actions')
    expect(
      item.compareDocumentPosition(actionsHeading) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()
    await user.click(item)
    expect(run).toHaveBeenCalledTimes(1)
    expect(useCommandStore.getState().open).toBe(false)
  })

  it('withdraws the object actions once a typed query owns the list', async () => {
    act(() =>
      useObjectActionsStore.getState().set({
        label: 'Marc · Codex',
        actions: [
          { id: 'approve', label: 'Approve', shortcut: 'A', run: vi.fn() },
        ],
      }),
    )
    const user = userEvent.setup()
    open()
    expect(await screen.findByText('For Marc · Codex')).toBeInTheDocument()
    await user.type(screen.getByRole('combobox'), 's')
    expect(screen.queryByText('For Marc · Codex')).not.toBeInTheDocument()
    expect(
      screen.queryByTestId('palette-object-approve'),
    ).not.toBeInTheDocument()
  })

  it('narrows the data search to people on @, with navigation out', async () => {
    const user = userEvent.setup()
    vi.mocked(http.get).mockImplementation(async (path) => {
      if (path === '/v1/search')
        return {
          results: [
            { kind: 'user', id: 'u1', name: 'Marc', detail: 'Codex' },
            {
              kind: 'workspace',
              id: 'w1',
              name: 'Marc ops',
              detail: 'Workspace',
            },
          ],
          truncated: false,
        }
      if (path === '/v1/m/agenttools/inventory') return inventory
      if (path === '/v1/m/agenttools/providers')
        return { providers: [snapshot] }
      throw new Error(`Unexpected read: ${path}`)
    })
    open()
    await user.type(screen.getByRole('combobox'), '@mar')
    expect(
      await screen.findByRole('option', { name: /^Marc/ }),
    ).toBeInTheDocument()
    expect(
      screen.queryByRole('option', { name: /Marc ops/ }),
    ).not.toBeInTheDocument()
    expect(screen.queryByText('Go to')).not.toBeInTheDocument()
    expect(screen.getByTestId('footer-people')).toHaveTextContent('@')
  })

  it('asks for a name on a bare @, before there is anything to match', async () => {
    const user = userEvent.setup()
    open()
    await user.type(screen.getByRole('combobox'), '@')
    expect(
      await screen.findByRole('option', { name: 'Type a name.' }),
    ).toBeInTheDocument()
    expect(screen.queryByText('No matches.')).not.toBeInTheDocument()
  })

  it('filters sessions on # from the rail, without the data search', async () => {
    const user = userEvent.setup()
    railState.groups = [
      {
        id: 'working',
        rows: [sessionRow('run:alpha'), sessionRow('run:beta')],
        total: 2,
        sessionTotal: 2,
      },
    ]
    open()
    await user.type(screen.getByRole('combobox'), '#alpha')
    expect(
      await screen.findByRole('option', { name: /run:alpha/ }),
    ).toBeInTheDocument()
    expect(
      screen.queryByRole('option', { name: /run:beta/ }),
    ).not.toBeInTheDocument()
    expect(http.get).not.toHaveBeenCalledWith('/v1/search', expect.anything())
    expect(screen.getByTestId('footer-sessions')).toHaveTextContent('#')
  })

  it('always names the three scopes in the footer, whatever is filtered', async () => {
    open()
    // The scope hints are a map, not a breadcrumb of where the operator is:
    // `>` commands, `@` people, `#` sessions show before any scope is chosen.
    expect(screen.getByTestId('footer-people')).toHaveTextContent('@')
    expect(screen.getByTestId('footer-sessions')).toHaveTextContent('#')
  })

  it('marks matches by weight, not underline', async () => {
    const user = userEvent.setup()
    open()
    await user.type(screen.getByRole('combobox'), 'Start a')
    const mark = await screen.findByText('Start a', { selector: 'mark' })
    expect(mark.className).toContain('font-semibold')
    expect(mark.className).not.toContain('underline')
  })
})
