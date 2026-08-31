// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// `listDependencies` pide el maximo del motor, y NADA MAS que eso.
//
// ⛔ LAS DOS MITADES SON IGUAL DE NECESARIAS, y la segunda es la que sorprende.
//    (1) Sin `limit` el motor sirve su pagina por omision de CIEN y la vista pinta una lista
//        incompleta sin decirlo.
//    (2) Pero `workChildrenQuery` (`modules/sessions/work_api.go:954-963`) RECHAZA con 400
//        `invalid_command` **cualquier** parametro de query que no sea `limit` o `cursor`. Asi que
//        el arreglo NO puede ser el de las listas de modelos —expandir lo que trae el llamante—:
//        aqui un filtro de mas no recorta, ROMPE. Este testigo fija las dos cosas.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { listDependencies } from './api'
import { WORK_PAGE_MAX } from './api'

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

describe('listDependencies pide el maximo del motor y solo eso', () => {
  afterEach(() => {
    urls = []
    vi.restoreAllMocks()
  })

  it('manda `limit` con el maximo de la familia sessions, que es 200 y NO 1000', async () => {
    capturaFetch()
    await listDependencies('11111111-1111-1111-1111-111111111111', {
      tenant: 't1',
    })
    const u = new URL(urls[0], 'http://test')
    expect(u.searchParams.get('limit')).toBe(String(WORK_PAGE_MAX))
    expect(WORK_PAGE_MAX).toBe(200)
  })

  it('NO manda ningun otro parametro: el motor responde 400 a lo que no sea limit/cursor', async () => {
    capturaFetch()
    await listDependencies('11111111-1111-1111-1111-111111111111', {
      tenant: 't1',
    })
    const claves = [
      ...new URL(urls[0], 'http://test').searchParams.keys(),
    ].sort()
    expect(claves).toEqual(['limit'])
  })
})
