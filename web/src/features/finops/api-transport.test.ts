// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { finopsApi } from './api'

/**
 * ⛔ ESTE TESTIGO MIRA LA URL, y existe porque la otra mitad no puede.
 *
 * Los tests de vista mockean `finopsApi` entero, así que verían pasar un cliente que ACEPTA el
 * techo y lo TIRA — que es exactamente lo que pasó una vez con `listTokens` en la consola: el
 * método recibía `limit` y sólo reenviaba `include_revoked`. La única forma de saber que el techo
 * viaja es leer la petición que sale.
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

/** Las siete listas y cómo se llaman. */
const LISTAS: Array<[string, () => Promise<unknown>]> = [
  ['outcomes', () => finopsApi.outcomes(undefined, { tenant: 't-transporte' })],
  ['budgets', () => finopsApi.budgets()],
  ['alerts', () => finopsApi.alerts()],
  [
    'costCenters',
    () => finopsApi.costCenters(undefined, { tenant: 't-transporte' }),
  ],
  [
    'costCenterMappings',
    () => finopsApi.costCenterMappings('cc-1', { tenant: 't-transporte' }),
  ],
  [
    'modelRates',
    () => finopsApi.modelRates(undefined, { tenant: 't-transporte' }),
  ],
  [
    'statements',
    () => finopsApi.statements(undefined, { tenant: 't-transporte' }),
  ],
]

describe('las siete listas de finops piden su techo', () => {
  it.each(LISTAS)('%s manda limit=1000 en la URL', async (_nombre, llamar) => {
    capturaFetch()
    await llamar()
    expect(urls).toHaveLength(1)
    expect(limitDe(urls[0])).toBe('1000')
  })

  it('CONTROL de la sonda — una ruta que NO es de lista no lleva techo', async () => {
    capturaFetch()
    await finopsApi.budgetStatus('b-1')
    expect(urls).toHaveLength(1)
    expect(limitDe(urls[0])).toBeNull()
  })

  it('un `limit` explícito del llamante MANDA sobre el techo por defecto', async () => {
    capturaFetch()
    await finopsApi.alerts({ limit: 25 })
    expect(limitDe(urls[0])).toBe('25')
    expect(limitDe(urls[0])).not.toBe('1000')
  })

  it('el techo no pisa los filtros que ya pasaban', async () => {
    capturaFetch()
    await finopsApi.statements(
      { period: '2026-08', status: 'issued' },
      { tenant: 't-transporte' },
    )
    expect(limitDe(urls[0])).toBe('1000')
    expect(urls[0]).toContain('period=2026-08')
    expect(urls[0]).toContain('status=issued')
  })
})
