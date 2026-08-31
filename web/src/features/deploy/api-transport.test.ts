// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
import { readFileSync } from 'node:fs'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { deployApi, EVIDENCE_PAGE } from './api'

/**
 * ⛔ ESTE TESTIGO MIRA LA URL. El de vista mockea el cliente entero, así que daría por bueno un
 * método que ACEPTA el techo y lo TIRA.
 */

let urls: string[] = []

function capturaFetch(): void {
  globalThis.fetch = vi.fn(async (url: string) => {
    urls.push(String(url))
    return new Response(JSON.stringify({ items: [], has_more: false }), {
      status: 200,
      headers: new Headers({ 'Content-Type': 'application/json' }),
    })
  }) as never
}

/**
 * El valor EXACTO de `limit`. ⛔ Antes esto se comprobaba con `toContain('limit=1')`, y el
 * contraste lo cazó: **esa forma también acepta `limit=10`** — comprobado con node. Una
 * aserción de subcadena sobre un número es un colador.
 */
function limitDe(u: string): string | null {
  return new URL(u, 'https://test.local').searchParams.get('limit')
}

afterEach(() => {
  urls = []
})

/** Las TRES que paginan: sus handlers llaman `listQuery(r)` y publican `has_more`. */
const PAGINAN: Array<[string, () => Promise<unknown>]> = [
  ['listDefinitions', () => deployApi.listDefinitions()],
  ['listWirings', () => deployApi.listWirings()],
  ['listOperations', () => deployApi.listOperations()],
]

describe('las tres listas de deploy piden su techo', () => {
  it.each(PAGINAN)(
    '%s manda el techo sin que el llamante lo pase',
    async (_n, llamada) => {
      capturaFetch()
      await llamada()
      expect(urls).toHaveLength(1)
      expect(limitDe(urls[0])).toBe(String(EVIDENCE_PAGE))
    },
  )

  /**
   * ⛔ LA CELDA QUE PROTEGE UN LLAMANTE REAL. `definition-detail.tsx:123` pide
   * `listOperations({ definition_id, limit: 1 })` para traer SOLO la última operación. Si el techo
   * pisara al llamante, esa pantalla se traería mil filas para enseñar una.
   */
  it('un `limit` del llamante GANA — y hay una pantalla que depende de ello', async () => {
    capturaFetch()
    await deployApi.listOperations({ definition_id: 'def-1', limit: 1 })
    expect(limitDe(urls[0])).toBe('1')
  })

  it('un `limit: undefined` explícito NO pisa el techo', async () => {
    capturaFetch()
    await deployApi.listWirings({ limit: undefined })
    expect(limitDe(urls[0])).toBe(String(EVIDENCE_PAGE))
  })

  /**
   * ⚠ `listRevisions` NO manda techo: su handler drena con `listAll`
   * (`modules/deploy/definitions.go:437`). Esta celda fija «no mandes un parámetro que el handler
   * ignora», NO «la carga es completa» — leer el handler aislado no prueba que `listAll` sea
   * ilimitado por dentro.
   */
  it('listRevisions NO manda limit: su handler drena', async () => {
    capturaFetch()
    await deployApi.listRevisions('def-1')
    expect(urls).toHaveLength(1)
    expect(limitDe(urls[0])).toBeNull()
  })

  it('el techo es el máximo que el motor acepta, LEÍDO DEL MOTOR', () => {
    const go = readFileSync(
      '../core/internal/store/sqlstore/generic.go',
      'utf8',
    )
    // ⛔ ANCLADO a la DECLARACION. El contraste avisó de que un `maxLimit` suelto puede casar
    // antes un comentario o una cadena; con `^…$` sólo casa la línea de la constante.
    const m = go.match(/^\s*maxLimit\s*=\s*(\d+)\s*$/m)
    expect(
      m,
      'no encuentro maxLimit en generic.go — la sonda dejó de ver el motor',
    ).not.toBeNull()
    expect(EVIDENCE_PAGE).toBe(Number(m![1]))
  })
})
