// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ⛔ EL TECHO SE MIDE EN LA URL, NO EN LA LLAMADA AL CLIENTE. Los tests de vista mockean
// `consoleApi`, así que comprueban lo que la pantalla PIDE al cliente y no lo que el cliente MANDA.
// Aquí eso importa el doble: el cuerpo de `listTokens` descartaba todo salvo `include_revoked`, de
// modo que añadir `limit?` al TIPO habría dejado el techo en la firma sin llegar nunca al servidor
// — y ningún testigo de vista lo habría notado. Lo cazó un mutante que quitaba el reenvío del
// `limit` y ESCAPABA del arnés de vistas.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { consoleApi } from './api'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'

function jsonResponse(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  })
}

beforeEach(() => {
  vi.restoreAllMocks()
  useSessionStore.setState({ token: 'tok' } as never)
  useTenantStore.setState({ activeTenant: 'acme' } as never)
})

describe('consoleApi.listTokens — lo que sale por el cable', () => {
  it('el limit viaja en la URL', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(jsonResponse({ items: [], has_more: false }))
    vi.stubGlobal('fetch', fetchMock)

    await consoleApi.listTokens({ limit: 1000 })

    const [url] = fetchMock.mock.calls[0]
    const query = new URL(url as string, 'https://x').searchParams
    expect(query.get('limit')).toBe('1000')
    // Y sin pedir revocados, ese parámetro NO se manda: un filtro vacío no se inventa.
    expect(query.has('include_revoked')).toBe(false)
  })

  it('el limit y include_revoked viajan JUNTOS', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(jsonResponse({ items: [], has_more: false }))
    vi.stubGlobal('fetch', fetchMock)

    await consoleApi.listTokens({ include_revoked: true, limit: 1000 })

    const query = new URL(fetchMock.mock.calls[0][0] as string, 'https://x')
      .searchParams
    expect(query.get('limit')).toBe('1000')
    expect(query.get('include_revoked')).toBe('true')
  })
})

/**
 * ⛔ UN `limit: undefined` NO PUEDE BORRAR EL TECHO. Lo midio el contraste (F-03): en nueve
 *    rutas el orden era `{ limit: EVIDENCE_PAGE, ...params }`, y como ocho firmas admiten
 *    `limit?: number`, pasar `undefined` PISABA el 1000 y el cliente omitia el parametro despues
 *    — la peticion salia SIN techo. Un valor explicito del llamante debe ganar; la AUSENCIA de
 *    valor, no. Se prueba por el cable, que es donde se ve la diferencia.
 */
/**
 * ⛔ Y EL SUJETO ES `listWorkspaces`, NO `listTokens`. Lo descubri al mutar: `listTokens` ya era
 *    inmune —usa `...(params?.limit ? { limit: params.limit } : {})`, un spread CONDICIONAL que
 *    no pisa con `undefined`—, asi que un testigo suyo pasa sin tocar el defecto y yo lo habria
 *    dado por cubierto. Prueba correcta, conclusion falsa. Las nueve rutas reconstruidas son las
 *    que hacian `{ limit: EVIDENCE_PAGE, ...params }` con spread INCONDICIONAL, y
 *    `listWorkspaces` es la que el propio contraste sondeo (obtuvo `limit=null`).
 */
describe('consoleApi.listWorkspaces — el techo sobrevive a un limit ausente', () => {
  it('limit: undefined NO borra el techo — viaja 1000', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(jsonResponse({ items: [], has_more: false }))
    vi.stubGlobal('fetch', fetchMock)

    await consoleApi.listWorkspaces({ limit: undefined })

    const [url] = fetchMock.mock.calls[0]
    const query = new URL(url as string, 'https://x').searchParams
    expect(query.get('limit')).toBe('1000')
  })

  it('y un valor MENOR explicito del llamante sigue ganando', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(jsonResponse({ items: [], has_more: false }))
    vi.stubGlobal('fetch', fetchMock)

    await consoleApi.listWorkspaces({ limit: 25 })

    const [url] = fetchMock.mock.calls[0]
    const query = new URL(url as string, 'https://x').searchParams
    expect(query.get('limit')).toBe('25')
  })
})

/** El de `listTokens` se queda: documenta que su spread condicional ya era correcto. */
describe('consoleApi.listTokens — el techo sobrevive a un limit ausente', () => {
  it('limit: undefined NO borra el techo — viaja 1000', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(jsonResponse({ items: [], has_more: false }))
    vi.stubGlobal('fetch', fetchMock)

    await consoleApi.listTokens({ limit: undefined })

    const [url] = fetchMock.mock.calls[0]
    const query = new URL(url as string, 'https://x').searchParams
    expect(query.get('limit')).toBe('1000')
  })

  it('y un valor MENOR explicito del llamante sigue ganando', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(jsonResponse({ items: [], has_more: false }))
    vi.stubGlobal('fetch', fetchMock)

    await consoleApi.listTokens({ limit: 25 })

    const [url] = fetchMock.mock.calls[0]
    const query = new URL(url as string, 'https://x').searchParams
    expect(query.get('limit')).toBe('25')
  })
})
