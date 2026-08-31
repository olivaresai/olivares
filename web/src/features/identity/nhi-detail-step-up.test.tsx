// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repo root.
//
// ⛔ ESTA CELDA EXISTE PORQUE EL CONTRASTE ENCONTRÓ LO QUE YO NO PROBÉ. Esta hoja resume DOS
// lecturas en un solo booleano; al añadir el gemelo de ceremonia creé una TERCERA composición
// —ceremonia y avería roja a la vez, y los datos de eventos visibles bajo la ceremonia— y no
// escribí ninguna celda que renderizara el componente. `codex the model max` lo cazó con
// file:line; mis celdas focales pasaban sin ejercer esta composición.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'

const api = vi.hoisted(() => ({ nhiDetail: vi.fn(), nhiEvents: vi.fn() }))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, identityApi: { ...actual.identityApi, ...api } }
})
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))
// ⛔ EL DOBLE EXPONE `onElevated`. Sin él estas celdas sólo miran la COMPOSICIÓN —que no
// haya avería roja junto a la ceremonia, que los eventos no se cuelen debajo— y no ven si la
// ceremonia lleva a alguna parte: `onElevated={undefined}` en el sitio productivo las dejaría
// verdes. Es el agujero que el contraste `sol max` me encontró cuatro veces en esta campaña,
// y aquí lo destapó un barrido de mis PROPIAS celdas.
vi.mock('@/features/identity/assurance', () => ({
  StepUpPanel: ({ onElevated }: { onElevated?: () => void }) => (
    <div>
      <span>step-up ceremony</span>
      <button type="button" onClick={() => onElevated?.()}>
        elevar
      </button>
    </div>
  ),
}))

import { NhiDetailSheet } from './nhi-detail'
import './i18n'

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

const stepUp = () =>
  new ApiError(403, 'step_up_required', 'assurance level too low')

beforeEach(() => {
  api.nhiDetail.mockReset()
  api.nhiEvents.mockReset()
})

describe('NhiDetailSheet — una negativa, UN estado', () => {
  it('el step-up de EVENTOS no pinta a la vez la ceremonia y una avería roja', async () => {
    // El caso exacto que el contraste describe: la ceremonia sale del agregado, y el bloque
    // de eventos —condicionado sólo por `!forbidden`— volvía a entrar y caía en ErrorState.
    api.nhiDetail.mockResolvedValue({
      identity_ref: 'nhi:1',
      kind: 'service',
      status: 'active',
    })
    api.nhiEvents.mockRejectedValue(stepUp())
    wrap(<NhiDetailSheet identityRef="nhi:1" open onOpenChange={() => {}} />)

    expect(await screen.findByText('step-up ceremony')).toBeInTheDocument()
    // Y NO hay avería roja acompañándola. Anclado a la ceremonia, que ya está en pantalla,
    // así que la ausencia no se cumple por llegar antes de tiempo.
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('el step-up del DETALLE no deja los eventos visibles debajo de la ceremonia', async () => {
    api.nhiDetail.mockRejectedValue(stepUp())
    api.nhiEvents.mockResolvedValue({
      items: [{ at: '2026-08-14T00:00:00Z', kind: 'rotated', actor: 'a' }],
    })
    wrap(<NhiDetailSheet identityRef="nhi:1" open onOpenChange={() => {}} />)

    expect(await screen.findByText('step-up ceremony')).toBeInTheDocument()
    await waitFor(() => expect(api.nhiEvents).toHaveBeenCalled())
    // Los eventos RESOLVIERON, así que si el bloque siguiera pintándose su fila estaría aquí.
    expect(screen.queryByText('rotated')).not.toBeInTheDocument()
  })

  it('y elevar REINTENTA las DOS lecturas que la hoja resume', async () => {
    // Esta hoja resume DOS consultas en un solo estado, así que la salida tiene que repetir
    // las dos: dejar una sin reintentar dejaría media hoja vacía después de la ceremonia.
    api.nhiDetail.mockRejectedValue(stepUp())
    api.nhiEvents.mockRejectedValue(stepUp())
    wrap(<NhiDetailSheet identityRef="nhi:1" open onOpenChange={() => {}} />)

    await screen.findByText('step-up ceremony')
    api.nhiDetail.mockResolvedValue({
      identity_ref: 'nhi:1',
      kind: 'service',
      status: 'active',
    })
    api.nhiEvents.mockResolvedValue({ items: [] })
    await userEvent.click(screen.getByRole('button', { name: /elevar/i }))

    await waitFor(() => expect(api.nhiDetail).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(api.nhiEvents).toHaveBeenCalledTimes(2))
  })

  it('un 403 SIN código de ceremonia conserva la negativa de ROL', async () => {
    // Control negativo: sin código, la negativa es de rol y se pinta como tal.
    api.nhiDetail.mockRejectedValue(new ApiError(403, 'forbidden', 'no'))
    api.nhiEvents.mockRejectedValue(new ApiError(403, 'forbidden', 'no'))
    wrap(<NhiDetailSheet identityRef="nhi:1" open onOpenChange={() => {}} />)

    expect(await screen.findByRole('status')).toBeInTheDocument()
    expect(screen.queryByText('step-up ceremony')).not.toBeInTheDocument()
  })
})
