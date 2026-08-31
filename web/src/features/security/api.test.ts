// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The findings SARIF export is a RAW fetch, so the logic that matters lives here
// rather than in the view: the request it builds, the server's own artifact name,
// and the truncation flag that tells an analyst the run is partial.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import { securityApi } from './api'

function sarifResponse(headers: Record<string, string> = {}) {
  return new Response('{"$schema":"sarif","runs":[]}', {
    status: 200,
    headers: {
      'Content-Type': 'application/sarif+json',
      // Deliberately NOT the client's fallback name: if the parser were dropped,
      // the fallback would satisfy a test that expected the default.
      'Content-Disposition':
        'attachment; filename="server-named-findings.sarif"',
      ...headers,
    },
  })
}

beforeEach(() => {
  vi.restoreAllMocks()
  useSessionStore.setState({ token: 'tok' } as never)
  useTenantStore.setState({ activeTenant: 'acme' } as never)
})

describe('securityApi.exportFindings', () => {
  it('asks for SARIF, carries auth and tenant, and passes the list filters through', async () => {
    const fetchMock = vi.fn().mockResolvedValue(sarifResponse())
    vi.stubGlobal('fetch', fetchMock)

    await securityApi.exportFindings({ severity: 'high', status: '' })

    const [url, init] = fetchMock.mock.calls[0]
    const query = new URL(url as string, 'https://x').searchParams
    expect(query.get('format')).toBe('sarif')
    expect(query.get('severity')).toBe('high')
    // An empty filter is absent, not sent as an empty string the server would
    // have to interpret.
    expect(query.has('status')).toBe(false)
    const headers = (init as RequestInit).headers as Headers
    expect(headers.get('Authorization')).toBe('Bearer tok')
    expect(headers.get('X-Olivares-Tenant')).toBe('acme')
  })

  it('keeps the server bytes and the server filename', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(sarifResponse()))

    const res = await securityApi.exportFindings()

    expect(res.text).toBe('{"$schema":"sarif","runs":[]}')
    expect(res.filename).toBe('server-named-findings.sarif')
    expect(res.content_type).toBe('application/sarif+json')
    expect(res.truncated).toBe(false)
  })

  it('falls back to its own name only when the server suggests none', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response('{}', {
          status: 200,
          headers: { 'Content-Type': 'application/sarif+json' },
        }),
      ),
    )

    expect((await securityApi.exportFindings()).filename).toBe(
      'olivares-findings.sarif',
    )
  })

  it('reports a capped export as truncated', async () => {
    vi.stubGlobal(
      'fetch',
      vi
        .fn()
        .mockResolvedValue(sarifResponse({ 'X-Olivares-Truncated': 'true' })),
    )

    expect((await securityApi.exportFindings()).truncated).toBe(true)
  })

  it('surfaces the server error envelope instead of a half-written file', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ code: 'forbidden', message: 'nope' }), {
          status: 403,
          headers: { 'Content-Type': 'application/json' },
        }),
      ),
    )

    await expect(securityApi.exportFindings()).rejects.toBeInstanceOf(ApiError)
  })
})

// ⛔ EL TESTIGO DE TRANSPORTE, que la batería de la pantalla NO puede tener: allí
//    `securityApi.cases` está mockeado entero, así que un mutante que reciba el `limit` y
//    NO lo ponga en la URL escaparía — comprobar que la vista llamó con `(undefined, 1000)`
//    no dice nada sobre lo que sale por el cable. Lo señaló el contraste.
describe('los parámetros de lista llegan a la URL, no sólo a la firma', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
    useSessionStore.setState({ token: 't' } as never)
    useTenantStore.setState({ activeTenant: 't1' } as never)
  })

  it('cases manda `limit` en la query', async () => {
    const spy = vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response('{"items":[],"has_more":false}', {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    )
    await securityApi.cases(undefined, 1000)
    // Se busca ENTRE TODAS las llamadas registradas, no en una posición: este fichero
    // comparte el `fetch` global con los casos del export y el orden de registro no es
    // una propiedad de lo que se está midiendo.
    const urls = spy.mock.calls.map((c) =>
      String(c[0] as Request | string | URL),
    )
    // FIRES IF: alguien quita `limit` del objeto `query` del envoltorio.
    expect(urls.some((u) => /\/cases\?.*limit=1000/.test(u))).toBe(true)
  })

  it('findings manda `limit` en la query', async () => {
    const spy = vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response('{"items":[],"has_more":false}', {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    )
    await securityApi.findings({ limit: 1000 })
    const urls = spy.mock.calls.map((c) =>
      String(c[0] as Request | string | URL),
    )
    expect(urls.some((u) => /\/findings\?.*limit=1000/.test(u))).toBe(true)
  })
})
