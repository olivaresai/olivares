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
  outputs: 'loadRunOutputs uses listAll (modules/sandbox/runs.go)',
}
const VISTA = 'features/sandbox/sandbox-view.tsx'

describe('sandbox · paginated lists ask for a ceiling and declare truncation', () => {
  it('the three paginated routes have limits and outputs stays complete', () => {
    const { rutas, sinTecho } = rutasSinTecho(
      leer('features/sandbox/api.ts'),
      NO_PAGINAN,
    )
    expect(rutas).toHaveLength(4)
    expect(rutas).toContain('outputs')
    expect(sinTecho).toEqual([])
  })

  it('all three live paginated queries have their own badge', () => {
    const listas = nombresDeLista(leer('features/sandbox/api.ts')).filter(
      (n) => !(n in NO_PAGINAN),
    )
    const { total, desajustes } = desajustesPorFichero(
      [{ nombre: VISTA, src: leer(VISTA) }],
      listas,
      'sandboxApi',
    )
    expect(listas).toHaveLength(3)
    expect(total).toBe(3)
    expect(desajustes).toEqual([])
    expect(etiquetasConLiteral(leer(VISTA))).toEqual([])
  })
})
