// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Las reglas DLP se piden con techo, y el techo no se puede borrar desde el llamante.
//
// ⛔ POR QUE. `handleListDLPRules` (`modules/knowledge/dlp.go:168`) responde con `Items` +
//    `Cursor` + `HasMore` a traves de `listQuery(r)`, y sin `?limit` el store generico sirve su
//    pagina por omision de CIEN (`core/internal/store/sqlstore/generic.go`). El cliente no mandaba
//    query ninguna: un tenant con mas de cien reglas DLP veia cien en una pantalla de gobierno y
//    nada decia que faltaran.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { knowledgeApi } from './api'
import { EVIDENCE_PAGE } from '@/features/models/api'
import type { RequestOptions } from '@/lib/api/client'

// ⛔ LOS ESPERADOS SON EL LITERAL '1000', NO `String(EVIDENCE_PAGE)`, y esto lo encontro el
//    contraste `the model` del 2026-08-26 en MI diseno. Derivando el esperado de la misma
//    constante que se vigila, el testigo se mueve CON el defecto: bajar `EVIDENCE_PAGE` de 1000 a
//    999 dejaba estas tres pruebas **3/3 en verde** mientras la pantalla pedia 999. Un oraculo que
//    sale de su sujeto no es un oraculo: es un espejo.
//    La forma buena ya estaba escrita al lado, en `models/tenant-list-options.test.ts:81-103`.
let peticiones: { url: string; init: RequestInit | undefined }[] = []

function capturaFetch(): void {
  globalThis.fetch = vi.fn(async (url: string, init?: RequestInit) => {
    peticiones.push({ url: String(url), init })
    return new Response(JSON.stringify({ items: [], has_more: false }), {
      status: 200,
      headers: new Headers({ 'Content-Type': 'application/json' }),
    })
  }) as never
}

const limitDe = (u: string) =>
  new URL(u, 'http://test').searchParams.get('limit')

describe('dlpRules pide el maximo del store', () => {
  afterEach(() => {
    peticiones = []
    vi.restoreAllMocks()
  })

  it('manda el techo del store generico', async () => {
    capturaFetch()
    await knowledgeApi.dlpRules({ tenant: 't1' })
    expect(limitDe(peticiones[0].url)).toBe('1000')
  })

  it('un `limit: undefined` explicito NO borra el techo', async () => {
    capturaFetch()
    await knowledgeApi.dlpRules({ tenant: 't1', query: { limit: undefined } })
    expect(limitDe(peticiones[0].url)).toBe('1000')
  })

  it('una variable ANCHA con `anonymous` no suelta el inquilino', async () => {
    const ancho = { tenant: 't1', anonymous: true } as RequestOptions & {
      tenant: string
    }
    capturaFetch()
    await knowledgeApi.dlpRules(ancho)
    expect(
      new Headers(peticiones[0].init?.headers).get('X-Olivares-Tenant'),
    ).toBe('t1')
  })

  it('la constante compartida no cambia sin que esta bateria lo diga', () => {
    // ⛔ Este caso existe PORQUE los de arriba ya no dependen de `EVIDENCE_PAGE`: fijar el literal
    //    cierra el espejo, pero dejaria la constante sin vigilante de este lado.
    //
    // ⛔ Y AQUI NO SE AFIRMA PARIDAD CON EL STORE, aunque la primera version de este comentario lo
    //    dijera. Comparar `EVIDENCE_PAGE` con otro literal de TypeScript **no crea ningun enlace
    //    con Go**: el contraste `the model` lo probo bajando `maxLimit` a 999 en
    //    `sqlstore/generic.go` y esta bateria seguia 4/4 verde, rc 0, sin un mensaje. Un comentario
    //    no es un contrato.
    //    La paridad REAL la afirma un test del lado de Go que LEE este arbol:
    //    `core/internal/store/sqlstore/evidence_page_parity_test.go`, y muere con los mutantes de
    //    los dos lados. Esto de aqui sigue siendo util por lo que si es: que el numero que la
    //    consola manda no cambie sin que nadie lo note DENTRO de la consola.
    expect(EVIDENCE_PAGE).toBe(1000)
  })
})
