// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repo root.
//
// ⛔ EL 403 A PELO, QUE ES LA FORMA QUE NINGÚN BARRIDO DE `isForbidden` ENCUENTRA.
//
// `RunError` decidía su mensaje con `error.status === 403`, y `step_up_required` TAMBIÉN es un 403
// (`lib/api/errors.ts`). Resultado: una ceremonia se pintaba como «no tienes permiso» — se acusa al
// operador de carecer de algo que sí tiene, y no se le ofrece la salida.
//
// Este fichero sobrevivió a toda la campaña de la ceremonia porque los barridos buscaban
// `isForbidden`, y aquí no aparece esa palabra por ningún lado. La celda mide el COMPORTAMIENTO,
// que es lo único que no depende de cómo esté escrita la condición.
import { render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

import { ApiError } from '@/lib/api/errors'

// El doble de i18n devuelve la CLAVE, así que la aserción distingue ramas sin depender de la copy.
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}))

import { RunError } from './run-panel'

const ceremonia = () =>
  new ApiError(403, 'step_up_required', 'assurance level too low')
const rol = () => new ApiError(403, 'forbidden', 'your role cannot run this')

describe('RunError distingue los DOS 403', () => {
  it('con `step_up_required` ofrece la ceremonia, no la acusación', () => {
    render(<RunError error={ceremonia()} />)
    expect(screen.getByRole('alert')).toHaveTextContent(
      'common:privileged.stepUp.title',
    )
    // ⛔ La exclusión importa tanto como la presencia: enseñar las dos cosas a la vez es el defecto
    //    que esta campaña vino a quitar.
    expect(screen.queryByText('run.forbidden')).toBeNull()
  })

  it('y con un 403 de ROL sigue acusando — no he roto el otro camino', () => {
    // CONTROL NEGATIVO: sin él, «poner la ceremonia delante» se cumpliría borrando la rama de rol,
    // y la pantalla mentiría cuando el operador SÍ carece del permiso.
    render(<RunError error={rol()} />)
    expect(screen.getByRole('alert')).toHaveTextContent('run.forbidden')
  })

  it('y los otros códigos no se tocan', () => {
    // Tercer camino: sin esto, «reconoce la ceremonia» se cumpliría rompiendo 423 y 409.
    render(<RunError error={new ApiError(423, 'locked', 'x')} />)
    expect(screen.getByRole('alert')).toHaveTextContent('run.locked')
  })
})
