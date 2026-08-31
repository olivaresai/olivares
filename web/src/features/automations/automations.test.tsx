// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repo root.
//
// ⛔ ESTA PANTALLA NO TENÍA NI UNA CELDA, y el defecto que se arregla aquí es de los que
// sólo se ven leyéndola: la tarjeta de raíl afirmaba EN PROSA «no tienes autorización para
// ver este raíl» ante un 403 de CEREMONIA, en la pestaña de aterrizaje y junto a dos
// tarjetas sanas. `ApiError.isForbidden` es SÓLO el status (lib/api/errors.ts:59) y
// `isStepUpRequired` es el código (:77): un step-up satisface los dos, así que ramificar
// por el status primero acusa al operador de no tener un permiso que tiene.
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { ApiError } from '@/lib/api/errors'
import { useStepUpStore } from '@/stores/step-up'

const api = vi.hoisted(() => ({
  schedules: vi.fn(),
  subscriptions: vi.fn(),
  routes: vi.fn(),
  eventTypes: vi.fn(),
  matchTypes: vi.fn(),
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, automationsApi: api }
})

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))

// El enrutador no es el sujeto: el `Link` de la tarjeta sólo tiene que renderizar.
vi.mock('@tanstack/react-router', () => ({
  Link: ({ children }: { children?: ReactNode }) => <a href="#">{children}</a>,
}))

import { AutomationsView } from './automations-view'
import './i18n'

function wrap(ui: ReactNode) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

const EMPTY = { items: [] }

beforeEach(() => {
  useStepUpStore.setState({ request: null })
  for (const fn of Object.values(api)) fn.mockReset()
  api.schedules.mockResolvedValue(EMPTY)
  api.subscriptions.mockResolvedValue(EMPTY)
  api.routes.mockResolvedValue(EMPTY)
  api.eventTypes.mockResolvedValue({ event_types: [] })
  api.matchTypes.mockResolvedValue({ match_types: [] })
})

describe('AutomationsView — el 403 de ceremonia en la tarjeta de raíl', () => {
  it('ofrece la ceremonia cuando el raíl se niega por ASEGURAMIENTO', async () => {
    api.schedules.mockRejectedValue(
      new ApiError(403, 'step_up_required', 'assurance level too low'),
    )
    wrap(<AutomationsView />)

    // Ancla POSITIVA primero: la ceremonia está en pantalla. Una aserción de ausencia
    // sola se cumpliría en el primer tick, antes de que nada pudiera pintarse, y la
    // celda pasaría con el defecto puesto.
    expect(
      await screen.findByText(/step-up|verification|verificación/i),
    ).toBeInTheDocument()
    // Y la frase falsa NO acompaña a la ceremonia.
    expect(screen.queryByText(/not authorized to view this rail/i)).toBeNull()
  })

  it('conserva la negativa de ROL cuando el 403 no trae código de ceremonia', async () => {
    // Control negativo: sin código, la negativa es de rol, es cierta, y se queda.
    api.schedules.mockRejectedValue(new ApiError(403, 'forbidden', 'no'))
    wrap(<AutomationsView />)

    expect(
      await screen.findByText(/not authorized to view this rail/i),
    ).toBeInTheDocument()
    expect(useStepUpStore.getState().request).toBeNull()
  })

  it('el catálogo de disparadores tampoco confunde la ceremonia con una avería', async () => {
    // Este sitio NO estaba en el censo de `isForbidden` porque no tenía ninguno: mandaba
    // cualquier 403 al mismo «no se pudo cargar». Un barrido por token no ve un sitio
    // que no distingue NADA.
    api.eventTypes.mockRejectedValue(
      new ApiError(403, 'step_up_required', 'assurance level too low'),
    )
    wrap(<AutomationsView />)

    await waitFor(() => expect(api.eventTypes).toHaveBeenCalled())
    expect(
      await screen.findByText(/step-up|verification|verificación/i),
    ).toBeInTheDocument()
  })
})
