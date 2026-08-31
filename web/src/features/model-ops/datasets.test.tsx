// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Datasets tab tests. These pin: the create body carries the operator `verified` claim
// but never a server-set field (id/attested_by/attested_at would 400 the strict decoder);
// a validation/reference refusal is shown inline with the form retained; and there is NO
// classification filter (the backend can't filter by it, so filtering one loaded page
// would falsely imply completeness).
import type { ReactNode } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'
import { useStepUpStore } from '@/stores/step-up'

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
  datasets: vi.fn(),
  createDataset: vi.fn(),
  deleteDataset: vi.fn(),
}))
vi.mock('@/features/models/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/models/api')>()
  return { ...actual, modelsApi: api }
})

import { modelsKeys } from '@/features/models/api'
import { DatasetsTab } from './datasets'
import './i18n'
import '@/features/_intel'

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  authState.can = () => true
  api.datasets.mockReset()
  api.createDataset.mockReset()
  toast.success.mockReset()
  api.datasets.mockResolvedValue({ items: [] })
  api.createDataset.mockResolvedValue({
    id: 'd1',
    name: 'ds',
    classification: 'other',
    verified: true,
  })
})
afterEach(() => vi.clearAllMocks())

describe('DatasetForm — honest, strict-decoder-safe body', () => {
  it('sends the operator verified claim and never a server-set field', async () => {
    const user = userEvent.setup()
    wrap(<DatasetsTab />)
    await user.click(
      await screen.findByRole('button', { name: /new dataset/i }),
    )

    fireEvent.change(await screen.findByLabelText(/^name/i), {
      target: { value: 'ds' },
    })
    await user.click(screen.getByLabelText(/provenance reviewed/i))
    await user.click(screen.getByRole('button', { name: /^create$/i }))

    await waitFor(() => expect(api.createDataset).toHaveBeenCalledTimes(1))
    const body = api.createDataset.mock.calls[0][0]
    expect(body.verified).toBe(true)
    expect(body.name).toBe('ds')
    expect(body).not.toHaveProperty('id')
    expect(body).not.toHaveProperty('attested_by')
    expect(body).not.toHaveProperty('attested_at')
  })

  it('un step_up_required abre la CEREMONIA y no acusa al operador', async () => {
    // ⛔ LA CLASE ENTERA DE ESTE PR, en su forma de ESCRITURA. Estos `onError` a mano
    // ramificaban por `isForbidden`, que es SÓLO el status 403 (lib/api/errors.ts:59): un
    // `step_up_required` lo satisface también, así que el operador recibía «no tienes
    // autorización» —falso, tiene el permiso— en vez de la ceremonia que levanta la negativa,
    // y con el formulario recién tecleado por medio. El reporte vive ahora en
    // `useFailedActionReporter` (use-privileged-mutation.ts:33-59), que es donde ya estaba
    // la política; su propio comentario dice que estos handlers la re-implementaban mal.
    useStepUpStore.setState({ request: null })
    const user = userEvent.setup()
    api.createDataset.mockRejectedValue(
      new ApiError(403, 'step_up_required', 'assurance level too low'),
    )
    wrap(<DatasetsTab />)
    await user.click(
      await screen.findByRole('button', { name: /new dataset/i }),
    )
    fireEvent.change(await screen.findByLabelText(/^name/i), {
      target: { value: 'ds' },
    })
    await user.click(screen.getByRole('button', { name: /^create$/i }))

    // Ancla POSITIVA: la ceremonia se pidió.
    await waitFor(() =>
      expect(useStepUpStore.getState().request).not.toBeNull(),
    )
    // Y la acusación NO acompaña.
    expect(toast.warning).not.toHaveBeenCalled()
  })

  it('un 403 SIN código de ceremonia conserva la negativa de ROL', async () => {
    // Control negativo: sin código, la negativa es de rol, es cierta, y se queda como estaba.
    // Sin esta celda, mandar los DOS 403 a la ceremonia también pasaría — el defecto simétrico.
    useStepUpStore.setState({ request: null })
    const user = userEvent.setup()
    api.createDataset.mockRejectedValue(new ApiError(403, 'forbidden', 'no'))
    wrap(<DatasetsTab />)
    await user.click(
      await screen.findByRole('button', { name: /new dataset/i }),
    )
    fireEvent.change(await screen.findByLabelText(/^name/i), {
      target: { value: 'ds' },
    })
    await user.click(screen.getByRole('button', { name: /^create$/i }))

    await waitFor(() => expect(toast.warning).toHaveBeenCalledTimes(1))
    expect(useStepUpStore.getState().request).toBeNull()
  })

  it('shows a reference/validation refusal inline and keeps the form', async () => {
    const user = userEvent.setup()
    api.createDataset.mockRejectedValue(
      new ApiError(404, 'not_found', 'referenced owned model not found'),
    )
    wrap(<DatasetsTab />)
    await user.click(
      await screen.findByRole('button', { name: /new dataset/i }),
    )
    fireEvent.change(await screen.findByLabelText(/^name/i), {
      target: { value: 'ds' },
    })
    await user.click(screen.getByRole('button', { name: /^create$/i }))

    expect(
      await screen.findByText(/referenced owned model not found/i),
    ).toBeInTheDocument()
    // Form retained, no generic error toast.
    expect(toast.error).not.toHaveBeenCalled()
    expect(
      screen.getByRole('button', { name: /^create$/i }),
    ).toBeInTheDocument()
  })
})

