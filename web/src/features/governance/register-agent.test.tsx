// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

const toast = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
  info: vi.fn(),
}))
vi.mock('@/components/ui/toaster', () => ({ toast, Toaster: () => null }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))

const api = vi.hoisted(() => ({ registerAgent: vi.fn() }))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, governanceApi: { ...actual.governanceApi, ...api } }
})

import { RegisterAgentDialog } from './register-agent-dialog'
import './i18n'

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.clearAllMocks()
  api.registerAgent.mockResolvedValue({ promoted: false })
})

async function abrir() {
  const user = userEvent.setup()
  wrap(<RegisterAgentDialog canAdmin />)
  await user.click(screen.getByRole('button', { name: /register agent/i }))
  return user
}

describe('RegisterAgentDialog', () => {
  it('no se ofrece sin governance:nhi:admin', () => {
    wrap(<RegisterAgentDialog canAdmin={false} />)
    expect(screen.queryByRole('button', { name: /register agent/i })).toBeNull()
  })

  // El motor exige patrocinador deny-closed: el formulario no puede tratarlo como opcional.
  it('no deja registrar sin patrocinador, aunque haya identidad', async () => {
    const user = await abrir()
    fireEvent.change(await screen.findByLabelText(/^agent identity$/i), {
      target: { value: 'agent-9' },
    })
    expect(screen.getByRole('button', { name: /^register$/i })).toBeDisabled()
    fireEvent.change(screen.getByLabelText(/^sponsor$/i), {
      target: { value: 'user:1' },
    })
    await waitFor(() =>
      expect(screen.getByRole('button', { name: /^register$/i })).toBeEnabled(),
    )
    await user.click(screen.getByRole('button', { name: /^register$/i }))
    await waitFor(() => expect(api.registerAgent).toHaveBeenCalledTimes(1))
  })

  it('manda los campos recortados y omite los vacíos', async () => {
    const user = await abrir()
    fireEvent.change(await screen.findByLabelText(/^agent identity$/i), {
      target: { value: '  agent-9  ' },
    })
    fireEvent.change(screen.getByLabelText(/^sponsor$/i), {
      target: { value: '  user:1  ' },
    })
    await user.click(screen.getByRole('button', { name: /^register$/i }))
    await waitFor(() => expect(api.registerAgent).toHaveBeenCalledTimes(1))
    expect(api.registerAgent.mock.calls[0][0]).toEqual({
      identity_ref: 'agent-9',
      sponsor_ref: 'user:1',
    })
  })

  // La celda que da sentido a `postWithMeta`: 200 es PROMOVIDO, no creado.
  it('una PROMOCIÓN no se anuncia como creación', async () => {
    api.registerAgent.mockResolvedValue({ promoted: true })
    const user = await abrir()
    fireEvent.change(await screen.findByLabelText(/^agent identity$/i), {
      target: { value: 'agent-9' },
    })
    fireEvent.change(screen.getByLabelText(/^sponsor$/i), {
      target: { value: 'user:1' },
    })
    await user.click(screen.getByRole('button', { name: /^register$/i }))
    await waitFor(() => expect(toast.success).toHaveBeenCalledTimes(1))
    expect(toast.success.mock.calls[0][0]).toMatch(/promoted/i)
    expect(toast.success.mock.calls[0][0]).not.toMatch(/registered/i)
  })

  it('una CREACIÓN sí se anuncia como registro', async () => {
    const user = await abrir()
    fireEvent.change(await screen.findByLabelText(/^agent identity$/i), {
      target: { value: 'agent-9' },
    })
    fireEvent.change(screen.getByLabelText(/^sponsor$/i), {
      target: { value: 'user:1' },
    })
    await user.click(screen.getByRole('button', { name: /^register$/i }))
    await waitFor(() => expect(toast.success).toHaveBeenCalledTimes(1))
    expect(toast.success.mock.calls[0][0]).toMatch(/registered/i)
  })

  it('la ayuda del patrocinador dice que debe ser humano y estar en el roster', async () => {
    await abrir()
    expect(
      await screen.findByText(
        /must be a HUMAN identity already in the roster/i,
      ),
    ).toBeInTheDocument()
  })
})
