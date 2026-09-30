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
  useSessionStore
    .getState()
    .setSession({
      token: 'fixture-first',
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
    await user.type(within(form).getByLabelText(/Server name/), 'New MCP')
    await user.type(
      within(form).getByLabelText(/Upstream URL/),
      'https://new.test/mcp',
    )
    await user.click(
      within(form).getByRole('button', { name: 'Save disabled' }),
    )
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
      useSessionStore
        .getState()
        .setSession({
          token: 'fixture-successor',
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
