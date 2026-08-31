// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ⛔ EL IDENTIFICADOR QUE HACÍA FALTA PARA CASAR PANTALLA Y SERVIDOR. `lib/api/client.ts` captura
// el `X-Request-ID` de CADA respuesta fallida y lo guarda en el `ApiError` — y hasta este cambio
// no lo leía NADIE en toda la consola. El operador veía un error genérico, el motor tenía la línea
// con la causa, y no había forma de casar las dos. Es el mismo `request_id` del log de peticiones.
//
// Estas celdas fijan las DOS mitades, porque sólo la primera no basta:
//   · si el error TRAE id, la pantalla lo enseña;
//   · si NO lo trae, no se pinta un hueco — «id: —» es peor que no decir nada.
import { describe, expect, it, vi } from 'vitest'
import { renderIntel, screen } from '@/test/intel'
import { AsyncSection } from './async'
import { ApiError, NetworkError } from '@/lib/api/errors'
import '@/features/_intel'

function failing(error: Error) {
  return {
    data: undefined,
    isLoading: false,
    isError: true as const,
    error,
    refetch: vi.fn(),
  }
}

describe('AsyncSection — el request id llega a la pantalla', () => {
  it('lo ENSEÑA cuando el error lo trae', async () => {
    renderIntel(
      <AsyncSection
        query={failing(
          new ApiError(500, 'internal_error', 'boom', 'req-7f3a91'),
        )}
      >
        {() => <div>nunca</div>}
      </AsyncSection>,
    )
    expect(await screen.findByText('req-7f3a91')).toBeVisible()
  })

  it('NO pinta un hueco cuando el error no lo trae', async () => {
    renderIntel(
      <AsyncSection
        query={failing(new ApiError(500, 'internal_error', 'boom'))}
      >
        {() => <div>nunca</div>}
      </AsyncSection>,
    )
    await screen.findByRole('alert')
    // el rótulo sólo existe si hay id que enseñar
    expect(screen.queryByText(/Request ID|ID de petición/i)).toBeNull()
  })

  it('un error de red tampoco inventa un id', async () => {
    renderIntel(
      <AsyncSection query={failing(new NetworkError('sin red'))}>
        {() => <div>nunca</div>}
      </AsyncSection>,
    )
    await screen.findByRole('alert')
    expect(screen.queryByText(/Request ID|ID de petición/i)).toBeNull()
  })
})
