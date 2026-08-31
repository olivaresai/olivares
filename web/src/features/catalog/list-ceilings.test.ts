// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md
//
// Las tres listas del catalogo se piden CON techo, y se comprueba sobre la peticion.
//
// ⛔ POR QUE. Ninguna mandaba `limit`: `{ ...params }` en entries e instances, y
//    `{ entry_ref: … }` en admisiones. Sin `?limit` el store generico sirve su pagina por omision
//    de CIEN (`core/internal/store/sqlstore/generic.go:26-29`). Verificado en el motor antes de
//    poner techo —las tres PAGINAN, no drenan—, asi que el recorte es real.
//
// ⛔ LOS ESPERADOS SON EL LITERAL '1000', NO `CATALOG_PAGE`. Un oraculo derivado de su sujeto se
//    mueve CON el defecto: bajar la constante a 999 dejaria esto verde con la pantalla pidiendo
//    999. Es un espejo, no un oraculo.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { catalogApi } from './api'

let peticiones: { url: string }[] = []

function capturaFetch(): void {
  globalThis.fetch = vi.fn(async (url: string) => {
    peticiones.push({ url: String(url) })
    return new Response(JSON.stringify({ items: [], has_more: false }), {
      status: 200,
      headers: new Headers({ 'Content-Type': 'application/json' }),
    })
  }) as never
}

const limitDe = (u: string) =>
  new URL(u, 'http://test').searchParams.get('limit')

describe('las listas del catalogo piden el maximo del store', () => {
  afterEach(() => {
    peticiones = []
    vi.restoreAllMocks()
  })

  it('listEntries manda el techo', async () => {
    capturaFetch()
    await catalogApi.listEntries()
    expect(limitDe(peticiones[0].url)).toBe('1000')
  })

  it('listInstances manda el techo', async () => {
    capturaFetch()
    await catalogApi.listInstances()
    expect(limitDe(peticiones[0].url)).toBe('1000')
  })

  it('listAdmissions manda el techo, y no pierde su filtro', async () => {
    capturaFetch()
    await catalogApi.listAdmissions('mcp', 'e1')
    expect(limitDe(peticiones[0].url)).toBe('1000')
    // Un techo que se lleva por delante el filtro no arregla nada: cambia una lista recortada
    // por una lista de otra cosa.
    expect(
      new URL(peticiones[0].url, 'http://test').searchParams.get('entry_ref'),
    ).toBe('e1')
  })

  // ⛔ EL ORDEN DEL SPREAD, probado por SEPARADO en cada una: el contraste que reviso este mismo
  //    patron en `lib/api/endpoints.ts` encontro que la propiedad solo estaba probada para UNA
  //    llamada, asi que invertir el orden en la otra dejaba la bateria entera en verde.
  it('un `limit: undefined` explicito NO borra el techo en entries', async () => {
    capturaFetch()
    await catalogApi.listEntries({ limit: undefined })
    expect(limitDe(peticiones[0].url)).toBe('1000')
  })

  it('un `limit: undefined` explicito NO borra el techo en instances', async () => {
    capturaFetch()
    await catalogApi.listInstances({ limit: undefined })
    expect(limitDe(peticiones[0].url)).toBe('1000')
  })

  // Y el llamante SI puede bajarlo: el techo es un maximo, no una imposicion.
  it('el llamante puede pedir menos', async () => {
    capturaFetch()
    await catalogApi.listEntries({ limit: 25 })
    expect(limitDe(peticiones[0].url)).toBe('25')
  })
})
