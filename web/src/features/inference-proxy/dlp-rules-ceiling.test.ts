// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md
//
// La lista de reglas DLP se pide CON techo, y se comprueba sobre la peticion, no sobre el texto.
//
// ⛔ POR QUE. `listDLPRules` no mandaba query ninguna —`http.get(...)` a secas— y sin `?limit` el
//    store generico sirve su pagina por omision de CIEN
//    (`core/internal/store/sqlstore/generic.go:26-29`, `defaultLimit`). Un inquilino con mas de
//    cien reglas veia cien en una pantalla de gobierno del egreso y nada decia que faltaran: se
//    puede creer que el egreso esta gobernado por las reglas que se ven.
//
// ⛔ EL ESPERADO ES EL LITERAL '1000', NO `DLP_PAGE`. Un oraculo derivado de su propio sujeto se
//    mueve CON el defecto: bajar la constante a 999 dejaria esta prueba verde mientras la pantalla
//    pide 999. Es un espejo, no un oraculo. La leccion ya estaba escrita al lado, en
//    `knowledge/dlp-ceiling.test.ts`, y se copia a proposito.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { inferenceProxyApi } from './api'

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

describe('listDLPRules pide el maximo del store', () => {
  afterEach(() => {
    peticiones = []
    vi.restoreAllMocks()
  })

  it('manda el techo en la peticion', async () => {
    capturaFetch()
    await inferenceProxyApi.listDLPRules()
    expect(peticiones).toHaveLength(1)
    expect(limitDe(peticiones[0].url)).toBe('1000')
  })

  // Y la ruta sigue siendo la del modulo: un techo puesto en la llamada equivocada no protege
  // nada, y este par de aserciones distingue «pide techo» de «pide techo AQUI».
  it('lo manda en la ruta de reglas DLP', async () => {
    capturaFetch()
    await inferenceProxyApi.listDLPRules()
    expect(peticiones[0].url).toContain('/dlp/rules')
  })
})
