// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import {
  desajustesPorFichero,
  etiquetasConLiteral,
  leer,
  nombresDeLista,
  rutasSinTecho,
} from '@/test/list-ceiling-ratchet'

/**
 * Check the engine before writing the client, following lesson.
 * `handleListTargets` and `handleListRuns` use `listQuery(r)`; `handleListResults` uses
 * `listAll` and does not set `HasMore`. That is why there are two ceilings, not three.
 */
const NO_PAGINAN: Record<string, string> = {
  results:
    '`handleListResults` usa `listAll` y devuelve todos los resultados de la ejecución, sin ' +
    'poner `HasMore` (modules/redteam/scorecard.go)',
}

describe('redteam · las listas que paginan piden su techo y declaran su recorte', () => {
  it('ninguna ruta paginada sale sin `limit`', () => {
    const { rutas, sinTecho } = rutasSinTecho(
      leer('features/redteam/api.ts'),
      NO_PAGINAN,
    )
    expect(rutas.length).toBeGreaterThanOrEqual(3)
    expect(Object.keys(NO_PAGINAN).every((k) => rutas.includes(k))).toBe(true)
    expect(sinTecho).toEqual([])
  })

  it('toda consulta paginada tiene su aviso, en SU MISMO fichero', () => {
    const listas = nombresDeLista(leer('features/redteam/api.ts')).filter(
      (n) => !(n in NO_PAGINAN),
    )
    const { total, desajustes } = desajustesPorFichero(
      [
        {
          nombre: 'redteam-view.tsx',
          src: leer('features/redteam/redteam-view.tsx'),
        },
      ],
      listas,
      'redteamApi',
    )
    expect(listas.length).toBe(2)
    expect(total).toBeGreaterThanOrEqual(2)
    expect(desajustes).toEqual([])
  })

  it('el número del aviso sale de la CONSULTA, no de un literal', () => {
    const malas = ['features/redteam/redteam-view.tsx'].flatMap((v) =>
      etiquetasConLiteral(leer(v)),
    )
    expect(malas).toEqual([])
  })
})
