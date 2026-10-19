// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import { FEATURE_EXTENSIONS } from '@/features/extensions'
import {
  mismatchesByFile,
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

// The red-team screen (`redteam-view.tsx`) ships with the Business overlay. Where the overlay
// registers it these cases read it, and a missing file fails; the Community tree has no such
// screen, so they are reported skipped there. The API client case always runs.
const businessView = it.skipIf(
  !FEATURE_EXTENSIONS.some((view) => view.id === 'redteam'),
)

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

  businessView('every paginated query has its notice, in the SAME file', () => {
    const listas = nombresDeLista(leer('features/redteam/api.ts')).filter(
      (n) => !(n in NO_PAGINAN),
    )
    const { total, desajustes } = mismatchesByFile(
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

  businessView(
    'the notice number comes from the QUERY, not from a literal',
    () => {
      const malas = ['features/redteam/redteam-view.tsx'].flatMap((v) =>
        etiquetasConLiteral(leer(v)),
      )
      expect(malas).toEqual([])
    },
  )
})
