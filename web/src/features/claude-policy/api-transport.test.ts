// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { claudePolicyApi } from './api'

/**
 * ⛔ ESTE TESTIGO MIRA LA URL, y existe porque la otra mitad no puede.
 *
 * Los tests de vista mockean el cliente entero, así que verían pasar un método que ACEPTA el techo
 * y lo TIRA. En una pantalla de política eso importa el doble: aquí la ausencia de una fila es la
 * afirmación —«no hay deriva», «no hay versión»—, así que un recorte silencioso no es una lista
 * incompleta, es una respuesta falsa.
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

/** Las que PAGINAN. Las dos van al mismo handler de seguridad. */
const PAGINAN: Array<[string, () => Promise<unknown>]> = [
  ['listDrift', () => claudePolicyApi.listDrift()],
  ['listManagedAgentHitl', () => claudePolicyApi.listManagedAgentHitl()],
]

/**
 * Las que NO paginan: sus handlers devuelven el conjunto completo, así que un `limit` sería
 * decorativo. Empecé mandándoselo a las cinco; el contraste lo refutó y lo comprobé en el motor.
 * Estas celdas fijan la decisión: si alguien vuelve a añadirlo, se pone rojo y hay que releer por
 * qué se quitó.
 */
const NO_PAGINAN: Array<[string, () => Promise<unknown>]> = [
  ['listVersions', () => claudePolicyApi.listVersions('managed-settings')],
  ['pdpVersions', () => claudePolicyApi.pdpVersions()],
  ['threadEvents', () => claudePolicyApi.threadEvents('sess-1')],
]

describe('las listas de política que paginan piden su techo', () => {
  it.each(NO_PAGINAN)(
    '%s NO manda limit — su handler devuelve el conjunto completo',
    async (_n, llamar) => {
      capturaFetch()
      await llamar()
      expect(urls).toHaveLength(1)
      expect(limitDe(urls[0])).toBeNull()
    },
  )

  it.each(PAGINAN)('%s manda limit=1000 en la URL', async (_n, llamar) => {
    capturaFetch()
    await llamar()
    expect(urls).toHaveLength(1)
    expect(limitDe(urls[0])).toBe('1000')
  })

  it('CONTROL de la sonda — una ruta que NO es de lista no lleva techo', async () => {
    capturaFetch()
    await claudePolicyApi.pdpGetVersion('cedar', 3)
    expect(urls).toHaveLength(1)
    expect(limitDe(urls[0])).toBeNull()
  })

  it('el techo no pisa los filtros que ya viajaban', async () => {
    capturaFetch()
    await claudePolicyApi.listDrift()
    expect(urls[0]).toContain('kind=policy_drift')
    expect(limitDe(urls[0])).toBe('1000')
  })

  it('el filtro doble de HITL sigue viajando junto al techo', async () => {
    capturaFetch()
    await claudePolicyApi.listManagedAgentHitl()
    expect(urls[0]).toContain('kind=governance')
    expect(urls[0]).toContain('subject_kind=anthropic.managed_agent')
    expect(limitDe(urls[0])).toBe('1000')
  })
})
