// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactElement, ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import './i18n'

const { api, authState, toastMock } = vi.hoisted(() => ({
  api: {
    listConnectors: vi.fn(),
    listSources: vi.fn(),
    putConnector: vi.fn(),
    testConnector: vi.fn(),
    deleteConnector: vi.fn(),
    reloadRuntime: vi.fn(),
  },
  authState: {
    activeTenant: 't1' as string | null,
    activeRole: 'owner' as string | null,
    isSuperadmin: true,
    principal: { aal: 3 } as { aal?: number } | null,
    can: (_p: string) => true,
  },
  // Spy the toast surface so we can assert the reload success message is QUALIFIED
  // (restart caveat / partial), never a plain "reloaded".
  toastMock: {
    success: vi.fn(),
    error: vi.fn(),
    warning: vi.fn(),
    info: vi.fn(),
  },
}))

vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('@/features/identity/assurance', () => ({
  AAL: { PASSWORD: 1, MFA: 2, HARDWARE: 3 },
  RequireAssurance: ({ children }: { children: ReactNode }) => <>{children}</>,
}))
vi.mock('@/components/ui/toaster', () => ({
  toast: toastMock,
  Toaster: () => null,
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, consoleApi: api }
})

import { ConnectorsTab } from './connectors-tab'

function wrap(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

const catalog = {
  connectors: [
    {
      kind: 'vault',
      title: 'Vault',
      description: 'HashiCorp Vault audit',
      transport: 'in_process',
      fields_known: true,
      fields: [
        { key: 'base_url', type: 'string', required: true, secret: false },
        { key: 'token', type: 'string', required: true, secret: true },
      ],
    },
  ],
}

beforeEach(() => {
  vi.clearAllMocks()
  authState.isSuperadmin = true
  api.listConnectors.mockResolvedValue(catalog)
  api.listSources.mockResolvedValue({ sources: [] })
})

describe('ConnectorsTab', () => {
  it('is superadmin-only', async () => {
    authState.isSuperadmin = false
    wrap(<ConnectorsTab />)
    expect(await screen.findByText(/only a superadmin/i)).toBeInTheDocument()
    expect(api.listConnectors).not.toHaveBeenCalled()
  })

  it('lists configured connectors with source-mode badges and filters them', async () => {
    api.listSources.mockResolvedValue({
      sources: [
        {
          name: 'vault-prod',
          kind: 'vault',
          tenant: 'acme',
          enabled: true,
          status: 'running',
          config: {},
        },
        {
          name: 'wiki-live',
          kind: 'vault',
          tenant: 'acme',
          enabled: true,
          status: 'running',
          source_mode: 'live',
          config: { mode: 'live' },
        },
      ],
    })
    const user = userEvent.setup()
    wrap(<ConnectorsTab />)
    expect(await screen.findByText('vault-prod')).toBeInTheDocument()
    expect(screen.getByText('wiki-live')).toBeInTheDocument()
    expect(screen.getByText('Export')).toBeInTheDocument()
    expect(screen.getByText('Live')).toBeInTheDocument()
    expect(screen.getAllByText('Running')).toHaveLength(2)
    await user.click(screen.getByRole('combobox', { name: /mode filter/i }))
    await user.click(screen.getByRole('option', { name: 'Live' }))
    expect(screen.queryByText('vault-prod')).toBeNull()
    expect(screen.getByText('wiki-live')).toBeInTheDocument()
  })

  it('shows an empty state when there are no connectors', async () => {
    wrap(<ConnectorsTab />)
    expect(await screen.findByText(/no connectors yet/i)).toBeInTheDocument()
  })

  it('adds a connector with an inline credential (PUT seals it into secrets)', async () => {
    api.putConnector.mockResolvedValue({
      name: 'vault-prod',
      action: 'added',
      persisted: true,
      applied: true,
    })
    const user = userEvent.setup()
    wrap(<ConnectorsTab />)
    await user.click(
      await screen.findByRole('button', { name: /add connector/i }),
    )
    const dialog = await screen.findByRole('dialog')

    await user.type(within(dialog).getByLabelText(/^name/i), 'vault-prod')
    await user.type(within(dialog).getByLabelText(/^tenant/i), 'acme')
    // Descriptor-driven fields: a non-secret text field and a masked secret field.
    await user.type(
      within(dialog).getByLabelText(/^base_url/),
      'https://v:8200',
    )
    const token = within(dialog).getByLabelText(/^token/) as HTMLInputElement
    expect(token).toHaveAttribute('type', 'password')
    await user.type(token, 'hvs.SECRET')

    await user.click(
      within(dialog).getByRole('button', { name: /save connector/i }),
    )
    await waitFor(() =>
      expect(api.putConnector).toHaveBeenCalledWith({
        name: 'vault-prod',
        kind: 'vault',
        tenant: 'acme',
        enabled: true,
        poll_seconds: 0,
        config: { base_url: 'https://v:8200' },
        secrets: { token: 'hvs.SECRET' },
      }),
    )
  })

  it('tests the connection before saving (POST /test)', async () => {
    api.testConnector.mockResolvedValue({ ok: true })
    const user = userEvent.setup()
    wrap(<ConnectorsTab />)
    await user.click(
      await screen.findByRole('button', { name: /add connector/i }),
    )
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByLabelText(/^name/i), 'vault-prod')
    await user.type(within(dialog).getByLabelText(/^tenant/i), 'acme')
    await user.type(
      within(dialog).getByLabelText(/^base_url/),
      'https://v:8200',
    )
    await user.type(within(dialog).getByLabelText(/^token/), 'hvs.SECRET')
    await user.click(
      within(dialog).getByRole('button', { name: /test connection/i }),
    )
    await waitFor(() => expect(api.testConnector).toHaveBeenCalledTimes(1))
  })

  it('keeps a stored credential when the secret field is left blank on edit', async () => {
    api.putConnector.mockResolvedValue({
      name: 'vault-prod',
      action: 'rotated',
      persisted: true,
      applied: true,
    })
    api.listSources.mockResolvedValue({
      sources: [
        {
          name: 'vault-prod',
          kind: 'vault',
          tenant: 'acme',
          enabled: true,
          status: 'running',
          config: {
            base_url: 'https://v:8200',
            token: 'store:source/vault-prod/token',
          },
        },
      ],
    })
    const user = userEvent.setup()
    wrap(<ConnectorsTab />)
    await user.click(await screen.findByRole('button', { name: /edit/i }))
    const dialog = await screen.findByRole('dialog')
    // The token field is blank on edit (never prefilled with the stored value).
    const token = within(dialog).getByLabelText(/^token/) as HTMLInputElement
    expect(token.value).toBe('')
    await user.click(
      within(dialog).getByRole('button', { name: /save connector/i }),
    )
    await waitFor(() =>
      // A blank secret field is sent as "" — the engine keeps the stored sealed value.
      expect(api.putConnector).toHaveBeenCalledWith(
        expect.objectContaining({ name: 'vault-prod', secrets: { token: '' } }),
      ),
    )
  })

  it('deletes a connector after confirmation (DELETE)', async () => {
    api.deleteConnector.mockResolvedValue({
      name: 'vault-prod',
      action: 'removed',
      persisted: true,
      applied: true,
    })
    api.listSources.mockResolvedValue({
      sources: [
        {
          name: 'vault-prod',
          kind: 'vault',
          tenant: 'acme',
          enabled: true,
          status: 'running',
          config: {},
        },
      ],
    })
    const user = userEvent.setup()
    wrap(<ConnectorsTab />)
    await user.click(await screen.findByRole('button', { name: /delete/i }))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: /delete/i }))
    await waitFor(() =>
      expect(api.deleteConnector).toHaveBeenCalledWith('vault-prod'),
    )
  })

  // --- Reload runtime / reconcile source roster --------------------------------

  // Open the reload confirm dialog and confirm it, firing the reconcile.
  async function reload(
    user: ReturnType<typeof userEvent.setup>,
    report: Record<string, unknown>,
  ) {
    api.reloadRuntime.mockResolvedValue(report)
    await user.click(
      await screen.findByRole('button', { name: /reload runtime/i }),
    )
    const confirm = await screen.findByRole('dialog')
    await user.click(
      within(confirm).getByRole('button', { name: /reload runtime/i }),
    )
  }

  it('reloads the runtime and invalidates the sources AND connectors caches', async () => {
    // PINS: reload POSTs /v1/console/runtime/reload and invalidates BOTH
    // consoleKeys.sources() and consoleKeys.connectors() so both views refresh.
    const invalidateSpy = vi.spyOn(QueryClient.prototype, 'invalidateQueries')
    const user = userEvent.setup()
    wrap(<ConnectorsTab />)
    await reload(user, {
      unchanged: 2,
      requires_restart: ['HTTP/gRPC listeners and TLS'],
    })
    await waitFor(() => expect(api.reloadRuntime).toHaveBeenCalledTimes(1))
    expect(invalidateSpy).toHaveBeenCalledWith({
      queryKey: ['console', 'sources'],
    })
    expect(invalidateSpy).toHaveBeenCalledWith({
      queryKey: ['console', 'connectors'],
    })
    invalidateSpy.mockRestore()
  })

  it('always surfaces requires_restart as a warning and never a plain success', async () => {
    // PINS honesty (c)+(e): requires_restart is rendered EVERY time (even on an
    // otherwise-clean reconcile), and the only success signal is QUALIFIED with the
    // restart caveat — never an unconditional "reloaded".
    const user = userEvent.setup()
    wrap(<ConnectorsTab />)
    await reload(user, {
      unchanged: 3,
      requires_restart: [
        'HTTP/gRPC listeners and TLS',
        'database DSN, the event bus, and the sealer key',
      ],
    })
    expect(
      await screen.findByText(/a restart is still required/i),
    ).toBeInTheDocument()
    expect(screen.getByText('HTTP/gRPC listeners and TLS')).toBeInTheDocument()
    // No plain success: the success toast carries the restart qualifier.
    await waitFor(() => expect(toastMock.success).toHaveBeenCalledTimes(1))
    expect(toastMock.success).toHaveBeenCalledWith(
      expect.stringMatching(/restart/i),
      undefined,
    )
  })

  it('renders rejected sources per name + reason and suppresses a plain success', async () => {
    // PINS honesty (d): rejected[] is shown per name + reason and the outcome is a
    // qualified PARTIAL — the success toast says "in part", never a clean success.
    const user = userEvent.setup()
    wrap(<ConnectorsTab />)
    await reload(user, {
      removed: ['old-wiki'],
      unchanged: 1,
      rejected: [
        { name: 'vault-prod', reason: 'seal failed: connection refused' },
        { name: 'gh-live', reason: 'unknown kind' },
      ],
      requires_restart: ['HTTP/gRPC listeners and TLS'],
    })
    expect(await screen.findByText('vault-prod')).toBeInTheDocument()
    expect(
      screen.getByText(/seal failed: connection refused/),
    ).toBeInTheDocument()
    expect(screen.getByText('gh-live')).toBeInTheDocument()
    expect(screen.getByText(/unknown kind/)).toBeInTheDocument()
    // Suppress plain success: exactly one, qualified as partial.
    await waitFor(() => expect(toastMock.success).toHaveBeenCalledTimes(1))
    expect(toastMock.success).toHaveBeenCalledWith(
      expect.stringMatching(/in part/i),
      undefined,
    )
  })

  it('never overclaims: the reload copy never says PDP, policy or governance', async () => {
    // PINS honesty (b): this reload covers the source roster + license only; it does
    // NOT reload the access engine. Regression-guard the button, the confirm dialog
    // and the report against policy/PDP/governance wording.
    const forbidden = /\b(pdp|policy|policies|governance)\b/i
    const user = userEvent.setup()
    wrap(<ConnectorsTab />)
    const reloadBtn = await screen.findByRole('button', {
      name: /reload runtime/i,
    })
    expect(reloadBtn.textContent ?? '').not.toMatch(forbidden)
    api.reloadRuntime.mockResolvedValue({
      unchanged: 1,
      requires_restart: ['HTTP/gRPC listeners and TLS'],
    })
    await user.click(reloadBtn)
    const confirm = await screen.findByRole('dialog')
    expect(confirm.textContent ?? '').not.toMatch(forbidden)
    await user.click(
      within(confirm).getByRole('button', { name: /reload runtime/i }),
    )
    expect(
      await screen.findByText(/runtime reload report/i),
    ).toBeInTheDocument()
    expect(screen.getByRole('dialog').textContent ?? '').not.toMatch(forbidden)
  })

  it('confirms live sources are torn down BEFORE the reload fires', async () => {
    // PINS honesty (a): the disruptive tear-down warning is shown and the POST does
    // NOT fire until the operator confirms.
    api.reloadRuntime.mockResolvedValue({
      unchanged: 1,
      requires_restart: ['HTTP/gRPC listeners and TLS'],
    })
    const user = userEvent.setup()
    wrap(<ConnectorsTab />)
    await user.click(
      await screen.findByRole('button', { name: /reload runtime/i }),
    )
    const confirm = await screen.findByRole('dialog')
    expect(within(confirm).getByText(/torn down live/i)).toBeInTheDocument()
    // Nothing POSTed yet — the confirmation gates the mutation.
    expect(api.reloadRuntime).not.toHaveBeenCalled()
    await user.click(
      within(confirm).getByRole('button', { name: /reload runtime/i }),
    )
    await waitFor(() => expect(api.reloadRuntime).toHaveBeenCalledTimes(1))
  })
})
