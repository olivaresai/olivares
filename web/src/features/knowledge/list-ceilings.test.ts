// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md
//
// Las listas de conocimiento piden techo — y las que DRENAN no lo piden, tambien a proposito.
//
// ⛔ EL CASO QUE MAS VALE ES EL DEL ORDEN, y no es teorico: cinco de estas llamadas YA tenian
//    techo, escrito `{ limit: EVIDENCE_PAGE, ...params }`. En ese orden, un `limit: undefined`
//    explicito —TypeScript valido, y sale solo de `lista({ limit: filtro.limit })` con el filtro
//    vacio— BORRA el techo y la llamada vuelve a pedir los cien del store en silencio. La forma
//    de ahora deja al llamante BAJARLO y no BORRARLO.
//
// ⛔ ESPERADO POR EL LITERAL '1000', nunca por `EVIDENCE_PAGE`: un oraculo derivado de su sujeto
//    se mueve CON el defecto.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { knowledgeApi } from './api'

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

describe('las listas paginadas de conocimiento piden el maximo del store', () => {
  it.each([
    ['listKbs', () => knowledgeApi.listKbs()],
    ['listLineage', () => knowledgeApi.listLineage()],
    ['listPrompts', () => knowledgeApi.listPrompts()],
    ['listContextPolicies', () => knowledgeApi.listContextPolicies()],
    ['listDataProducts', () => knowledgeApi.listDataProducts()],
    ['listDocuments', () => knowledgeApi.listDocuments('kb1')],
    ['listContracts', () => knowledgeApi.listContracts('dp1')],
    ['listDPEvents', () => knowledgeApi.listDPEvents('dp1')],
    ['labels', () => knowledgeApi.labels()],
  ])('%s manda el techo', async (_n, llamada) => {
    capturaFetch()
    await llamada()
    expect(q(urls[0], 'limit')).toBe('1000')
  })

  // ⛔ EL ORDEN, LLAMADA POR LLAMADA. Probarlo en una sola dejaria en verde invertir las demas —
  //    lo encontro el contraste de `lib/api/endpoints.ts`— y aqui habia CINCO invertidas a la vez.
  it.each([
    ['listKbs', () => knowledgeApi.listKbs({ limit: undefined })],
    ['listLineage', () => knowledgeApi.listLineage({ limit: undefined })],
    ['listPrompts', () => knowledgeApi.listPrompts({ limit: undefined })],
    [
      'listContextPolicies',
      () => knowledgeApi.listContextPolicies({ limit: undefined }),
    ],
    [
      'listDataProducts',
      () => knowledgeApi.listDataProducts({ limit: undefined }),
    ],
    [
      'listDocuments',
      () => knowledgeApi.listDocuments('kb1', { limit: undefined }),
    ],
    [
      'listContracts',
      () => knowledgeApi.listContracts('dp1', { limit: undefined }),
    ],
    [
      'listDPEvents',
      () => knowledgeApi.listDPEvents('dp1', { limit: undefined }),
    ],
  ])(
    'un `limit: undefined` explicito NO borra el techo en %s',
    async (_n, llamada) => {
      capturaFetch()
      await llamada()
      expect(q(urls[0], 'limit')).toBe('1000')
    },
  )

  it('el llamante puede pedir menos: el techo es un maximo, no una imposicion', async () => {
    capturaFetch()
    await knowledgeApi.listKbs({ limit: 25 })
    expect(q(urls[0], 'limit')).toBe('25')
  })

  it('el techo no se lleva por delante los filtros', async () => {
    capturaFetch()
    await knowledgeApi.listKbs({ status: 'ready' })
    expect(q(urls[0], 'limit')).toBe('1000')
    expect(q(urls[0], 'status')).toBe('ready')
  })
})

describe('lo que DRENA no pide techo, y esa ausencia es el contrato', () => {
  // `handleListAllMemory` llama a `listAll`, que recorre `q.Cursor` en BUCLE hasta `!page.HasMore`.
  // Declaraba `params?: { limit?: number }` y el handler NUNCA lo leia: un control anunciado que no
  // existe es peor que ninguno, porque quien pasara `limit: 10` recibiria la memoria entera.
  it('allMemory no manda limit, porque el motor drena y no lo lee', async () => {
    capturaFetch()
    await knowledgeApi.allMemory()
    expect(q(urls[0], 'limit')).toBeNull()
  })

  // Misma familia: /memory y /prompts/{id}/revisions tambien drenan con `listAll`.
  it('listRevisions no manda limit', async () => {
    capturaFetch()
    await knowledgeApi.listRevisions('p1')
    expect(q(urls[0], 'limit')).toBeNull()
  })
})
