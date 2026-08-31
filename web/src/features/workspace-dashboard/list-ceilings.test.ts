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

const VISTA = 'features/workspace-dashboard/workspace-dashboard-view.tsx'

describe('workspace dashboard · both paginated lists declare truncation', () => {
  it('both routes retain an explicit positive ceiling', () => {
    const { rutas, sinTecho } = rutasSinTecho(
      leer('features/workspace-dashboard/api.ts'),
    )
    expect(rutas).toEqual(['agents', 'groups'])
    expect(sinTecho).toEqual([])
  })

  it('both live queries have their own badge', () => {
    const listas = nombresDeLista(leer('features/workspace-dashboard/api.ts'))
    const { total, desajustes } = desajustesPorFichero(
      [{ nombre: VISTA, src: leer(VISTA) }],
      listas,
      'workspaceDashboardApi',
    )
    expect(total).toBe(2)
    expect(desajustes).toEqual([])
    expect(etiquetasConLiteral(leer(VISTA))).toEqual([])
  })
})
