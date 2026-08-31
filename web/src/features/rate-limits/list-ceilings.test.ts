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

const VISTA = 'features/rate-limits/rate-limits-view.tsx'

describe('rate limits · the findings page declares truncation', () => {
  it('the findings route asks for an explicit ceiling', () => {
    const { rutas, sinTecho } = rutasSinTecho(
      leer('features/rate-limits/api.ts'),
    )
    expect(rutas).toEqual(['findings'])
    expect(sinTecho).toEqual([])
  })

  it('the live findings query has its badge', () => {
    const listas = nombresDeLista(leer('features/rate-limits/api.ts'))
    const { total, desajustes } = desajustesPorFichero(
      [{ nombre: VISTA, src: leer(VISTA) }],
      listas,
      'rateLimitsApi',
    )
    expect(total).toBe(1)
    expect(desajustes).toEqual([])
    expect(etiquetasConLiteral(leer(VISTA))).toEqual([])
  })
})
