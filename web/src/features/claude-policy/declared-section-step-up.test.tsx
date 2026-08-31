// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repo root.
//
// ⛔ ESTA CELDA PRUEBA EL ENVOLTORIO DIRECTAMENTE, Y NO ES PEREZA: ES LA ÚNICA FORMA DE QUE
// PRUEBE ALGO. Dos intentos previos a través de `ClaudePolicyView` pasaron con el defecto
// PUESTO, cada uno satisfecho por un extraño distinto:
//   1. una aserción por /verification/i la casaba la copy de la propia feature
//      («Publish + drift verification», i18n/en.json:42)
//   2. y con un marcador único para la ceremonia, el marcador lo pintaba OTRO componente
//      de la vista, no esta rama
// Las dos veces el mutante —quitar la rama de aseguramiento— sobrevivió. Contra el
// componente exportado no hay sitio donde esconderse: lo que aparece sale de él.
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'
import { DeclaredSection } from './components'
import './i18n'

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

// `UseQueryResult['error']` es `Error | null` en este cliente, así que el doble se tipa
// con Error y no con `unknown`: un doble que no encaja con la firma real no prueba lo que
// producción hace (memoria propia: «verde porque el DOBLE no puede lo que producción sí»).
function fakeQuery(error: Error) {
  return {
    data: undefined,
    isLoading: false,
    isError: true as const,
    error,
    refetch: vi.fn(),
  }
}

describe('DeclaredSection — los dos 403 no son el mismo', () => {
  it('un step_up_required ofrece la CEREMONIA', async () => {
    const q = fakeQuery(
      new ApiError(403, 'step_up_required', 'assurance level too low'),
    )
    render(
      <DeclaredSection query={q} what="drift">
        {() => null}
      </DeclaredSection>,
    )
    expect(await screen.findByText('step-up ceremony')).toBeInTheDocument()

    // ⛔ Y LA SALIDA. El marcador sólo prueba que la ceremonia aparece; el contraste mutó el
    // callsite productivo a `onElevated={undefined}` y esta celda siguió verde. Elevar tiene
    // que reintentar la lectura refusada, que es la única salida de la negativa.
    await userEvent.click(screen.getByRole('button', { name: /elevar/i }))
    expect(q.refetch).toHaveBeenCalledTimes(1)
  })

  it('un 403 SIN código de ceremonia conserva la negativa de ROL', () => {
    // Control negativo: la negativa de rol es cierta y no se toca. Anclado a lo positivo
    // —el estado de rol SE PINTA— antes de afirmar la ausencia de la ceremonia.
    render(
      <DeclaredSection
        query={fakeQuery(new ApiError(403, 'forbidden', 'no'))}
        what="drift"
      >
        {() => null}
      </DeclaredSection>,
    )
    expect(screen.getByRole('status')).toBeInTheDocument()
    expect(screen.queryByText('step-up ceremony')).not.toBeInTheDocument()
  })
})
