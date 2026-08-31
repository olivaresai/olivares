// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repo root.
//
// ⛔ CONTRA EL COMPONENTE EXPORTADO, no a través de la vista, y por una razón medida en
// `#752`: allí DOS celdas mías pasaron con el defecto puesto —una la satisfacía la copy de
// la propia feature, la otra un componente vecino que también pinta la ceremonia— y el
// mutante sobrevivió las dos veces. Contra el componente no hay extraños que la satisfagan.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'

const api = vi.hoisted(() => ({ getLineage: vi.fn() }))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, knowledgeApi: { ...actual.knowledgeApi, ...api } }
})
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))
// ⛔ EL DOBLE TIENE QUE PODER LO QUE PRODUCCIÓN PUEDE. La primera versión era un componente
// SIN PROPS, así que `onElevated` —la única salida de la negativa— no se ejercía: el contraste
// `sol max` mutó el callsite productivo a `onElevated={undefined}` y las celdas siguieron
// verdes. Fijaban que aparece un marcador, no que hay salida. Ahora el doble respeta la firma
// real (assurance.tsx:83-98) y EXPONE el callback, para poder afirmar el reintento.
vi.mock('@/features/identity/assurance', () => ({
  StepUpPanel: ({
    action,
    onElevated,
  }: {
    minAal: number
    currentAal: number
    action: string
    className?: string
    onElevated?: () => void
  }) => (
    <div>
      <span>step-up ceremony</span>
      <span>{`action:${action}`}</span>
      <button type="button" onClick={() => onElevated?.()}>
        elevar
      </button>
    </div>
  ),
}))

import { LineageDetailSheet } from './lineage-detail'
import './i18n'

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  api.getLineage.mockReset()
})

describe('LineageDetailSheet — los dos 403 no son el mismo', () => {
  it('un step_up_required ofrece la CEREMONIA', async () => {
    api.getLineage.mockRejectedValue(
      new ApiError(403, 'step_up_required', 'assurance level too low'),
    )
    wrap(<LineageDetailSheet lineageId="lin-1" open onOpenChange={() => {}} />)
    expect(await screen.findByText('step-up ceremony')).toBeInTheDocument()

    // ⛔ Y LA SALIDA, que es lo que el marcador NO prueba: elevar reintenta la lectura
    // refusada. Sin esto, `onElevated={undefined}` en el callsite productivo dejaba la
    // celda verde — la ceremonia aparecía y no llevaba a ninguna parte.
    // La fixture es la REAL del feature (knowledge.test.tsx:97), no una inventada: la mía
    // ponía `{ kb_ref, items: [] }` y el componente reventaba leyendo `chunk_refs.length`.
    // Un doble que no puede lo que producción sí, no prueba lo que producción hace.
    api.getLineage.mockResolvedValue({
      id: 'ln1',
      kb_ref: 'kb1',
      agent_ref: 'agent:planner',
      query_hash: 'a1b2c3d4e5f6a7b8c9d0e1f2',
      chunk_refs: [],
      source_refs: [],
      residency_region: 'eu',
      decision: 'allowed',
      reason: '',
      egress: false,
      result_count: 0,
      occurred_at: '2026-06-04T10:00:00Z',
    })
    await userEvent.click(screen.getByRole('button', { name: /elevar/i }))
    await waitFor(() => expect(api.getLineage).toHaveBeenCalledTimes(2))
  })

  it('un 403 SIN código de ceremonia conserva la negativa de ROL', async () => {
    // Control negativo, anclado a lo POSITIVO: primero se espera a que la negativa de rol
    // esté en pantalla, y sólo entonces se afirma que la ceremonia no está. Una ausencia
    // afirmada sola se cumple en el primer tick, antes de que nada haya podido pintarse.
    api.getLineage.mockRejectedValue(new ApiError(403, 'forbidden', 'no'))
    wrap(<LineageDetailSheet lineageId="lin-1" open onOpenChange={() => {}} />)
    expect(await screen.findByRole('status')).toBeInTheDocument()
    expect(screen.queryByText('step-up ceremony')).not.toBeInTheDocument()
  })
})
