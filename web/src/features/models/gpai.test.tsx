// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ReactNode } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'
import type { GpaiPosture } from './types'

const toast = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
  info: vi.fn(),
}))
vi.mock('@/components/ui/toaster', () => ({ toast, Toaster: () => null }))

const authState = vi.hoisted(() => ({
  activeTenant: 'tenant-1' as string | null,
  can: (_permission: string): boolean => true,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))

const api = vi.hoisted(() => ({
  gpaiPostures: vi.fn(),
  attestGpaiPosture: vi.fn(),
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, modelsApi: api }
})

import { GpaiTab } from './gpai'
import './i18n'
import '@/features/_intel'

const posture: GpaiPosture = {
  id: 'gpai-1',
  provider_ref: 'provider-one',
  cop_signatory: false,
  technical_docs: true,
  training_data_summary: false,
  copyright_policy: false,
  downstream_info: false,
  systemic_risk: false,
  safety_report: false,
  verified: false,
  attested_by: 'operator@example.test',
  attested_at: '2026-07-22T10:00:00Z',
  note: 'Initial supplier review.',
}

function renderTab(ui: ReactNode = <GpaiTab />) {
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  })
  return render(
    <QueryClientProvider client={queryClient}>{ui}</QueryClientProvider>,
  )
}

beforeEach(() => {
  authState.can = () => true
  api.gpaiPostures.mockReset()
  api.attestGpaiPosture.mockReset()
  toast.success.mockReset()
  toast.error.mockReset()
  toast.warning.mockReset()
  api.gpaiPostures.mockResolvedValue({
    items: [posture],
    cursor: '',
    has_more: false,
  })
  api.attestGpaiPosture.mockResolvedValue(posture)
})

afterEach(() => vi.clearAllMocks())

async function openCreateDialog() {
  const user = userEvent.setup()
  renderTab()
  await user.click(
    await screen.findByRole('button', { name: 'Attest posture' }),
  )
  return { user, dialog: await screen.findByRole('dialog') }
}

describe('GpaiTab', () => {
  it('sends only the writable PUT fields in canonical claim order', async () => {
    const { user, dialog } = await openCreateDialog()
    await user.type(
      within(dialog).getByRole('textbox', { name: 'Provider reference' }),
      'new-provider',
    )

    await user.click(
      within(dialog).getByRole('button', { name: 'Attest posture' }),
    )

    await waitFor(() => expect(api.attestGpaiPosture).toHaveBeenCalledTimes(1))
    expect(api.attestGpaiPosture).toHaveBeenCalledWith({
      provider_ref: 'new-provider',
      cop_signatory: false,
      technical_docs: false,
      training_data_summary: false,
      copyright_policy: false,
      downstream_info: false,
      systemic_risk: false,
      safety_report: false,
      verified: false,
    })
    const body = api.attestGpaiPosture.mock.calls[0][0]
    expect(body).not.toHaveProperty('id')
    expect(body).not.toHaveProperty('attested_by')
    expect(body).not.toHaveProperty('attested_at')
  })

  it('persists all seven GPAI claim switches as one canonical action', async () => {
    const { user, dialog } = await openCreateDialog()
    await user.type(
      within(dialog).getByRole('textbox', { name: 'Provider reference' }),
      'seven-claims-provider',
    )
    for (const name of [
      'Code of Practice signatory',
      'Technical documentation published',
      'Training-data summary published',
      'Copyright policy recorded',
      'Downstream information provided',
      'GPAI with systemic risk',
      'Safety & Security Model Report',
    ]) {
      await user.click(within(dialog).getByRole('switch', { name }))
    }
    await user.click(
      within(dialog).getByRole('button', { name: 'Attest posture' }),
    )

    await waitFor(() => expect(api.attestGpaiPosture).toHaveBeenCalledTimes(1))
    expect(api.attestGpaiPosture).toHaveBeenCalledWith(
      expect.objectContaining({
        cop_signatory: true,
        technical_docs: true,
        training_data_summary: true,
        copyright_policy: true,
        downstream_info: true,
        systemic_risk: true,
        safety_report: true,
      }),
    )
  })

  it('requires a verification method for an operator-reviewed posture', async () => {
    const { user, dialog } = await openCreateDialog()
    await user.type(
      within(dialog).getByRole('textbox', { name: 'Provider reference' }),
      'new-provider',
    )
    await user.click(
      within(dialog).getByRole('switch', {
        name: 'Reviewed against published provider material',
      }),
    )

    await user.click(
      within(dialog).getByRole('button', { name: 'Attest posture' }),
    )

    expect(
      await within(dialog).findByText(
        'Describe how the published provider material was reviewed.',
      ),
    ).toBeInTheDocument()
    expect(api.attestGpaiPosture).not.toHaveBeenCalled()
  })

  it('locks provider_ref in the edit dialog', async () => {
    const user = userEvent.setup()
    renderTab()
    await user.click(
      await screen.findByRole('button', {
        name: 'Edit posture for provider-one',
      }),
    )

    const dialog = await screen.findByRole('dialog')
    expect(
      within(dialog).getByRole('textbox', { name: 'Provider reference' }),
    ).toBeDisabled()
    expect(
      within(dialog).getByText(/locked while editing/i),
    ).toBeInTheDocument()
  })

  it('renders absent claims neutrally and never invents an aggregate score', async () => {
    renderTab()
    expect(await screen.findByText('provider-one')).toBeInTheDocument()
    expect(screen.getAllByText('Claim not recorded').length).toBeGreaterThan(0)
    expect(document.body.textContent ?? '').not.toMatch(/\b\d+\s*\/\s*7\b/)
  })

  it('retains the form and shows a backend 400 error inline', async () => {
    api.attestGpaiPosture.mockRejectedValue(
      new ApiError(400, 'bad_request', 'provider posture was rejected'),
    )
    const { user, dialog } = await openCreateDialog()
    await user.type(
      within(dialog).getByRole('textbox', { name: 'Provider reference' }),
      'new-provider',
    )

    await user.click(
      within(dialog).getByRole('button', { name: 'Attest posture' }),
    )

    expect(
      await within(dialog).findByText('provider posture was rejected'),
    ).toBeInTheDocument()
    expect(
      within(dialog).getByRole('textbox', { name: 'Provider reference' }),
    ).toHaveValue('new-provider')
    expect(toast.error).not.toHaveBeenCalled()
  })
})

describe('Posturas GPAI — el techo se pide y el recorte se declara', () => {
  it('pide el techo real del motor', async () => {
    renderTab()
    await waitFor(() => expect(api.gpaiPostures).toHaveBeenCalled())
    expect(api.gpaiPostures).toHaveBeenLastCalledWith({ limit: 1000 })
  })

  it('declara el recorte con has_more y no sin él, con la cifra cargada', async () => {
    api.gpaiPostures.mockResolvedValue({ items: [posture], has_more: true })
    renderTab()
    expect(
      await screen.findByText('Loaded 1 postures; there are more'),
    ).toBeVisible()

    cleanup()
    api.gpaiPostures.mockResolvedValue({ items: [posture], has_more: false })
    renderTab()
    await screen.findByText('provider-one')
    expect(
      screen.queryByText(/Loaded \d+ postures; there are more/i),
    ).toBeNull()
  })
})