describe('DatasetsTab — no misleading filter', () => {
  it('offers no classification filter (the backend cannot filter by it)', async () => {
    wrap(<DatasetsTab />)
    await screen.findByRole('button', { name: /new dataset/i })
    // The only combobox anywhere would be a filter; the tab has none.
    expect(screen.queryByRole('combobox')).not.toBeInTheDocument()
  })
})

describe('Datasets — el techo se pide y el recorte se declara', () => {
  /**
   * ⛔ TESTIGO DE TRANSPORTE: el techo tiene que llegar a la llamada. Sin `limit` el
   * repositorio genérico pagina a 100 y el handler publica un `has_more` que la pantalla
   * tiraba, así que una lista recortada se leía como completa.
   */
  it('pide el techo real del motor', async () => {
    wrap(<DatasetsTab />)
    await waitFor(() => expect(api.datasets).toHaveBeenCalled())
    expect(api.datasets).toHaveBeenLastCalledWith({ limit: 1000 })
  })

  /** El aviso, en las dos direcciones, y con la cifra REAL —no la constante pedida—. */
  it('declara el recorte con has_more y no sin él, con la cifra cargada', async () => {
    api.datasets.mockResolvedValue({
      items: [{ id: 'ds1', name: 'corpus' }],
      has_more: true,
    })
    wrap(<DatasetsTab />)
    expect(
      await screen.findByText('Loaded 1 datasets; there are more'),
    ).toBeVisible()

    cleanup()
    api.datasets.mockResolvedValue({
      items: [{ id: 'ds1', name: 'corpus' }],
      has_more: false,
    })
    wrap(<DatasetsTab />)
    // ⛔ SE ESPERA A QUE LA FILA ESTÉ PINTADA antes de afirmar la ausencia: comprobar
    //    `toHaveBeenCalled` no garantiza que la respuesta haya llegado al DOM, así que la
    //    negativa podría pasar por llegar pronto y no por ser cierta. Lo señaló el contraste.
    await screen.findByText('corpus')
    expect(
      screen.queryByText(/Loaded \d+ datasets; there are more/i),
    ).toBeNull()
  })

  /**
   * ⛔ LA GUARDA DEL ERROR, con dato viejo sembrado. Un rechazo desde el primer intento deja
   * `data` en `undefined` y la casilla pasaría SIN llegar a la guarda; react-query conserva el
   * último dato bueno mientras marca el error, y ése es el escenario real.
   */
  it('un refetch fallido retira el aviso aunque el dato viejo diga has_more', async () => {
    const clave = modelsKeys.datasets('t1', undefined, { limit: 1000 })
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false, staleTime: 0 } },
    })
    qc.setQueryData(clave, {
      items: [{ id: 'ds1', name: 'corpus' }],
      has_more: true,
    })
    api.datasets.mockRejectedValue(new Error('boom'))
    render(
      <QueryClientProvider client={qc}>
        <DatasetsTab />
      </QueryClientProvider>,
    )
    await waitFor(() => expect(api.datasets).toHaveBeenCalled())
    await waitFor(() => expect(qc.getQueryState(clave)?.error).toBeTruthy())
    expect(
      screen.queryByText(/Loaded \d+ datasets; there are more/i),
    ).toBeNull()
  })
})
