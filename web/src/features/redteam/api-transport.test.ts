// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { redteamApi } from './api'

/** Mira la URL: los tests de vista mockean el cliente y no verían un techo que se acepta y se tira. */
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
 * ⛔ EL VALOR DEL PARÁMETRO, NO UNA SUBCADENA. `toContain('limit=1000')` acepta también
 *    `limit=1000oops`, y eso no es teórico: con un techo así el motor falla el `Atoi`, deja
 *    `Limit=0` y el store **vuelve al 100 por defecto** — el techo revertido con la batería verde.
 *    Lo construyó un contraste.
 */
function limitDe(url: string): string | null {
  return new URL(url, 'http://test').searchParams.get('limit')
}

afterEach(() => {
  urls = []
})

describe('las listas de redteam que paginan piden su techo', () => {
  it.each([
    ['targets', () => redteamApi.targets()],
    ['runs', () => redteamApi.runs()],
  ])('%s manda limit=1000', async (_n, llamar) => {
    capturaFetch()
    await llamar()
    expect(urls).toHaveLength(1)
    expect(limitDe(urls[0])).toBe('1000')
  })

  /**
   * ⛔ `results` NO lleva techo, y la celda fija la DECISIÓN, no un olvido: `handleListResults`
   *    usa `listAll` y devuelve todos los resultados de la ejecución sin poner `HasMore`
   *    (`modules/redteam/scorecard.go`). Un techo ahí sería decorativo y su aviso, inalcanzable.
   *    Si alguien lo añade, esto se pone rojo y hay que releer por qué se dejó fuera.
   */
  it('results NO manda limit — su handler devuelve todos los resultados', async () => {
    capturaFetch()
    await redteamApi.results('run-1')
    expect(urls).toHaveLength(1)
    expect(limitDe(urls[0])).toBeNull()
  })

  it('el techo no pisa los filtros que ya viajaban', async () => {
    capturaFetch()
    await redteamApi.runs({ target_ref: 't-1', suite: 'owasp' })
    expect(limitDe(urls[0])).toBe('1000')
    expect(urls[0]).toContain('target_ref=t-1')
    expect(urls[0]).toContain('suite=owasp')
  })
})
