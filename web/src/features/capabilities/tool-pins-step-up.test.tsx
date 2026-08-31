// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repo root.
//
// ⛔ ESTA CELDA EXISTE PORQUE UN MUTANTE SOBREVIVIÓ AL BARRIDO TEXTUAL, y lo reproduje contra el
// árbol antes de escribirla en vez de fiarme de la teoría.
//
// `step-up-detail-sheets.test.ts` comprueba nombres, orden, JSX y `onElevated` LEYENDO EL
// FICHERO. Cambiar la condición por
//
//     (query.error.isStepUpRequired && false) ? (
//
// conserva los cuatro rasgos —el nombre sigue ahí, sigue antes que `isForbidden`, el JSX y el
// `onElevated` siguen escritos— y las cuatro celdas siguen VERDES. En ejecución, en cambio, la
// ceremonia se vuelve inalcanzable y el mismo error cae en la rama de rol, porque un
// `step_up_required` satisface también `isForbidden` (`lib/api/errors.ts:59-61`).
//
// Un texto no ve control de flujo, y eso no se arregla endureciendo el texto: se arregla
// EJECUTANDO. Ésta es la celda que ejecuta, y con ella el mutante muere.
//
// Se conduce `tool-pins` por ser la única de las seis con la decisión en forma de `if` con
// retorno —las otras cinco son brazos de ternario—, así que cubre la forma que el barrido peor
// distingue. Las otras cinco siguen amparadas por el barrido, y ese techo está declarado allí.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@/lib/api/errors'

vi.mock('@/components/layout/step-up-state', () => ({
  StepUpRequiredState: ({ action }: { action: string }) => (
    <div data-testid="ceremonia">{`ceremonia:${action}`}</div>
  ),
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))
vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
  Toaster: () => null,
}))

const api = vi.hoisted(() => ({ listToolPins: vi.fn() }))
vi.mock('./api', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return {
    ...real,
    capabilitiesApi: { ...(real.capabilitiesApi as object), ...api },
  }
})

import { ToolPinsTab } from './tool-pins'

const ceremonia = () =>
  new ApiError(403, 'step_up_required', 'assurance level too low')
const rol = () => new ApiError(403, 'forbidden', 'your role cannot read this')

const ACUSACION = /not authorized|forbidden/i

// ⛔ EL PLAZO NO ES DECORATIVO. `ToolPinsTab` define su PROPIO `retry` (`tool-pins.tsx:118-119`:
// dos intentos salvo enterprise-pending), que ANULA el `retry: false` del cliente de prueba. Con
// el plazo por defecto de `findBy*` la celda expiraba antes de que el error se asentara y fallaba
// por impaciencia, no por el invariante. Se espera lo que el sujeto tarda de verdad en vez de
// cambiar el sujeto para que quepa en la prueba.
const PLAZO = { timeout: 5000 }

const wrap = (ui: ReactNode) =>
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      {ui}
    </QueryClientProvider>,
  )

describe('tool-pins ofrece la ceremonia cuando el motor la pide', () => {
  beforeEach(() => vi.clearAllMocks())

  it('con `step_up_required` pinta la ceremonia y NO la acusación', async () => {
    api.listToolPins.mockRejectedValue(ceremonia())
    wrap(<ToolPinsTab canWrite />)

    expect(
      await screen.findByTestId('ceremonia', {}, PLAZO),
    ).toBeInTheDocument()
    // La exclusión importa tanto como la presencia: enseñar las dos cosas a la vez es el defecto
    // que esta campaña vino a quitar.
    expect(screen.queryByText(ACUSACION)).toBeNull()
  })

  it('y con un 403 de ROL sigue acusando — no he roto el otro camino', async () => {
    // ⛔ CONTROL NEGATIVO: sin él, «poner la ceremonia delante» se cumpliría igual borrando la
    //    rama de rol, y la pantalla mentiría cuando el operador SÍ carece del permiso.
    api.listToolPins.mockRejectedValue(rol())
    wrap(<ToolPinsTab canWrite />)

    expect(await screen.findByText(ACUSACION, {}, PLAZO)).toBeInTheDocument()
    expect(screen.queryByTestId('ceremonia')).toBeNull()
  })

  it('y un error que no es 403 sigue siendo un error', async () => {
    // El tercer camino: «no acuses nunca» sería un arreglo que borra la señal de un fallo real.
    api.listToolPins.mockRejectedValue(new ApiError(500, 'internal', 'boom'))
    wrap(<ToolPinsTab canWrite />)

    expect(
      await screen.findByRole('button', { name: /retry|reintentar/i }, PLAZO),
    ).toBeInTheDocument()
    expect(screen.queryByTestId('ceremonia')).toBeNull()
  })
})
