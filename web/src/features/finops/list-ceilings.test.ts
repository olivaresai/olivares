// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import { FEATURE_EXTENSIONS } from '@/features/extensions'
import {
  avisosPorConsulta,
  etiquetasConLiteral,
  cuenta,
  leer,
  rutasSinTecho,
  nombresDeLista,
  pideTecho,
} from '@/test/list-ceiling-ratchet'

/** El trinquete vive en `@/test/list-ceiling-ratchet`; aquí sólo se aplica a finops. */

// The FinOps screen (`finops-view.tsx`) ships with the Business overlay. Where the overlay
// registers it these cases read it, and a missing file fails; the Community tree has no such
// screen, so they are reported skipped there. The API client cases below always run.
const businessView = it.skipIf(
  !FEATURE_EXTENSIONS.some((view) => view.id === 'finops'),
)

describe('finops · las listas piden su techo y declaran su recorte', () => {
  it('ninguna ruta de lista sale sin `limit`', () => {
    const { rutas, sinTecho } = rutasSinTecho(leer('features/finops/api.ts'))
    expect(rutas.length).toBeGreaterThanOrEqual(7)
    expect(sinTecho).toEqual([])
  })

  businessView(
    'toda consulta de lista tiene su aviso, atado a la MISMA consulta',
    () => {
      const listas = nombresDeLista(leer('features/finops/api.ts'))
      const { deLista, conAviso } = avisosPorConsulta(
        leer('features/finops/finops-view.tsx'),
        listas,
        'finopsApi',
      )
      expect(listas.length).toBeGreaterThanOrEqual(7)
      expect(deLista.length).toBeGreaterThanOrEqual(7)
      expect(conAviso.length).toBeGreaterThanOrEqual(7)
      expect(cuenta(conAviso)).toEqual(cuenta(deLista))
    },
  )

  it('CONTROL de la sonda — ve la que falta y no acusa a la que lo lleva', () => {
    const conTecho = `http.get<ListResponse<X>>(\`\${BASE}/x\`, { query: { limit: 1000 } })`
    const sinTecho = `http.get<ListResponse<X>>(\`\${BASE}/x\`, { query: { status } })`
    const anidada = `http.get<ListResponse<X>>(\`\${BASE}/x\`, { query: { limit: F(g(1)) } })`
    expect(pideTecho(conTecho, 0)).toBe(true)
    expect(pideTecho(sinTecho, 0)).toBe(false)
    expect(pideTecho(anidada, 0)).toBe(true)
    // ⛔ Y LOS DOS TECHOS FALSOS que un contraste construyó: `limit:` a secas los aceptaba.
    const indefinido = `http.get<ListResponse<X>>(\`\${BASE}/x\`, { query: { limit: undefined } })`
    const cero = `http.get<ListResponse<X>>(\`\${BASE}/x\`, { query: { limit: 0 } })`
    expect(pideTecho(indefinido, 0)).toBe(false)
    expect(pideTecho(cero, 0)).toBe(false)
  })

  businessView(
    'the notice number comes from the QUERY, not from a literal',
    () => {
      const malas = ['features/finops/finops-view.tsx'].flatMap((v) =>
        etiquetasConLiteral(leer(v)),
      )
      expect(malas).toEqual([])
    },
  )
})
