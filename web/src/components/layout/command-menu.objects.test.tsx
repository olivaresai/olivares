// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { act, screen, waitFor } from '@testing-library/react'
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
    can: (permission: string) =>
      permission === 'system:admin' ? !state.denied : !state.restricted,
  }),
}))
vi.mock('./use-session-rail', () => ({
  useSessionRail: () => ({ groups: [] }),
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
      act(() => useModulesStore.setState({ off: new Set(['system']) }))
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
  await user.type(screen.getByRole('combobox'), 'Notifications')
  expect(
    screen.queryByRole('option', { name: /^Notifications/ }),
  ).not.toBeInTheDocument()
  expect(http.get).not.toHaveBeenCalledWith(
    '/v1/m/agenttools/inventory',
    expect.anything(),
  )
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
