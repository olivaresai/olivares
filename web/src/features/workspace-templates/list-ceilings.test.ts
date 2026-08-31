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

const VISTAS = [
  'features/workspace-templates/templates-view.tsx',
  'features/agentops/run-create-dialog.tsx',
]

describe('workspace templates · both live consumers declare truncation', () => {
  it('the templates route asks for an explicit ceiling', () => {
    const { rutas, sinTecho } = rutasSinTecho(
      leer('features/workspace-templates/api.ts'),
    )
    expect(rutas).toEqual(['list'])
    expect(sinTecho).toEqual([])
  })

  it('the catalog and launch picker each have their own badge', () => {
    const listas = nombresDeLista(leer('features/workspace-templates/api.ts'))
    const vistas = VISTAS.map((nombre) => ({ nombre, src: leer(nombre) }))
    const { total, desajustes } = desajustesPorFichero(
      vistas,
      listas,
      'templatesApi',
    )
    expect(total).toBe(2)
    expect(desajustes).toEqual([])
    expect(VISTAS.flatMap((v) => etiquetasConLiteral(leer(v)))).toEqual([])
  })
})
