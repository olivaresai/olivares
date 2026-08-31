// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Fine-tune jobs tab tests. The load-bearing case pins the timestamp regression Codex
// caught: the engine's canonical format demands EXACTLY nine fractional digits, so a
// datetime the form submits must carry a nine-digit fraction — a millisecond (three-digit)
// value would be silently dropped by the backend. Also pins the "track, not run" banner
// and that a create body never carries server-set fields.
import type { ReactNode } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const toast = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
  info: vi.fn(),
}))
vi.mock('@/components/ui/toaster', () => ({ toast, Toaster: () => null }))

const authState = vi.hoisted(() => ({
  activeTenant: 't1' as string | null,
  can: (_p: string): boolean => true,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))

const api = vi.hoisted(() => ({
  finetuneJobs: vi.fn(),
  createFinetuneJob: vi.fn(),
  updateFinetuneJob: vi.fn(),
}))
vi.mock('@/features/models/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/models/api')>()
  return { ...actual, modelsApi: api }
})

import { modelsKeys } from '@/features/models/api'
import { FinetuneTab } from './finetune'
import './i18n'
import '@/features/_intel'

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  authState.can = () => true
  api.finetuneJobs.mockReset()
  api.createFinetuneJob.mockReset()
  api.updateFinetuneJob.mockReset()
  toast.success.mockReset()
  api.finetuneJobs.mockResolvedValue({ items: [] })
  api.createFinetuneJob.mockResolvedValue({
    id: 'j1',
    name: 'ft-1',
    status: 'running',
  })
})
afterEach(() => vi.clearAllMocks())

describe('FinetuneForm — canonical timestamps', () => {
  it('submits started_at with EXACTLY nine fractional digits (never 3-digit ms)', async () => {
    const user = userEvent.setup()
    wrap(<FinetuneTab />)
    await user.click(await screen.findByRole('button', { name: /new job/i }))

    fireEvent.change(await screen.findByLabelText(/^name/i), {
      target: { value: 'ft-1' },
    })
    // datetime-local minute precision; the form must pad the fraction to nine digits.
    fireEvent.change(screen.getByLabelText(/started at/i), {
      target: { value: '2026-07-21T10:30' },
    })
    await user.click(screen.getByRole('button', { name: /^create$/i }))

    await waitFor(() => expect(api.createFinetuneJob).toHaveBeenCalledTimes(1))
    const body = api.createFinetuneJob.mock.calls[0][0]
    expect(body.started_at).toMatch(
      /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{9}Z$/,
    )
    // Never a bare id/attested field (server-set, DisallowUnknownFields).
    expect(body).not.toHaveProperty('id')
    expect(body).not.toHaveProperty('attested_by')
  })

  it('omits an empty timestamp rather than sending an invalid one', async () => {
    const user = userEvent.setup()
    wrap(<FinetuneTab />)
    await user.click(await screen.findByRole('button', { name: /new job/i }))
    fireEvent.change(await screen.findByLabelText(/^name/i), {
      target: { value: 'ft-2' },
    })
    await user.click(screen.getByRole('button', { name: /^create$/i }))

    await waitFor(() => expect(api.createFinetuneJob).toHaveBeenCalledTimes(1))
    const body = api.createFinetuneJob.mock.calls[0][0]
    expect(body).not.toHaveProperty('started_at')
    expect(body).not.toHaveProperty('ended_at')
  })
})

describe('FinetuneForm — honesty', () => {
  it('shows the track-not-run banner', async () => {
    const user = userEvent.setup()
    wrap(<FinetuneTab />)
    await user.click(await screen.findByRole('button', { name: /new job/i }))
    expect(
      await screen.findByText(/does not start, cancel, or execute training/i),
    ).toBeInTheDocument()
  })
})

describe('Fine-tunes — el techo se pide y el recorte se declara', () => {
  /**
   * ⛔ TESTIGO DE TRANSPORTE: el techo tiene que llegar a la llamada. Sin `limit` el
   * repositorio genérico pagina a 100 y el handler publica un `has_more` que la pantalla
   * tiraba, así que una lista recortada se leía como completa.
   */
  it('pide el techo real del motor', async () => {
    wrap(<FinetuneTab />)
    await waitFor(() => expect(api.finetuneJobs).toHaveBeenCalled())
    expect(api.finetuneJobs).toHaveBeenLastCalledWith({ limit: 1000 })
  })

  /** El aviso, en las dos direcciones, y con la cifra REAL —no la constante pedida—. */
  it('declara el recorte con has_more y no sin él, con la cifra cargada', async () => {
    api.finetuneJobs.mockResolvedValue({
      items: [{ id: 'j1', name: 'run-1', status: 'running' }],
      has_more: true,
    })
    wrap(<FinetuneTab />)
    expect(
      await screen.findByText('Loaded 1 jobs; there are more'),
    ).toBeVisible()

    cleanup()
    api.finetuneJobs.mockResolvedValue({
      items: [{ id: 'j1', name: 'run-1', status: 'running' }],
      has_more: false,
    })
    wrap(<FinetuneTab />)
    // ⛔ SE ESPERA A QUE LA FILA ESTÉ PINTADA antes de afirmar la ausencia: comprobar
    //    `toHaveBeenCalled` no garantiza que la respuesta haya llegado al DOM, así que la
    //    negativa podría pasar por llegar pronto y no por ser cierta. Lo señaló el contraste.
    await screen.findByText('run-1')
    expect(screen.queryByText(/Loaded \d+ jobs; there are more/i)).toBeNull()
  })

  /**
   * ⛔ LA GUARDA DEL ERROR, con dato viejo sembrado. Un rechazo desde el primer intento deja
   * `data` en `undefined` y la casilla pasaría SIN llegar a la guarda; react-query conserva el
   * último dato bueno mientras marca el error, y ése es el escenario real.
   */
  it('un refetch fallido retira el aviso aunque el dato viejo diga has_more', async () => {
    const clave = modelsKeys.finetuneJobs('t1', { limit: 1000 })
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false, staleTime: 0 } },
    })
    qc.setQueryData(clave, {
      items: [{ id: 'j1', name: 'run-1', status: 'running' }],
      has_more: true,
    })
    api.finetuneJobs.mockRejectedValue(new Error('boom'))
    render(
      <QueryClientProvider client={qc}>
        <FinetuneTab />
      </QueryClientProvider>,
    )
    await waitFor(() => expect(api.finetuneJobs).toHaveBeenCalled())
    await waitFor(() => expect(qc.getQueryState(clave)?.error).toBeTruthy())
    expect(screen.queryByText(/Loaded \d+ jobs; there are more/i)).toBeNull()
  })
})

describe('Fine-tunes — el techo viaja CON el filtro del servidor', () => {
  /** Mismo mutante que en despliegues: sin esta casilla, retirar `...filters` pasaría. */
  it('manda el filtro de estado junto al techo', async () => {
    const user = userEvent.setup()
    wrap(<FinetuneTab />)
    await waitFor(() => expect(api.finetuneJobs).toHaveBeenCalled())

    await user.click(screen.getAllByRole('combobox')[0])
    const listbox = await screen.findByRole('listbox')
    await user.click(within(listbox).getByRole('option', { name: 'running' }))

    await waitFor(() =>
      expect(api.finetuneJobs).toHaveBeenLastCalledWith({
        status: 'running',
        limit: 1000,
      }),
    )
  })
})
