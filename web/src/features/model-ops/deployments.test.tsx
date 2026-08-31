// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Deployments tab tests. The load-bearing case: a deny-closed admission 422 is a
// security decision shown INLINE with the engine's verbatim reason, the form is
// RETAINED (not closed, no error toast), and the create body carries only the fields
// the operator supplied.
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
import { ApiError } from '@/lib/api/errors'

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
  deployments: vi.fn(),
  createDeployment: vi.fn(),
  updateDeployment: vi.fn(),
  deleteDeployment: vi.fn(),
}))
vi.mock('@/features/models/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/models/api')>()
  return { ...actual, modelsApi: api }
})

import { modelsKeys } from '@/features/models/api'
import { DeploymentsTab } from './deployments'
import './i18n'
import '@/features/_intel'

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  authState.can = () => true
  api.deployments.mockReset()
  api.createDeployment.mockReset()
  api.updateDeployment.mockReset()
  api.deleteDeployment.mockReset()
  toast.success.mockReset()
  toast.error.mockReset()
  toast.warning.mockReset()
  api.deployments.mockResolvedValue({ items: [] })
})
afterEach(() => vi.clearAllMocks())

// Open the new-deployment form as a VALID local deployment (D-08: a local deployment
// requires both refs — the form defaults to `local` and cannot be submitted empty).
async function openLocalDeployment() {
  const user = userEvent.setup()
  wrap(<DeploymentsTab />)
  await user.click(
    await screen.findByRole('button', { name: /new deployment/i }),
  )
  const name = await screen.findByLabelText(/^name/i)
  fireEvent.change(name, { target: { value: 'edge-a' } })
  fireEvent.change(screen.getByLabelText(/model reference/i), {
    target: { value: 'm1' },
  })
  fireEvent.change(screen.getByLabelText(/version reference/i), {
    target: { value: 'v1' },
  })
  return user
}

describe('DeploymentsTab — admission 422 is an inline security decision', () => {
  it('shows the verbatim deny reason and keeps the form open, no error toast', async () => {
    const reason =
      'the trust anchor that admitted this version is no longer in the policy; re-admit before deploying'
    api.createDeployment.mockRejectedValue(
      new ApiError(422, 'admission_denied', reason),
    )
    const user = await openLocalDeployment()

    await user.click(screen.getByRole('button', { name: /^create$/i }))

    await waitFor(() => expect(screen.getByText(reason)).toBeInTheDocument())
    // Blocked-by-admission banner, form still mounted (name field present), no error toast.
    expect(screen.getByText(/blocked by admission/i)).toBeInTheDocument()
    expect(screen.getByLabelText(/^name/i)).toBeInTheDocument()
    expect(toast.error).not.toHaveBeenCalled()
  })

  it('sends a discriminated LOCAL body carrying the owned+version refs', async () => {
    api.createDeployment.mockResolvedValue({
      id: 'd1',
      name: 'edge-a',
      runtime: 'vllm',
      deployment_type: 'local',
      status: 'active',
      governed: false,
    })
    const user = await openLocalDeployment()
    await user.click(screen.getByRole('button', { name: /^create$/i }))

    await waitFor(() => expect(api.createDeployment).toHaveBeenCalledTimes(1))
    const body = api.createDeployment.mock.calls[0][0]
    // D-08: a local deployment is an explicit type that MUST carry both refs — never
    // the pre-fix `{name, runtime, active}` with empty refs that skipped the gate.
    expect(body).toEqual({
      name: 'edge-a',
      runtime: 'vllm',
      deployment_type: 'local',
      status: 'active',
      governed: false,
      owned_ref: 'm1',
      version_ref: 'v1',
    })
    expect(toast.success).toHaveBeenCalled()
  })
})

describe('DeploymentsTab — D-08 discriminated create', () => {
  it('refuses to submit a local deployment without its refs (no silent empty create)', async () => {
    const user = userEvent.setup()
    wrap(<DeploymentsTab />)
    await user.click(
      await screen.findByRole('button', { name: /new deployment/i }),
    )
    // Only a name — the default `local` type has no refs yet, so Create stays disabled and
    // the old empty-body create can never fire.
    fireEvent.change(await screen.findByLabelText(/^name/i), {
      target: { value: 'edge-a' },
    })
    expect(screen.getByRole('button', { name: /^create$/i })).toBeDisabled()
    expect(api.createDeployment).not.toHaveBeenCalled()
  })

  it('sends a discriminated BROKERED body with the provider endpoint, no self-hosted refs', async () => {
    api.createDeployment.mockResolvedValue({
      id: 'd2',
      name: 'edge-a',
      runtime: 'vllm',
      deployment_type: 'brokered',
      status: 'active',
      governed: false,
    })
    const user = userEvent.setup()
    wrap(<DeploymentsTab />)
    await user.click(
      await screen.findByRole('button', { name: /new deployment/i }),
    )
    fireEvent.change(await screen.findByLabelText(/^name/i), {
      target: { value: 'edge-a' },
    })
    // Switch the discriminator to brokered (the first combobox in the form).
    await user.click(screen.getAllByRole('combobox')[0])
    const listbox = await screen.findByRole('listbox')
    await user.click(within(listbox).getByRole('option', { name: /brokered/i }))
    fireEvent.change(screen.getByLabelText(/provider endpoint/i), {
      target: { value: 'https://api.anthropic.com' },
    })
    await user.click(screen.getByRole('button', { name: /^create$/i }))

    await waitFor(() => expect(api.createDeployment).toHaveBeenCalledTimes(1))
    const body = api.createDeployment.mock.calls[0][0]
    expect(body).toEqual({
      name: 'edge-a',
      runtime: 'vllm',
      deployment_type: 'brokered',
      status: 'active',
      governed: false,
      endpoint_ref: 'https://api.anthropic.com',
    })
    // A brokered deployment never carries self-hosted refs (the server rejects them).
    expect(body).not.toHaveProperty('owned_ref')
    expect(body).not.toHaveProperty('version_ref')
    expect(toast.success).toHaveBeenCalled()
  })
})

