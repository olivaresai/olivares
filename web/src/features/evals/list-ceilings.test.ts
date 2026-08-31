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

const NO_PAGINAN: Record<string, string> = {
  scorecards:
    'handleListScorecards aggregates over listAll (modules/evals/scorecards.go)',
  runResults: 'handleListRunResults uses listAll (modules/evals/runs.go)',
  suiteCases: 'handleListSuiteCases uses listAll (modules/evals/suites.go)',
}

const VISTA = 'features/evals/evals-view.tsx'

describe('evals · paginated lists ask for a ceiling and declare truncation', () => {
  it('only the five paginated routes require a limit', () => {
    const { rutas, sinTecho } = rutasSinTecho(
      leer('features/evals/api.ts'),
      NO_PAGINAN,
    )
    expect(rutas).toHaveLength(8)
    expect(Object.keys(NO_PAGINAN).every((k) => rutas.includes(k))).toBe(true)
    expect(sinTecho).toEqual([])
  })

  it('all six live paginated queries have their own badge', () => {
    const listas = nombresDeLista(leer('features/evals/api.ts')).filter(
      (n) => !(n in NO_PAGINAN),
    )
    const { total, desajustes } = desajustesPorFichero(
      [{ nombre: VISTA, src: leer(VISTA) }],
      listas,
      'evalsApi',
    )
    expect(listas).toHaveLength(5)
    expect(total).toBe(6)
    expect(desajustes).toEqual([])
    expect(etiquetasConLiteral(leer(VISTA))).toEqual([])
  })
})
