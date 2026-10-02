// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, within, waitFor, act } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { beforeEach, describe, it, expect, vi } from 'vitest'
import { useSessionStore } from '@/stores/session'
import './i18n'
const toast = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
  info: vi.fn(),
}))
vi.mock('@/components/ui/toaster', () => ({ toast, Toaster: () => null }))
const { api, auth } = vi.hoisted(() => ({
  api: {
    get: vi.fn(),
    put: vi.fn(),
    test: vi.fn(),
    remove: vi.fn(),
    session: vi.fn(),
  },
  auth: {
    activeTenant: 'tenant-one' as string | null,
    principal: { user_id: 'fixture-user', aal: 3 },
    can: vi.fn(() => true),
  },
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))
vi.mock('@/features/identity/assurance', () => ({
  AAL: { HARDWARE: 3 },
  RequireAssurance: ({ children }: { children: ReactNode }) => <>{children}</>,
}))
vi.mock('./mcp-gateway-api', async (original) => ({
  ...(await original<typeof import('./mcp-gateway-api')>()),
  mcpGatewayApi: api,
}))
vi.mock('./api', async (original) => ({
  ...(await original<typeof import('./api')>()),
  consoleApi: {
    listSecrets: vi.fn(async () => ({
      secrets: [{ name: 'mcp/fixture', hint: 'abc' }],
      sealer_available: true,
    })),
  },
}))
import { MCPGatewayTab } from './mcp-gateway-tab'
const snapshot = {
  version: 1,
  source: 'store',
  read_only: false,
  session_tools: false,
  session_endpoint: '/session/mcp',
  governance: {
    redirects: 'refused',
    deep_content_inspection: 'not_configured',
  },
  servers: [
    {
      id: 'fixture-server',
      name: 'Fixture MCP',
      transport: 'streamable_http',
      url: 'https://tools.test/mcp',
      credential_ref: 'store:mcp/fixture',
      egress_cidrs: [],
      trust: { resource: '', issuer: '' },
      allowed_tools: [],
      enabled: false,
      probe: { state: 'never_tested', tools: [] },
    },
  ],
}
function setup() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const view = render(
    <QueryClientProvider client={qc}>
      <MCPGatewayTab />
    </QueryClientProvider>,
  )
  return { ...view, qc }
}
beforeEach(() => {
  useSessionStore.getState().setSession({
    csrfToken: 'fixture-first',
    sessionId: 'fixture-session',
    expiresAt: '2099-01-01T00:00:00Z',
  })
  vi.clearAllMocks()
  auth.activeTenant = 'tenant-one'
  auth.can.mockReturnValue(true)
  api.get.mockResolvedValue(structuredClone(snapshot))
  api.test.mockResolvedValue(snapshot)
  api.put.mockResolvedValue(snapshot)
  api.session.mockResolvedValue(snapshot)
  api.remove.mockResolvedValue({ ...snapshot, servers: [] })
})
describe('MCP console governance', () => {
  it('does not read or expose actions without tenant admin authority', () => {
    auth.can.mockReturnValue(false)
    setup()
    expect(api.get).not.toHaveBeenCalled()
    expect(
      screen.queryByRole('button', { name: 'Add server' }),
    ).not.toBeInTheDocument()
  })
  it('keeps file configuration read-only and explains ownership', async () => {
    api.get.mockResolvedValue({ ...snapshot, source: 'file', read_only: true })
    setup()
    await screen.findByText('Operator file')
    for (const name of [
      'Add server',
      'Manage credentials',
      'Test connection',
      'Configure',
      'Enable',
      'Remove',
    ])
      expect(screen.getByRole('button', { name })).toBeDisabled()
    expect(screen.getByRole('switch', { name: 'Session tools' })).toBeDisabled()
  })
  it('the Session tools card says what it does and the engine’s real state, not a default (MC bae7853d)', async () => {
    const on = structuredClone(snapshot)
    on.session_tools = true
    api.get.mockResolvedValue(on)
    const view = setup()
    const card = await screen.findByRole('region', { name: 'Session tools' })
    expect(
      within(card).getByText(
        'Sessions can message each other and use their assigned work. It gives sessions no new permissions.',
      ),
    ).toBeInTheDocument()
    expect(
      within(card).getByText('On: turn it off to stop it.'),
    ).toBeInTheDocument()
    expect(within(card).queryByText(/by default/)).toBeNull()
    view.unmount()
    const off = structuredClone(snapshot)
    off.session_tools = false
    api.get.mockResolvedValue(off)
    setup()
    const again = await screen.findByRole('region', { name: 'Session tools' })
    expect(
      within(again).getByText(
        'Off: sessions cannot message each other or use their assigned work.',
      ),
    ).toBeInTheDocument()
  })
  it('shows state and every action; discovery cannot enable an untested server', async () => {
    setup()
    const row = await screen.findByRole('article', { name: 'Fixture MCP' })
    expect(within(row).getByText('Not tested')).toBeInTheDocument()
    for (const name of ['Test connection', 'Configure', 'Enable', 'Remove'])
      expect(within(row).getByRole('button', { name })).toBeInTheDocument()
    expect(within(row).getByRole('button', { name: 'Enable' })).toBeDisabled()
    await userEvent.click(
      within(row).getByRole('button', { name: 'Test connection' }),
    )
    await waitFor(() =>
      expect(api.test).toHaveBeenCalledWith(
        1,
        'fixture-server',
        expect.objectContaining({
          tenant: 'tenant-one',
          dispatchGuard: expect.any(Function),
        }),
      ),
    )
  })
  it('creates only reference input and saves disabled', async () => {
    const user = userEvent.setup()
    setup()
    await user.click(await screen.findByRole('button', { name: 'Add server' }))
    const form = await screen.findByRole('dialog')
    await user.type(within(form).getByLabelText(/^Name/), 'New MCP')
    await user.type(
      within(form).getByLabelText(/^Server address/),
      'https://new.test/mcp',
    )
    await user.click(within(form).getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith(
        1,
        expect.objectContaining({
          name: 'New MCP',
          enabled: false,
          credential_ref: '',
        }),
        undefined,
        expect.objectContaining({ tenant: 'tenant-one' }),
      ),
    )
    expect(JSON.stringify(api.put.mock.calls[0][1])).not.toContain(
      'upstream_auth',
    )
  })
  it('names an empty server after its host and keeps caller settings under Advanced', async () => {
    const user = userEvent.setup()
    setup()
    await user.click(await screen.findByRole('button', { name: 'Add server' }))
    const form = await screen.findByRole('dialog')
    expect(within(form).getByLabelText(/^Token issuer/)).not.toBeVisible()
    await user.type(
      within(form).getByLabelText(/^Server address/),
      'https://tools.example/mcp',
    )
    await user.click(within(form).getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith(
        1,
        expect.objectContaining({ name: 'tools.example' }),
        undefined,
        expect.anything(),
      ),
    )
  })
  it('adds a server that runs as a command on this server', async () => {
    const user = userEvent.setup()
    setup()
    await user.click(await screen.findByRole('button', { name: 'Add server' }))
    const form = await screen.findByRole('dialog')
    await user.click(
      within(form).getByRole('combobox', { name: /how olivares reaches it/i }),
    )
    await user.click(
      await screen.findByRole('option', { name: 'Command on this server' }),
    )
    await user.type(within(form).getByLabelText(/^Command/), 'npx')
    await user.type(
      within(form).getByLabelText(/^Arguments/),
      '-y{Enter}@modelcontextprotocol/server-everything',
    )
    await user.type(
      within(form).getByLabelText(/^Secret environment/),
      'API_TOKEN=mcp/everything',
    )
    await user.click(within(form).getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith(
        1,
        expect.objectContaining({
          name: 'server-everything',
          transport: 'stdio',
          url: '',
          command: 'npx',
          args: ['-y', '@modelcontextprotocol/server-everything'],
          env_secret_refs: { API_TOKEN: 'store:mcp/everything' },
          egress_cidrs: [],
        }),
        undefined,
        expect.anything(),
      ),
    )
    expect(api.put.mock.calls.at(-1)![1]).not.toHaveProperty('credential_ref')
  })
  it('says why a test failed and what to do next', async () => {
    const failed = structuredClone(snapshot)
    Object.assign(failed.servers[0]!.probe, {
      state: 'unreachable',
      reason: 'dns',
    })
    api.get.mockResolvedValue(failed)
    setup()
    expect(
      await screen.findByText(
        /The name tools\.test did not resolve\. Check the address for a typo/,
      ),
    ).toBeInTheDocument()
  })
  it('a local server that did not start shows the engine’s own reason (MC probe.detail)', async () => {
    const failed = structuredClone(snapshot)
    Object.assign(failed.servers[0]!, {
      transport: 'stdio',
      url: '',
      command: 'node',
      args: ['/srv/mcp/missing.js'],
    })
    Object.assign(failed.servers[0]!.probe, {
      state: 'unreachable',
      reason: 'process_exit',
      detail: "Error: Cannot find module '/srv/mcp/missing.js'",
    })
    api.get.mockResolvedValue(failed)
    setup()
    expect(
      await screen.findByText(
        "Did not start: Error: Cannot find module '/srv/mcp/missing.js'",
      ),
    ).toBeInTheDocument()
  })
  // Root 2026-10-02 (MCP tool trust): a server's own hints never let a tool run without
  // approval. Turning it on shows the tested tools, pre-selects proposed_allow, and sends the
  // explicit list the administrator confirms; every other tool asks first.
  const twoTools = () => {
    const tested = structuredClone(snapshot)
    Object.assign(tested.servers[0]!, {
      proposed_allow: ['hu_echo'],
      probe: {
        state: 'ok',
        tools: [
          { name: 'hu_echo', fingerprint: 'f1', read_only: true },
          { name: 'hu_wipe', fingerprint: 'f2' },
        ],
      },
    })
    return tested
  }
  it('turning a server on pre-selects the tools the server calls read-only, and sends the confirmed list', async () => {
    api.get.mockResolvedValue(twoTools())
    setup()
    const row = await screen.findByRole('article', { name: 'Fixture MCP' })
    await userEvent.click(within(row).getByRole('button', { name: 'Enable' }))
    const dialog = await screen.findByRole('dialog', {
      name: 'Turn on Fixture MCP',
    })
    expect(
      within(dialog).getByRole('checkbox', {
        name: 'Run hu_echo without approval',
      }),
    ).toBeChecked()
    expect(
      within(dialog).getByRole('checkbox', {
        name: 'Run hu_wipe without approval',
      }),
    ).not.toBeChecked()
    expect(api.put).not.toHaveBeenCalled()
    await userEvent.click(
      within(dialog).getByRole('button', { name: 'Turn on' }),
    )
    await waitFor(() => expect(api.put).toHaveBeenCalled())
    expect(api.put.mock.calls.at(-1)![1]).toMatchObject({
      enabled: true,
      allowed_tools: [
        { name: 'hu_echo', required_scope: 'tools:call', destructive: false },
        { name: 'hu_wipe', required_scope: 'tools:call', destructive: true },
      ],
    })
  })
  it('the administrator decides: without the proposal every tool asks first', async () => {
    api.get.mockResolvedValue(twoTools())
    setup()
    const row = await screen.findByRole('article', { name: 'Fixture MCP' })
    await userEvent.click(within(row).getByRole('button', { name: 'Enable' }))
    const dialog = await screen.findByRole('dialog', {
      name: 'Turn on Fixture MCP',
    })
    await userEvent.click(
      within(dialog).getByRole('checkbox', {
        name: 'Run hu_echo without approval',
      }),
    )
    await userEvent.click(
      within(dialog).getByRole('button', { name: 'Turn on' }),
    )
    await waitFor(() => expect(api.put).toHaveBeenCalled())
    expect(
      (
        api.put.mock.calls.at(-1)![1] as {
          allowed_tools: { destructive: boolean }[]
        }
      ).allowed_tools.every((p) => p.destructive),
    ).toBe(true)
  })
  it('a tested server with no external trust can be enabled; half-set trust cannot (engine rule)', async () => {
    const tested = structuredClone(snapshot)
    Object.assign(tested.servers[0]!, {
      probe: { state: 'ok', tools: [{ name: 'hu_echo', fingerprint: 'f1' }] },
    })
    api.get.mockResolvedValue(tested)
    const view = setup()
    const row = await screen.findByRole('article', { name: 'Fixture MCP' })
    expect(within(row).getByRole('button', { name: 'Enable' })).toBeEnabled()
    view.unmount()
    const half = structuredClone(tested)
    Object.assign(half.servers[0]!, {
      trust: { resource: 'https://tools.test', issuer: '' },
    })
    api.get.mockResolvedValue(half)
    setup()
    const again = await screen.findByRole('article', { name: 'Fixture MCP' })
    expect(within(again).getByRole('button', { name: 'Enable' })).toBeDisabled()
  })
  it('a tool list someone set is sent as set on enable', async () => {
    const tested = structuredClone(snapshot)
    const policy = {
      name: 'hu_echo',
      required_scope: 'tools:call',
      destructive: true,
    }
    Object.assign(tested.servers[0]!, {
      allowed_tools: [policy],
      probe: { state: 'ok', tools: [{ name: 'hu_echo', fingerprint: 'f1' }] },
    })
    api.get.mockResolvedValue(tested)
    setup()
    const row = await screen.findByRole('article', { name: 'Fixture MCP' })
    await userEvent.click(within(row).getByRole('button', { name: 'Enable' }))
    // The dialog opens on the list as set; confirming keeps it.
    const dialog = await screen.findByRole('dialog', {
      name: 'Turn on Fixture MCP',
    })
    await userEvent.click(
      within(dialog).getByRole('button', { name: 'Turn on' }),
    )
    await waitFor(() => expect(api.put).toHaveBeenCalled())
    expect(api.put.mock.calls.at(-1)![1]).toMatchObject({
      enabled: true,
      allowed_tools: [policy],
    })
  })
  it('says which tools run without approval, ask first, or are not allowed (CLX copy 19)', async () => {
    const live = structuredClone(snapshot)
    Object.assign(live.servers[0]!, {
      enabled: true,
      allowed_tools: [
        { name: 'hu_echo', required_scope: 'tools:call', destructive: true },
      ],
      probe: {
        state: 'ok',
        tools: [
          { name: 'hu_echo', fingerprint: 'f1' },
          { name: 'hu_wipe', fingerprint: 'f2' },
        ],
      },
    })
    api.get.mockResolvedValue(live)
    setup()
    const row = await screen.findByRole('article', { name: 'Fixture MCP' })
    expect(within(row).getByText('Ask first: hu_echo')).toBeInTheDocument()
    expect(within(row).getByText('Not allowed: hu_wipe')).toBeInTheDocument()
    expect(within(row).queryByText(/^Run without approval/)).toBeNull()
    expect(within(row).getByText('Asks for approval first')).toBeInTheDocument()
    expect(within(row).getByText('Not allowed')).toBeInTheDocument()
  })
  it('Allow grants tools:call, and a Save the engine did not keep stays open and says why (HU 026)', async () => {
    const tested = structuredClone(snapshot)
    Object.assign(tested.servers[0]!, {
      // Complete trust: jsdom's http origin would otherwise prefill an invalid resource.
      trust: {
        resource: 'https://tools.test/resource',
        issuer: 'https://issuer.test',
        jwks_url: 'https://issuer.test/jwks',
      },
      probe: {
        state: 'ok',
        tools: [{ name: 'hu_echo', fingerprint: 'f1', read_only: true }],
      },
    })
    api.get.mockResolvedValue(tested)
    // The answer comes back without the permission that was sent.
    api.put.mockResolvedValue(tested)
    const user = userEvent.setup()
    setup()
    const row = await screen.findByRole('article', { name: 'Fixture MCP' })
    await user.click(within(row).getByRole('button', { name: 'Configure' }))
    const dialog = await screen.findByRole('dialog')
    await user.click(
      within(dialog).getByRole('switch', { name: 'Allow hu_echo' }),
    )
    expect(
      within(dialog).getByLabelText(/Required scope for hu_echo/),
    ).toHaveValue('tools:call')
    await user.click(within(dialog).getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(api.put).toHaveBeenCalled())
    expect(api.put.mock.calls.at(-1)![1]).toMatchObject({
      allowed_tools: [
        { name: 'hu_echo', required_scope: 'tools:call', destructive: false },
      ],
    })
    expect(
      await within(dialog).findByText(
        /Not saved: the engine did not keep the permission for hu_echo/,
      ),
    ).toBeInTheDocument()
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })
  it('an unchanged Save keeps an enabled server on and writes no trust (HU-R19)', async () => {
    const live = structuredClone(snapshot)
    Object.assign(live.servers[0]!, {
      enabled: true,
      allowed_tools: [
        { name: 'hu_echo', required_scope: 'tools:call', destructive: true },
      ],
      probe: { state: 'ok', tools: [{ name: 'hu_echo', fingerprint: 'f1' }] },
    })
    api.get.mockResolvedValue(live)
    api.put.mockResolvedValue(live)
    const user = userEvent.setup()
    setup()
    const row = await screen.findByRole('article', { name: 'Fixture MCP' })
    await user.click(within(row).getByRole('button', { name: 'Configure' }))
    const dialog = await screen.findByRole('dialog')
    // The form says what a Save does to a server that exists (HU 034).
    expect(
      within(dialog).getByText(/^Saving keeps the server as it is\./),
    ).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(api.put).toHaveBeenCalled())
    const sent = api.put.mock.calls.at(-1)![1]
    expect(sent).toMatchObject({
      enabled: true,
      allowed_tools: [
        { name: 'hu_echo', required_scope: 'tools:call', destructive: true },
      ],
    })
    // Stored empty trust goes back as {}: no generated resource (MC contract).
    expect(sent.trust).toEqual({})
  })
  it('a Save that moves the server turns it off until it is tested again', async () => {
    const live = structuredClone(snapshot)
    Object.assign(live.servers[0]!, {
      enabled: true,
      probe: { state: 'ok', tools: [{ name: 'hu_echo', fingerprint: 'f1' }] },
    })
    api.get.mockResolvedValue(live)
    api.put.mockResolvedValue(live)
    const user = userEvent.setup()
    setup()
    const row = await screen.findByRole('article', { name: 'Fixture MCP' })
    await user.click(within(row).getByRole('button', { name: 'Configure' }))
    const dialog = await screen.findByRole('dialog')
    const address = within(dialog).getByDisplayValue('https://tools.test/mcp')
    await user.clear(address)
    await user.type(address, 'https://tools.test/v2/mcp')
    await user.click(within(dialog).getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(api.put).toHaveBeenCalled())
    expect(api.put.mock.calls.at(-1)![1]).toMatchObject({ enabled: false })
    await waitFor(() =>
      expect(toast.success).toHaveBeenCalledWith(
        'Saved. Test the server again to turn it back on.',
        undefined,
      ),
    )
  })
  it('half-set trust cannot be saved, and says why', async () => {
    const user = userEvent.setup()
    setup()
    const row = await screen.findByRole('article', { name: 'Fixture MCP' })
    await user.click(within(row).getByRole('button', { name: 'Configure' }))
    const dialog = await screen.findByRole('dialog')
    await user.type(
      within(dialog).getByLabelText(/Token issuer/),
      'https://issuer.test',
    )
    expect(within(dialog).getByRole('button', { name: 'Save' })).toBeDisabled()
    expect(
      within(dialog).getByText(/needs the resource, the issuer and the keys/),
    ).toBeInTheDocument()
  })
  it('says every applied-governance fact in words, never as a raw key (HU-R21)', async () => {
    api.get.mockResolvedValue({
      ...structuredClone(snapshot),
      governance: {
        local_execution: 'session_runner_engine_user_session_folder',
        process_confinement: 'reported_by_session_runner',
        network_confinement: 'not_supplied_for_local_commands',
        some_future_fact: 'a_new_value',
      },
    })
    setup()
    const section = await screen.findByRole('region', {
      name: 'Applied governance',
    })
    expect(section.textContent).not.toMatch(
      /mcpGateway\.|governance(Keys|Values)/,
    )
    expect(section).toHaveTextContent('Process confinement')
    expect(section).toHaveTextContent('Some future fact · A new value')
  })
  it('retires an open form and cached inventory on tenant change', async () => {
    const user = userEvent.setup()
    const view = setup()
    await user.click(await screen.findByRole('button', { name: 'Configure' }))
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    auth.activeTenant = 'tenant-two'
    api.get.mockResolvedValue({ ...snapshot, servers: [] })
    view.rerender(
      <QueryClientProvider client={view.qc}>
        <MCPGatewayTab />
      </QueryClientProvider>,
    )
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    await screen.findByText('No upstream servers')
    expect(
      screen.queryByRole('article', { name: 'Fixture MCP' }),
    ).not.toBeInTheDocument()
    expect(api.put).not.toHaveBeenCalled()
  })
  it('retires a form when the credential generation changes', async () => {
    const user = userEvent.setup()
    setup()
    await user.click(await screen.findByRole('button', { name: 'Configure' }))
    act(() =>
      useSessionStore.getState().setSession({
        csrfToken: 'fixture-successor',
        sessionId: 'fixture-session',
        expiresAt: '2099-01-01T00:00:00Z',
      }),
    )
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument(),
    )
    expect(api.put).not.toHaveBeenCalled()
  })
})
