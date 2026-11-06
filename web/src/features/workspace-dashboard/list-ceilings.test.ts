// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import {
  avisosPorConsulta,
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

  // A capped preview ends with a Show more row, never a count of the rows it loaded.
  it('both live queries end with their own Show more row', () => {
    const listas = nombresDeLista(leer('features/workspace-dashboard/api.ts'))
    const src = leer(VISTA)
    const { deLista } = avisosPorConsulta(src, listas, 'workspaceDashboardApi')
    const filas = [
      ...src.matchAll(/<ShowMore[\s\S]{0,80}?query=\{(\w+)\}/g),
    ].map((m) => m[1])
    expect(deLista).toHaveLength(2)
    expect([...filas].sort()).toEqual([...deLista].sort())
  })
})
