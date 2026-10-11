// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactElement, ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import './i18n'

const { api, mcp, runs, authState } = vi.hoisted(() => ({
  api: {
    listSecrets: vi.fn(),
    putSecret: vi.fn(),
    deleteSecret: vi.fn(),
    listSources: vi.fn(),
  },
  mcp: { get: vi.fn() },
  runs: { listRuns: vi.fn() },
  authState: {
    activeTenant: 't1' as string | null,
    activeRole: 'owner' as string | null,
    isSuperadmin: true,
    principal: { aal: 3 } as { aal?: number } | null,
    can: (_p: string): boolean => true,
  },
}))

vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('@/features/identity/assurance', () => ({
  AAL: { PASSWORD: 1, MFA: 2, HARDWARE: 3 },
  // Passthrough gate (AAL3 enforcement is covered by the backend -race tests).
  RequireAssurance: ({ children }: { children: ReactNode }) => <>{children}</>,
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, consoleApi: api }
})
vi.mock('./mcp-gateway-api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./mcp-gateway-api')>()
  return { ...actual, mcpGatewayApi: mcp }
})
vi.mock('@/features/agentops/api', async (importOriginal) => {
  const actual =
    await importOriginal<typeof import('@/features/agentops/api')>()
  return { ...actual, agentOpsApi: runs }
})

import { ApiError } from '@/lib/api/errors'
import { SecretsTab } from './secrets-tab'

