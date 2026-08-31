// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md
//
// Cuatro listas de capabilities piden techo — y TRES no lo piden a proposito.
//
// ⛔ LOS CASOS NEGATIVOS SON LA MITAD QUE IMPORTA. Un testigo que solo comprueba los
//    cuatro techos deja que alguien «arregle» las otras tres poniendoles un `limit` que
//    el motor ignora, y eso no es inocuo: convierte una decision escrita en un descuido
//    aparente y el siguiente que pase lo repite. Cada negativo cita el motivo de motor.
//
// ⛔ ESPERADO POR EL LITERAL '1000', nunca por `CAPS_PAGE`: un oraculo derivado de su
//    sujeto se mueve CON el defecto.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { capabilitiesApi } from './api'

let urls: string[] = []

function capturaFetch(payload: unknown = { items: [], has_more: false }): void {
  globalThis.fetch = vi.fn(async (url: string) => {
    urls.push(String(url))
    return new Response(JSON.stringify(payload), {
      status: 200,
      headers: new Headers({ 'Content-Type': 'application/json' }),
    })
  }) as never
}

const q = (u: string, k: string) =>
  new URL(u, 'http://test').searchParams.get(k)

afterEach(() => {
  urls = []
  vi.restoreAllMocks()
})

describe('las listas paginadas de capabilities piden el maximo del store', () => {
  it('listServers manda el techo', async () => {
    capturaFetch()
    await capabilitiesApi.listServers()
    expect(q(urls[0], 'limit')).toBe('1000')
  })

  it('listSkills manda el techo, y conserva su filtro', async () => {
    capturaFetch()
    await capabilitiesApi.listSkills({ server_id: 's1' })
    expect(q(urls[0], 'limit')).toBe('1000')
    expect(q(urls[0], 'server_id')).toBe('s1')
  })

  it('listTools manda el techo, y conserva su filtro', async () => {
    capturaFetch()
    await capabilitiesApi.listTools({ server_id: 's1' })
    expect(q(urls[0], 'limit')).toBe('1000')
    expect(q(urls[0], 'server_id')).toBe('s1')
  })

  it('listConfigs manda el techo, y conserva sus filtros', async () => {
    capturaFetch()
    await capabilitiesApi.listConfigs({
      server_ref: 'github',
      transport: 'stdio',
    })
    expect(q(urls[0], 'limit')).toBe('1000')
    expect(q(urls[0], 'server_ref')).toBe('github')
    expect(q(urls[0], 'transport')).toBe('stdio')
  })

  // El orden del spread, probado en CADA UNA por separado: en `lib/api/endpoints.ts` se
  // encontro que probarlo en una sola dejaba en verde invertirlo en las demas.
  it.each([
    ['listServers', () => capabilitiesApi.listServers({ limit: undefined })],
    ['listSkills', () => capabilitiesApi.listSkills({ limit: undefined })],
    ['listTools', () => capabilitiesApi.listTools({ limit: undefined })],
    ['listConfigs', () => capabilitiesApi.listConfigs({ limit: undefined })],
  ])(
    'un `limit: undefined` explicito NO borra el techo en %s',
    async (_n, llamada) => {
      capturaFetch()
      await llamada()
      expect(q(urls[0], 'limit')).toBe('1000')
    },
  )

  it('y el llamante puede pedir menos: el techo es un maximo, no una imposicion', async () => {
    capturaFetch()
    await capabilitiesApi.listServers({ limit: 25 })
    expect(q(urls[0], 'limit')).toBe('25')
  })
})

describe('las tres que NO llevan techo, y por que', () => {
  // wiring fija su pagina dentro del motor (`Limit: listCap`) y no lee `limit` de la
  // query: mandarlo no cambiaria nada. Lo que si emite es `truncated`.
  it('wiring no manda limit', async () => {
    capturaFetch({ nodes: [], edges: [], note: '' })
    await capabilitiesApi.wiring()
    expect(q(urls[0], 'limit')).toBeNull()
  })

  // listRevisions tampoco lee `limit` ni cursor: el recorte lo declara `has_more`, que
  // el motor no propagaba hasta 2026-08-28.
  it('listRevisions no manda limit', async () => {
    capturaFetch()
    await capabilitiesApi.listRevisions('c1')
    expect(q(urls[0], 'limit')).toBeNull()
  })

  // listToolPins DRENA: el handler recorre `m.toolPins.Pins()` en memoria, sin store y
  // sin pagina. Un techo o un aviso avisarian de algo que no puede pasar.
  it('listToolPins no manda limit', async () => {
    capturaFetch({ items: [] })
    await capabilitiesApi.listToolPins()
    expect(q(urls[0], 'limit')).toBeNull()
  })
})
