// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
import { readFileSync } from 'node:fs'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { capabilitiesApi, EVIDENCE_PAGE } from './api'

/**
 * ⛔ ESTE TESTIGO MIRA LA URL, y compara el VALOR del parámetro, no una subcadena.
 *
 * El contraste de lo señaló en mi propio testigo: `toContain('limit=1')` **también acepta
 * `limit=10`** — comprobado con node. Una aserción de subcadena sobre un número es un colador.
 * Aquí se lee con `URLSearchParams` y se compara el valor exacto.
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

/** El valor EXACTO de `limit` en la última URL, o null si no viaja. */
function limitDe(u: string): string | null {
  return new URL(u, 'https://test.local').searchParams.get('limit')
}

afterEach(() => {
  urls = []
})

/** Las CUATRO que paginan: sus handlers llaman `listQuery(r)` y publican `has_more`. */
const PAGINAN: Array<[string, () => Promise<unknown>]> = [
  ['listServers', () => capabilitiesApi.listServers()],
  ['listSkills', () => capabilitiesApi.listSkills()],
  ['listTools', () => capabilitiesApi.listTools()],
  ['listConfigs', () => capabilitiesApi.listConfigs()],
]

/**
 * Las que NO llevan techo, cada una por su razón medida en el motor. Esta celda fija la decisión
 * en la dirección contraria: si alguien les añade un `limit`, se pone roja.
 *   · wiring        — devuelve un grafo, no `ListResponse`
 *   · listToolPins  — su handler escribe un mapa fijo (`toolpins.go:75`)
 *   · listRevisions — su handler fija `Limit: listCap` e IGNORA el del llamante (`config.go:401`)
 */
const SIN_TECHO: Array<[string, () => Promise<unknown>]> = [
  ['wiring', () => capabilitiesApi.wiring()],
  ['listRevisions', () => capabilitiesApi.listRevisions('cfg-1')],
]

describe('las cuatro listas de capabilities piden su techo', () => {
  it.each(PAGINAN)(
    '%s manda el techo sin que el llamante lo pase',
    async (_n, llamada) => {
      capturaFetch()
      await llamada()
      expect(urls).toHaveLength(1)
      expect(limitDe(urls[0])).toBe(String(EVIDENCE_PAGE))
    },
  )

  it.each(SIN_TECHO)(
    '%s NO manda limit: su handler no lo lee',
    async (_n, llamada) => {
      capturaFetch()
      await llamada()
      expect(urls).toHaveLength(1)
      expect(limitDe(urls[0])).toBeNull()
    },
  )

  it('un `limit` del llamante GANA, con su valor EXACTO', async () => {
    capturaFetch()
    await capabilitiesApi.listTools({ limit: 10 })
    // ⛔ `toBe('10')`, no `toContain('limit=1')`: eso último aceptaría 10, 100 y 1000.
    expect(limitDe(urls[0])).toBe('10')
  })

  it('un `limit: undefined` explícito NO pisa el techo', async () => {
    capturaFetch()
    await capabilitiesApi.listServers({ limit: undefined })
    expect(limitDe(urls[0])).toBe(String(EVIDENCE_PAGE))
  })

  it('el techo es el máximo que el motor acepta, LEÍDO DEL MOTOR', () => {
    const go = readFileSync(
      '../core/internal/store/sqlstore/generic.go',
      'utf8',
    )
    // Anclado a una declaración de constante Go, no a cualquier aparición del nombre:
    // el contraste avisó de que un regex suelto puede casar un comentario o una cadena.
    const m = go.match(/^\s*maxLimit\s*=\s*(\d+)\s*$/m)
    expect(
      m,
      'no encuentro la DECLARACION de maxLimit — la sonda dejó de ver el motor',
    ).not.toBeNull()
    expect(EVIDENCE_PAGE).toBe(Number(m![1]))
  })
})
