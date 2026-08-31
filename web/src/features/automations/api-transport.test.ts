// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
import { readFileSync } from 'node:fs'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { automationsApi, EVIDENCE_PAGE } from './api'

/**
 * ⛔ ESTE TESTIGO MIRA LA URL, y existe porque la otra mitad no puede.
 *
 * El test de vista mockea el cliente entero, así que vería pasar por bueno un método que ACEPTA el
 * techo y lo TIRA. Aquí la pantalla enseña RECUENTOS —«3 activos», «2 parados»— derivados de
 * `items.length`: si el motor recortó, esa cifra no es una lista incompleta, es un número falso.
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

afterEach(() => {
  urls = []
})

/**
 * Las que PAGINAN, medidas en el motor y no supuestas: sus tres handlers llaman `listQuery(r)` y
 * publican `has_more` — `modules/orchestration/schedules.go:343`,
 * `modules/eventing/subscription.go:269` y `modules/notify/route.go:141`.
 */
const PAGINAN: Array<[string, () => Promise<unknown>]> = [
  ['schedules', () => automationsApi.schedules({ limit: EVIDENCE_PAGE })],
  [
    'subscriptions',
    () => automationsApi.subscriptions({ limit: EVIDENCE_PAGE }),
  ],
  ['routes', () => automationsApi.routes({ limit: EVIDENCE_PAGE })],
]

/**
 * Las que NO paginan: devuelven un catálogo fijo (`modules/eventing/eventing.go:483` y
 * `modules/notify/evaluate.go:82` escriben un mapa, sin `listQuery` y sin `has_more`), así que un
 * `limit` sería decorativo. Esta celda fija la decisión: si alguien se lo añade, se pone rojo y
 * hay que releer por qué no lo lleva.
 */
const NO_PAGINAN: Array<[string, () => Promise<unknown>]> = [
  ['eventTypes', () => automationsApi.eventTypes()],
  ['matchTypes', () => automationsApi.matchTypes()],
]

describe('los tres raíles de automations piden su techo', () => {
  it.each(PAGINAN)(
    `%s pide limit=${EVIDENCE_PAGE} explícito`,
    async (_n, llamada) => {
      capturaFetch()
      await llamada()
      expect(urls).toHaveLength(1)
      expect(urls[0]).toContain(`limit=${EVIDENCE_PAGE}`)
    },
  )

  it.each(NO_PAGINAN)(
    '%s NO manda limit (sería decorativo)',
    async (_n, llamada) => {
      capturaFetch()
      await llamada()
      expect(urls).toHaveLength(1)
      expect(urls[0]).not.toContain('limit=')
    },
  )

  it('sin argumentos, el techo VIAJA IGUAL (F-03: la invariante es de la API)', async () => {
    capturaFetch()
    await automationsApi.routes()
    expect(urls[0]).toContain(`limit=${EVIDENCE_PAGE}`)
  })

  it('un llamante que pida otro techo GANA, que es el orden de la receta', async () => {
    capturaFetch()
    await automationsApi.routes({ limit: 7 })
    expect(urls[0]).toContain('limit=7')
    expect(urls[0]).not.toContain(`limit=${EVIDENCE_PAGE}`)
  })

  it('el techo es el máximo que el motor acepta, LEÍDO DEL MOTOR', () => {
    // ⛔ F-04 del contraste: la versión anterior era `expect(EVIDENCE_PAGE).toBe(1000)`. Eso fija
    // un literal de TypeScript contra otro literal de TypeScript y NO observa el motor: si mañana
    // `maxLimit` sube a 2000, esta celda sigue verde y la consola pide la mitad de lo que podría,
    // en silencio. Aquí se lee la constante de Go y se compara con la nuestra.
    // Ruta relativa al cwd de vitest, que es `web/`. `import.meta.url` no sirve: bajo la
    // transformación de vitest no es un URL de esquema `file:` y `readFileSync` lo rechaza.
    const go = readFileSync(
      '../core/internal/store/sqlstore/generic.go',
      'utf8',
    )
    const m = go.match(/maxLimit\s*=\s*(\d+)/)
    // Si el nombre cambia, esto es «no he podido mirar», no un verde.
    expect(
      m,
      'no encuentro maxLimit en generic.go — la sonda dejó de ver el motor',
    ).not.toBeNull()
    expect(EVIDENCE_PAGE).toBe(Number(m![1]))
  })
})