function wrap(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

const oneSecret = {
  secrets: [
    {
      name: 'gdrive/token',
      hint: 'a1b2c3',
      description: 'Drive OAuth token',
      created_at: '2026-06-01T00:00:00Z',
      updated_at: '2026-06-10T00:00:00Z',
    },
  ],
  sealer_available: true,
}

beforeEach(() => {
  vi.clearAllMocks()
  authState.isSuperadmin = true
  authState.activeTenant = 't1'
  authState.can = () => true
  api.listSecrets.mockResolvedValue({ secrets: [], sealer_available: true })
  api.listSources.mockResolvedValue({ sources: [] })
  mcp.get.mockResolvedValue({ servers: [] })
  runs.listRuns.mockResolvedValue({ items: [], has_more: false })
})

describe('SecretsTab', () => {
  it('admits tenant admins without global authority and reads the captured tenant', async () => {
    authState.isSuperadmin = false
    wrap(<SecretsTab scope="tenant" />)
    await screen.findByRole('button', { name: /new secret/i })
    expect(api.listSecrets).toHaveBeenCalledWith(
      'tenant',
      expect.objectContaining({
        tenant: 't1',
        signal: expect.any(AbortSignal),
      }),
    )
  })

  it('does not read tenant secrets for a viewer or without an active tenant', async () => {
    authState.can = () => false
    const view = wrap(<SecretsTab scope="tenant" />)
    expect(api.listSecrets).not.toHaveBeenCalled()
    view.unmount()
    authState.can = () => true
    authState.activeTenant = null
    wrap(<SecretsTab scope="tenant" />)
    expect(api.listSecrets).not.toHaveBeenCalled()
  })

  it('restricts tenant names and drops credential mutation variables when its form closes', async () => {
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    api.putSecret.mockResolvedValue({
      name: 'mcp/upstream',
      hint: 'fixture-hint',
    })
    render(
      <QueryClientProvider client={qc}>
        <SecretsTab scope="tenant" />
      </QueryClientProvider>,
    )
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: /new secret/i }))
    const form = screen.getByRole('dialog')
    const name = within(form).getByLabelText(/^name/i)
    expect(name).toHaveValue('mcp/')
    await user.clear(name)
    await user.type(name, 'provider/foreign')
    await user.type(within(form).getByLabelText(/^value/i), 'fixture-value')
    expect(
      within(form).getByRole('button', { name: /save secret/i }),
    ).toBeDisabled()
    await user.clear(name)
    await user.type(name, 'mcp/upstream')
    await user.click(within(form).getByRole('button', { name: /save secret/i }))
    await waitFor(() => expect(form).not.toBeInTheDocument())
    expect(api.putSecret).toHaveBeenCalledWith(
      expect.objectContaining({ name: 'mcp/upstream', value: 'fixture-value' }),
      'tenant',
      expect.objectContaining({
        tenant: 't1',
        dispatchGuard: expect.any(Function),
      }),
    )
    expect(qc.getMutationCache().getAll()).toHaveLength(0)
  })

  it('is superadmin-only', async () => {
    authState.isSuperadmin = false
    wrap(<SecretsTab />)
    expect(await screen.findByText(/only a superadmin/i)).toBeInTheDocument()
    expect(api.listSecrets).not.toHaveBeenCalled()
  })

  it('lists a secret masked, with its hint and a copyable reference', async () => {
    api.listSecrets.mockResolvedValue(oneSecret)
    wrap(<SecretsTab />)
    const folder = await screen.findByRole('rowgroup', { name: 'gdrive/' })
    expect(within(folder).getByText('token')).toBeInTheDocument()
    // The non-secret fingerprint is shown so an admin can tell a secret is set.
    expect(within(folder).getByText('a1b2c3')).toBeInTheDocument()
    expect(within(folder).getByText('••••••••')).toBeInTheDocument()
    expect(within(folder).getByText(/value hidden/i)).toBeInTheDocument()
    expect(within(folder).getByText('Drive OAuth token')).toBeInTheDocument()
    expect(
      within(folder).getByRole('button', { name: /store:gdrive\/token/ }),
    ).toBeInTheDocument()
  })

  it('never renders a value, even one a server would wrongly send', async () => {
    api.listSecrets.mockResolvedValue({
      secrets: [{ name: 'a/b', hint: 'h1', value: 'fixture-DO-NOT-SHOW' }],
      sealer_available: true,
    })
    wrap(<SecretsTab />)
    await screen.findByRole('rowgroup', { name: 'a/' })
    expect(document.body.textContent).not.toContain('fixture-DO-NOT-SHOW')
  })

  it('shows slash names as folders and flat names at the top level', async () => {
    api.listSecrets.mockResolvedValue({
      secrets: [
        { name: 'dev/db/password', hint: 'd1' },
        { name: 'flat-token', hint: 'f1' },
        { name: 'prod/db/password', hint: 'p1' },
      ],
      sealer_available: true,
    })
    wrap(<SecretsTab />)
    const prod = await screen.findByRole('rowgroup', { name: 'prod/db/' })
    const dev = screen.getByRole('rowgroup', { name: 'dev/db/' })
    const root = screen.getByRole('rowgroup', { name: /top level/i })
    expect(within(prod).getByText('prod/db/')).toBeInTheDocument()
    expect(within(prod).getByText('password')).toBeInTheDocument()
    expect(within(prod).getByText('p1')).toBeInTheDocument()
    expect(within(dev).getByText('password')).toBeInTheDocument()
    expect(within(dev).getByText('d1')).toBeInTheDocument()
    expect(
      within(root).getByRole('button', { name: /store:flat-token/ }),
    ).toBeInTheDocument()
    expect(
      within(prod).getByRole('button', { name: /store:prod\/db\/password/ }),
    ).toBeInTheDocument()
  })

  it('lists the connectors that reference a secret and external references read-only', async () => {
    api.listSecrets.mockResolvedValue({
      secrets: [
        { name: 'prod/db/password', hint: 'p1' },
        { name: 'unused', hint: 'u1' },
      ],
      sealer_available: true,
    })
    api.listSources.mockResolvedValue({
      sources: [
        {
          name: 'warehouse',
          tenant: 't',
          enabled: true,
          status: 'running',
          config: {
            password: 'store:prod/db/password',
            token: 'vault:kv/warehouse#token',
          },
        },
      ],
    })
    wrap(<SecretsTab />)
    const prod = await screen.findByRole('rowgroup', { name: 'prod/db/' })
    expect(await within(prod).findByText('warehouse')).toBeInTheDocument()
    const root = screen.getByRole('rowgroup', { name: /top level/i })
    expect(
      within(root).getByText(/no connector in the source roster/i),
    ).toBeInTheDocument()
    // The global store reads only its own roster.
    expect(runs.listRuns).not.toHaveBeenCalled()
    expect(mcp.get).not.toHaveBeenCalled()
    const external = screen.getByRole('region', {
      name: /external references/i,
    })
    expect(
      within(external).getByRole('button', {
        name: /vault:kv\/warehouse#token/,
      }),
    ).toBeInTheDocument()
    expect(within(external).getByText(/read-only/i)).toBeInTheDocument()
    expect(within(external).getByText('vault:')).toBeInTheDocument()
    // Read-only: nothing to rotate or delete there.
    expect(
      within(external).queryByRole('button', { name: /rotate|delete/i }),
    ).toBeNull()
  })

  it('says used-by could not be checked instead of "not referenced" when the roster fails', async () => {
    api.listSecrets.mockResolvedValue(oneSecret)
    api.listSources.mockRejectedValue(new Error('roster unavailable'))
    wrap(<SecretsTab />)
    expect(await screen.findByText(/could not be checked/i)).toBeInTheDocument()
    expect(screen.queryByText(/no connector/i)).toBeNull()
  })

  it('says the roster is not in this build when it answers 501', async () => {
    api.listSecrets.mockResolvedValue(oneSecret)
    api.listSources.mockRejectedValue(
      new ApiError(501, 'source_roster_unavailable', 'not wired'),
    )
    wrap(<SecretsTab />)
    expect(
      await screen.findByText(/not available in this build/i),
    ).toBeInTheDocument()
  })

  it('says nothing about use until the roster answers', async () => {
    api.listSecrets.mockResolvedValue(oneSecret)
    api.listSources.mockReturnValue(new Promise(() => {}))
    wrap(<SecretsTab />)
    const folder = await screen.findByRole('rowgroup', { name: 'gdrive/' })
    expect(within(folder).getByText(/checking/i)).toBeInTheDocument()
    expect(within(folder).queryByText(/no connector/i)).toBeNull()
  })

  it('lists the sessions that receive a session secret, and says when older ones were not read', async () => {
    authState.isSuperadmin = false
    api.listSecrets.mockResolvedValue({
      secrets: [{ name: 'env/api', hint: 'e1' }],
      sealer_available: true,
    })
    runs.listRuns.mockResolvedValue({
      items: [
        {
          run_ref: 'run_1',
          name: 'nightly',
          secret_env: [{ env: 'API', secret: 'env/api' }],
        },
      ],
      has_more: true,
    })
    wrap(<SecretsTab scope="tenant" namespace="env/" />)
    const env = await screen.findByRole('rowgroup', { name: 'env/' })
    expect(await within(env).findByText('nightly')).toBeInTheDocument()
    expect(runs.listRuns).toHaveBeenCalledWith(
      { limit: 500 },
      expect.objectContaining({ tenant: 't1' }),
    )
    expect(api.listSources).not.toHaveBeenCalled()
    expect(mcp.get).not.toHaveBeenCalled()
  })

  it('says an unused session secret was looked for only among the recent sessions', async () => {
    authState.isSuperadmin = false
    api.listSecrets.mockResolvedValue({
      secrets: [{ name: 'env/old', hint: 'o1' }],
      sealer_available: true,
    })
    runs.listRuns.mockResolvedValue({ items: [], has_more: true })
    const view = wrap(<SecretsTab scope="tenant" namespace="env/" />)
    expect(
      await screen.findByText(/no session among the 500 most recent/i),
    ).toBeInTheDocument()
    view.unmount()
    runs.listRuns.mockResolvedValue({ items: [], has_more: false })
    wrap(<SecretsTab scope="tenant" namespace="env/" />)
    expect(await screen.findByText(/^no session$/i)).toBeInTheDocument()
    expect(screen.queryByText(/500 most recent/i)).toBeNull()
  })

  it('lists the MCP servers that use a credential handle', async () => {
    authState.isSuperadmin = false
    api.listSecrets.mockResolvedValue({
      secrets: [{ name: 'mcp/github', hint: 'm1' }],
      sealer_available: true,
    })
    mcp.get.mockResolvedValue({
      servers: [{ name: 'github-server', credential_ref: 'store:mcp/github' }],
    })
    wrap(<SecretsTab scope="tenant" />)
    const folder = await screen.findByRole('rowgroup', { name: 'mcp/' })
    expect(await within(folder).findByText('github-server')).toBeInTheDocument()
    expect(mcp.get).toHaveBeenCalledWith(
      expect.objectContaining({ tenant: 't1' }),
    )
    expect(api.listSources).not.toHaveBeenCalled()
    expect(runs.listRuns).not.toHaveBeenCalled()
  })

  it('shows an empty state when there are no secrets', async () => {
    wrap(<SecretsTab />)
    expect(await screen.findByText(/no secrets yet/i)).toBeInTheDocument()
  })

  it('explains an empty tenant panel in its own terms, not connector configs', async () => {
    authState.isSuperadmin = false
    const view = wrap(<SecretsTab scope="tenant" namespace="env/" />)
    expect(await screen.findByText(/no secrets yet/i)).toBeInTheDocument()
    expect(screen.getByText(/give it to a session/i)).toBeInTheDocument()
    expect(screen.queryByText(/connector/i)).toBeNull()
    view.unmount()
    wrap(<SecretsTab scope="tenant" />)
    expect(await screen.findByText(/no secrets yet/i)).toBeInTheDocument()
    expect(screen.getByText(/from an MCP server/i)).toBeInTheDocument()
    expect(screen.queryByText(/connector/i)).toBeNull()
  })

  it('names the sessions that receive a session secret when deleting it, and says they cannot resume', async () => {
    authState.isSuperadmin = false
    api.listSecrets.mockResolvedValue({
      secrets: [{ name: 'env/api', hint: 'e1' }],
      sealer_available: true,
    })
    runs.listRuns.mockResolvedValue({
      items: [
        {
          run_ref: 'run_1',
          name: 'nightly',
          secret_env: [{ env: 'API', secret: 'env/api' }],
        },
      ],
      has_more: false,
    })
    api.deleteSecret.mockResolvedValue(undefined)
    const user = userEvent.setup()
    wrap(<SecretsTab scope="tenant" namespace="env/" />)
    const env = await screen.findByRole('rowgroup', { name: 'env/' })
    await within(env).findByText('nightly')
    await user.click(within(env).getByRole('button', { name: /delete/i }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('nightly')).toBeInTheDocument()
    expect(within(dialog).getByText(/cannot be resumed/i)).toBeInTheDocument()
    expect(within(dialog).queryByText(/connector/i)).toBeNull()
    // Deleting stays possible: a missing secret refuses the resume by name.
    await user.click(within(dialog).getByRole('button', { name: /delete/i }))
    await waitFor(() =>
      expect(api.deleteSecret).toHaveBeenCalledWith(
        'env/api',
        'tenant',
        expect.objectContaining({ tenant: 't1' }),
      ),
    )
  })

  it('says in the delete confirmation when the sessions could not be checked', async () => {
    authState.isSuperadmin = false
    api.listSecrets.mockResolvedValue({
      secrets: [{ name: 'env/api', hint: 'e1' }],
      sealer_available: true,
    })
    runs.listRuns.mockRejectedValue(new Error('runs unavailable'))
    const user = userEvent.setup()
    wrap(<SecretsTab scope="tenant" namespace="env/" />)
    const env = await screen.findByRole('rowgroup', { name: 'env/' })
    await within(env).findByText(/could not be checked/i)
    await user.click(within(env).getByRole('button', { name: /delete/i }))
    const dialog = await screen.findByRole('dialog')
    expect(
      within(dialog).getByRole('group', { name: /used by/i }),
    ).toHaveTextContent(/could not be checked/i)
    expect(within(dialog).queryByText(/no session/i)).toBeNull()
  })

  it('names the MCP servers that use a credential handle when deleting it', async () => {
    authState.isSuperadmin = false
    api.listSecrets.mockResolvedValue({
      secrets: [{ name: 'mcp/github', hint: 'm1' }],
      sealer_available: true,
    })
    mcp.get.mockResolvedValue({
      servers: [
        {
          id: 'srv_1',
          name: 'github-server',
          credential_ref: 'store:mcp/github',
        },
      ],
    })
    const user = userEvent.setup()
    wrap(<SecretsTab scope="tenant" />)
    const folder = await screen.findByRole('rowgroup', { name: 'mcp/' })
    await within(folder).findByText('github-server')
    await user.click(within(folder).getByRole('button', { name: /delete/i }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('github-server')).toBeInTheDocument()
    expect(within(dialog).getByText(/MCP server that/i)).toBeInTheDocument()
    expect(within(dialog).queryByText(/connector/i)).toBeNull()
  })

  it('creates a secret with a name + value (PUT)', async () => {
    api.putSecret.mockResolvedValue({
      name: 'slack/webhook',
      hint: 'ff00aa',
      description: 'Alerts',
      created_at: '',
      updated_at: '',
    })
    const user = userEvent.setup()
    wrap(<SecretsTab />)
    await user.click(await screen.findByRole('button', { name: /new secret/i }))
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByLabelText(/^name/i), 'slack/webhook')
    const value = within(dialog).getByLabelText(/^value/i)
    expect(value).toHaveAttribute('type', 'password')
    await user.type(value, 's3cr3t-value')
    await user.type(within(dialog).getByLabelText(/description/i), 'Alerts')
    await user.click(
      within(dialog).getByRole('button', { name: /save secret/i }),
    )
    await waitFor(() =>
      expect(api.putSecret).toHaveBeenCalledWith({
        name: 'slack/webhook',
        value: 's3cr3t-value',
        description: 'Alerts',
      }),
    )
  })

  it('rotates an existing secret with a blank value to keep it (PUT)', async () => {
    api.listSecrets.mockResolvedValue(oneSecret)
    api.putSecret.mockResolvedValue(oneSecret.secrets[0])
    const user = userEvent.setup()
    wrap(<SecretsTab />)
    await user.click(await screen.findByRole('button', { name: /rotate/i }))
    const dialog = await screen.findByRole('dialog')
    // The value input is blank on edit and the name is immutable (disabled).
    const valueInput = within(dialog).getByLabelText(
      /^value/i,
    ) as HTMLInputElement
    expect(valueInput.value).toBe('')
    expect(valueInput).toHaveAttribute('type', 'password')
    expect(within(dialog).getByLabelText(/^name/i)).toBeDisabled()
    // Edit the description only, leaving the value blank → value kept.
    const desc = within(dialog).getByLabelText(/description/i)
    await user.clear(desc)
    await user.type(desc, 'Rotated label')
    await user.click(within(dialog).getByRole('button', { name: /^rotate$/i }))
    await waitFor(() =>
      expect(api.putSecret).toHaveBeenCalledWith({
        name: 'gdrive/token',
        value: '',
        description: 'Rotated label',
      }),
    )
  })

  it('deletes a secret after confirmation (DELETE)', async () => {
    api.listSecrets.mockResolvedValue(oneSecret)
    api.deleteSecret.mockResolvedValue(undefined)
    const user = userEvent.setup()
    wrap(<SecretsTab />)
    await user.click(await screen.findByRole('button', { name: /delete/i }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText(/connector config/i)).toBeInTheDocument()
    expect(
      await within(dialog).findByText(/no connector in the source roster/i),
    ).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: /delete/i }))
    await waitFor(() =>
      expect(api.deleteSecret).toHaveBeenCalledWith('gdrive/token'),
    )
  })
})
