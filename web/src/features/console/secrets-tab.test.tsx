// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactElement, ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import './i18n'

const { api, authState } = vi.hoisted(() => ({
  api: {
    listSecrets: vi.fn(),
    putSecret: vi.fn(),
    deleteSecret: vi.fn(),
  },
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

  it('lists a secret with its name and hint, and never renders the value', async () => {
    api.listSecrets.mockResolvedValue(oneSecret)
    wrap(<SecretsTab />)
    expect(await screen.findByText('gdrive/token')).toBeInTheDocument()
    // The non-secret fingerprint is shown so an admin can tell a secret is set.
    expect(screen.getByText('a1b2c3')).toBeInTheDocument()
    expect(screen.getByText('Drive OAuth token')).toBeInTheDocument()
  })

  it('shows an empty state when there are no secrets', async () => {
    wrap(<SecretsTab />)
    expect(await screen.findByText(/no secrets yet/i)).toBeInTheDocument()
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
    await user.type(within(dialog).getByLabelText(/^value/i), 's3cr3t-value')
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
    await user.click(within(dialog).getByRole('button', { name: /delete/i }))
    await waitFor(() =>
      expect(api.deleteSecret).toHaveBeenCalledWith('gdrive/token'),
    )
  })
})