describe('Despliegues — el techo se pide y el recorte se declara', () => {
  /**
   * ⛔ TESTIGO DE TRANSPORTE: el techo tiene que llegar a la llamada. Sin `limit` el
   * repositorio genérico pagina a 100 y el handler publica un `has_more` que la pantalla
   * tiraba, así que una lista recortada se leía como completa.
   */
  it('pide el techo real del motor', async () => {
    wrap(<DeploymentsTab />)
    await waitFor(() => expect(api.deployments).toHaveBeenCalled())
    expect(api.deployments).toHaveBeenLastCalledWith({ limit: 1000 })
  })

  /** El aviso, en las dos direcciones, y con la cifra REAL —no la constante pedida—. */
  it('declara el recorte con has_more y no sin él, con la cifra cargada', async () => {
    api.deployments.mockResolvedValue({
      items: [{ id: 'd1', name: 'edge-a', runtime: 'local', status: 'active' }],
      has_more: true,
    })
    wrap(<DeploymentsTab />)
    expect(
      await screen.findByText('Loaded 1 deployments; there are more'),
    ).toBeVisible()

    cleanup()
    api.deployments.mockResolvedValue({
      items: [{ id: 'd1', name: 'edge-a', runtime: 'local', status: 'active' }],
      has_more: false,
    })
    wrap(<DeploymentsTab />)
    // ⛔ SE ESPERA A QUE LA FILA ESTÉ PINTADA antes de afirmar la ausencia: comprobar
    //    `toHaveBeenCalled` no garantiza que la respuesta haya llegado al DOM, así que la
    //    negativa podría pasar por llegar pronto y no por ser cierta. Lo señaló el contraste.
    await screen.findByText('edge-a')
    expect(
      screen.queryByText(/Loaded \d+ deployments; there are more/i),
    ).toBeNull()
  })

  /**
   * ⛔ LA GUARDA DEL ERROR, con dato viejo sembrado. Un rechazo desde el primer intento deja
   * `data` en `undefined` y la casilla pasaría SIN llegar a la guarda; react-query conserva el
   * último dato bueno mientras marca el error, y ése es el escenario real.
   */
  it('un refetch fallido retira el aviso aunque el dato viejo diga has_more', async () => {
    const clave = modelsKeys.deployments('t1', { limit: 1000 })
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false, staleTime: 0 } },
    })
    qc.setQueryData(clave, {
      items: [{ id: 'd1', name: 'edge-a', runtime: 'local', status: 'active' }],
      has_more: true,
    })
    api.deployments.mockRejectedValue(new Error('boom'))
    render(
      <QueryClientProvider client={qc}>
        <DeploymentsTab />
      </QueryClientProvider>,
    )
    await waitFor(() => expect(api.deployments).toHaveBeenCalled())
    await waitFor(() => expect(qc.getQueryState(clave)?.error).toBeTruthy())
    expect(
      screen.queryByText(/Loaded \d+ deployments; there are more/i),
    ).toBeNull()
  })
})

describe('Despliegues — el techo viaja CON los filtros del servidor', () => {
  /**
   * ⛔ EL MUTANTE QUE FALTABA: retirar `...filters` y dejar sólo `{ limit }`. La casilla de
   * transporte de arriba sólo prueba «sin filtros», así que ese mutante pasaría — y volvería
   * FALSA la copy, que dice que runtime y estado los aplica el motor. El motor los mete en el
   * mismo `model.Query` ANTES de paginar (`modules/models/owned.go`), o sea que el recorte es
   * del conjunto ya filtrado; si el filtro no viajara, el aviso hablaría de otra cosa.
   */
  it('manda el filtro de runtime junto al techo', async () => {
    const user = userEvent.setup()
    wrap(<DeploymentsTab />)
    await waitFor(() => expect(api.deployments).toHaveBeenCalled())

    await user.click(screen.getAllByRole('combobox')[0])
    const listbox = await screen.findByRole('listbox')
    await user.click(within(listbox).getByRole('option', { name: 'vllm' }))

    await waitFor(() =>
      expect(api.deployments).toHaveBeenLastCalledWith({
        runtime: 'vllm',
        limit: 1000,
      }),
    )
  })
})
