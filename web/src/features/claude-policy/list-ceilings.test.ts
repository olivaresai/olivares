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
 * ⛔ TRES DE LAS CINCO LISTAS DE ESTE CLIENTE NO PAGINAN, y la razón va aquí con el fichero del
 *    motor que la demuestra. Empecé poniéndoles techo y aviso a las cinco; el contraste lo refutó
 *    y lo comprobé: sus handlers devuelven el conjunto COMPLETO, así que el `limit` era decorativo
 *    y el aviso, inalcanzable. Se excluyen a propósito, no por olvido.
 */
const NO_PAGINAN: Record<string, string> = {
  listVersions:
    '`handleListVersions` no mira la query y llama a `listRevisions`, que «drains every page ' +
    'so the version history is complete» (modules/governance/revision.go)',
  pdpVersions:
    'reutiliza `listRevisions` para Cedar y luego OPA (modules/governance/pdp_authoring.go)',
  threadEvents:
    '`handleThreadEvents` no mira la query (modules/governance/claudeagents.go). ⚠ El recorte ' +
    'REAL está en el conector, que corta en `max_pages` y devuelve has_more:false — reportado',
}

const VISTAS = [
  'features/claude-policy/drift.tsx',
  'features/claude-policy/managed-agents-hitl.tsx',
  'features/claude-policy/cedar-opa-view.tsx',
  'features/claude-policy/policy-authoring-panel.tsx',
]

describe('claude-policy · las listas que PAGINAN piden su techo y declaran su recorte', () => {
  it('ninguna ruta paginada sale sin `limit`', () => {
    const { rutas, sinTecho } = rutasSinTecho(
      leer('features/claude-policy/api.ts'),
      NO_PAGINAN,
    )
    expect(rutas.length).toBeGreaterThanOrEqual(5)
    expect(Object.keys(NO_PAGINAN).every((k) => rutas.includes(k))).toBe(true)
    expect(sinTecho).toEqual([])
  })

  it('toda consulta paginada tiene su aviso, en SU MISMO fichero', () => {
    // ⛔ POR FICHERO, no sobre la concatenación: el contraste construyó el escape compuesto
    //    —borrar un aviso en un fichero y duplicar otro con el mismo nombre de variable en otro—
    //    que el multiconjunto global no distingue. Comparar por fichero lo cierra.
    const listas = nombresDeLista(leer('features/claude-policy/api.ts')).filter(
      (n) => !(n in NO_PAGINAN),
    )
    const { total, desajustes } = desajustesPorFichero(
      VISTAS.map((v) => ({ nombre: v, src: leer(v) })),
      listas,
      'claudePolicyApi',
    )
    expect(listas.length).toBe(2)
    expect(total).toBeGreaterThanOrEqual(2)
    expect(desajustes).toEqual([])
  })

  it('el número del aviso sale de la CONSULTA, no de un literal', () => {
    const malas = [
      'features/claude-policy/drift.tsx',
      'features/claude-policy/managed-agents-hitl.tsx',
    ].flatMap((v) => etiquetasConLiteral(leer(v)))
    expect(malas).toEqual([])
  })
})
