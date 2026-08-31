// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  __resetRefreshState,
  apiFetch,
  configureApiClient,
  ensureFreshSession,
  http,
} from './client'
import { ApiError, NetworkError } from './errors'

let lastUrl: string | undefined
let lastInit: RequestInit | undefined

function mock(
  status: number,
  body?: unknown,
  headers: Record<string, string> = {},
) {
  globalThis.fetch = vi.fn(async (url: string, init?: RequestInit) => {
    lastUrl = String(url)
    lastInit = init
    const h = new Headers({ 'Content-Type': 'application/json', ...headers })
    return new Response(
      body === undefined || status === 204 ? null : JSON.stringify(body),
      {
        status,
        headers: h,
      },
    )
  }) as never
}

function header(name: string): string | null {
  return new Headers(lastInit?.headers).get(name)
}

afterEach(() => {
  configureApiClient({
    getToken: () => null,
    getTenant: () => null,
    onUnauthorized: () => {},
    refreshSession: undefined,
    getExpiresAt: undefined,
  })
  __resetRefreshState()
  lastUrl = undefined
  lastInit = undefined
})

describe('apiFetch', () => {
  it('attaches the bearer token and tenant header for authenticated requests', async () => {
    configureApiClient({
      getToken: () => 'olvs_abc',
      getTenant: () => 'tenant-1',
    })
    mock(200, { ok: true })
    await apiFetch('/v1/agents')
    expect(header('Authorization')).toBe('Bearer olvs_abc')
    expect(header('X-Olivares-Tenant')).toBe('tenant-1')
  })

  it('does not attach auth headers for anonymous requests', async () => {
    configureApiClient({
      getToken: () => 'olvs_abc',
      getTenant: () => 'tenant-1',
    })
    mock(200, { setup_required: false })
    await apiFetch('/v1/server-info', { anonymous: true })
    expect(header('Authorization')).toBeNull()
    expect(header('X-Olivares-Tenant')).toBeNull()
  })

  it('honors an explicit per-request tenant override', async () => {
    configureApiClient({
      getToken: () => 'olvs_abc',
      getTenant: () => 'tenant-1',
    })
    mock(201, {})
    await apiFetch('/v1/tokens', {
      method: 'POST',
      body: {},
      tenant: 'tenant-2',
    })
    expect(header('X-Olivares-Tenant')).toBe('tenant-2')
  })

  it('appends defined query params and omits undefined', async () => {
    mock(200, { items: [] })
    await http.get('/v1/agents', { query: { limit: 50, cursor: undefined } })
    expect(lastUrl).toBe('/v1/agents?limit=50')
  })

  // A REPEATABLE engine filter (e.g. /v1/audit's exclude_action) is only repeatable
  // if the client emits it that way. The regression this forbids is silent and only
  // hits the second entry onwards: `String(['a','b'])` is the single value "a,b",
  // which the engine reads as one prefix and matches nothing — a filter that appears
  // to work with one value and quietly stops with two.
  it('repeats an array query param instead of comma-joining it', async () => {
    mock(200, { items: [] })
    await http.get('/v1/audit', {
      query: { limit: 10, exclude_action: ['audit.read', 'noise.'] },
    })
    expect(lastUrl).toBe(
      '/v1/audit?limit=10&exclude_action=audit.read&exclude_action=noise.',
    )
  })

  it('omits an EMPTY array rather than sending a blank occurrence', async () => {
    mock(200, { items: [] })
    await http.get('/v1/audit', { query: { limit: 10, exclude_action: [] } })
    expect(lastUrl).toBe('/v1/audit?limit=10')
  })

  it('returns the parsed body on success', async () => {
    mock(200, { version: 'dev' })
    const out = await http.get<{ version: string }>('/v1/server-info', {
      anonymous: true,
    })
    expect(out.version).toBe('dev')
  })

  it('resolves to undefined for a 204 response', async () => {
    mock(204)
    const out = await http.delete('/v1/tokens/abc')
    expect(out).toBeUndefined()
  })

  it('maps the error envelope to an ApiError with code, status and request id', async () => {
    mock(
      404,
      { error: { code: 'not_found', message: 'missing' } },
      { 'X-Request-ID': 'req-9' },
    )
    await expect(http.get('/v1/agents/x')).rejects.toMatchObject({
      status: 404,
      code: 'not_found',
      requestId: 'req-9',
    })
  })

  it('calls onUnauthorized on an authenticated 401', async () => {
    const onUnauthorized = vi.fn()
    configureApiClient({ getToken: () => 'olvs_x', onUnauthorized })
    mock(401, { error: { code: 'unauthenticated', message: 'no' } })
    await expect(http.get('/v1/auth/whoami')).rejects.toBeInstanceOf(ApiError)
    expect(onUnauthorized).toHaveBeenCalledTimes(1)
  })

  // ── refresco de credencial en un 401 ────────────────────────────────────────────
  //
  // ⛔ Hasta hoy un 401 autenticado iba directo a onUnauthorized: sesión borrada, /login,
  //    y el trabajo a medias del operador perdido. Una sesión CADUCADA y una REVOCADA son
  //    hechos distintos y sólo uno debe costar trabajo.

  it('rota la credencial y REPITE la petición en un 401 recuperable', async () => {
    const onUnauthorized = vi.fn()
    let n = 0
    globalThis.fetch = vi.fn(async () => {
      n += 1
      if (n === 1)
        return new Response(
          JSON.stringify({
            error: { code: 'unauthenticated', message: 'expired' },
          }),
          { status: 401, headers: { 'Content-Type': 'application/json' } },
        )
      return new Response(JSON.stringify({ ok: true }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }) as never
    const refreshSession = vi.fn(async () => true)
    configureApiClient({
      getToken: () => 'olvs_x',
      onUnauthorized,
      refreshSession,
    })

    await expect(http.get('/v1/auth/whoami')).resolves.toEqual({ ok: true })
    expect(refreshSession).toHaveBeenCalledTimes(1)
    expect(n).toBe(2)
    // Y la dirección que NO debe dispararse: un 401 recuperado no cierra la sesión.
    expect(onUnauthorized).not.toHaveBeenCalled()
  })

  it('cierra la sesión cuando el refresco NO puede renovar (revocada)', async () => {
    const onUnauthorized = vi.fn()
    const refreshSession = vi.fn(async () => false)
    configureApiClient({
      getToken: () => 'olvs_x',
      onUnauthorized,
      refreshSession,
    })
    mock(401, { error: { code: 'unauthenticated', message: 'revoked' } })

    await expect(http.get('/v1/auth/whoami')).rejects.toBeInstanceOf(ApiError)
    expect(refreshSession).toHaveBeenCalledTimes(1)
    expect(onUnauthorized).toHaveBeenCalledTimes(1)
  })

  it('reintenta UNA sola vez: un 401 que persiste no entra en bucle', async () => {
    // ⛔ EL TOPE SE DETECTA RÁPIDO A PROPÓSITO. Sin `yaReintentado` la recursión no tiene
    //    freno alguno, y una celda que sólo afirma el recuento se queda COLGADA hasta que
    //    el corredor la mata: rojo, sí, pero ilegible y lento. El mock corta a la cuarta
    //    llamada, así que la regresión sale con un mensaje que dice qué pasó.
    const onUnauthorized = vi.fn()
    const refreshSession = vi.fn(async () => true)
    let llamadas = 0
    globalThis.fetch = vi.fn(async () => {
      llamadas += 1
      if (llamadas > 3)
        throw new Error(
          `el cliente reintentó ${llamadas} veces: el tope de UN reintento no está`,
        )
      return new Response(
        JSON.stringify({
          error: { code: 'unauthenticated', message: 'still no' },
        }),
        { status: 401, headers: { 'Content-Type': 'application/json' } },
      )
    }) as never
    configureApiClient({
      getToken: () => 'olvs_x',
      onUnauthorized,
      refreshSession,
    })

    await expect(http.get('/v1/auth/whoami')).rejects.toBeInstanceOf(ApiError)
    expect(llamadas).toBe(2) // la original + UN reintento, nunca más
    expect(refreshSession).toHaveBeenCalledTimes(1)
    expect(onUnauthorized).toHaveBeenCalledTimes(1)
  })

  it('NO refresca ante el 401 del propio /v1/auth/refresh', async () => {
    const onUnauthorized = vi.fn()
    const refreshSession = vi.fn(async () => true)
    configureApiClient({
      getToken: () => 'olvs_x',
      onUnauthorized,
      refreshSession,
    })
    mock(401, { error: { code: 'unauthenticated', message: 'session over' } })

    await expect(
      apiFetch('/v1/auth/refresh', { method: 'POST' }),
    ).rejects.toBeInstanceOf(ApiError)
    expect(refreshSession).not.toHaveBeenCalled()
    expect(onUnauthorized).toHaveBeenCalledTimes(1)
  })

  it('⭐ UN SOLO refresco sirve a CINCO 401 concurrentes', async () => {
    // Ésta es la celda que impide que el arreglo sea peor que el defecto. El motor ROTA la
    // credencial: la vieja muere en cuanto se emite la nueva (core/api/api_test.go:316).
    // Cinco refrescos independientes rotarían cinco veces, las cuatro primeras credenciales
    // morirían en el acto y el operador acabaría fuera igual, habiendo quemado cinco
    // rotaciones.
    const onUnauthorized = vi.fn()
    const vistos = new Set<string>()
    let primeraTanda = true
    globalThis.fetch = vi.fn(async (url: string) => {
      const u = String(url)
      if (primeraTanda && !vistos.has(u)) {
        vistos.add(u)
        return new Response(
          JSON.stringify({
            error: { code: 'unauthenticated', message: 'expired' },
          }),
          { status: 401, headers: { 'Content-Type': 'application/json' } },
        )
      }
      return new Response(JSON.stringify({ ok: true }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }) as never
    let refrescos = 0
    const refreshSession = vi.fn(async () => {
      refrescos += 1
      await new Promise((r) => setTimeout(r, 5))
      primeraTanda = false
      return true
    })
    configureApiClient({
      getToken: () => 'olvs_x',
      onUnauthorized,
      refreshSession,
    })

    const rutas = ['/a', '/b', '/c', '/d', '/e']
    const res = await Promise.all(rutas.map((r) => http.get(r)))
    expect(res).toEqual(rutas.map(() => ({ ok: true })))
    expect(refrescos).toBe(1)
    expect(onUnauthorized).not.toHaveBeenCalled()
  })

  // ── renovación ANTES de caducar ─────────────────────────────────────────────────
  //
  // ⛔ ESTAS SON LAS CELDAS DEL ARREGLO DE VERDAD, y existen porque el reintento en el 401
  //    NO resuelve el caso que C07-02 nombra. Medido en el motor: el autenticador rechaza
  //    una sesión caducada (core/auth/authenticator.go:140) y `RefreshSession` se niega
  //    explícitamente a resucitarla (`:572`, «refresh extends a live session, it never
  //    resurrects a dead one»). Cuando una petición da 401 PORQUE el token caducó, el
  //    refresco con ese mismo token también da 401. Sólo renovar ANTES sirve.

  it('renueva antes de la petición cuando la sesión está a punto de caducar', async () => {
    const refreshSession = vi.fn(async () => true)
    configureApiClient({
      getToken: () => 'olvs_x',
      refreshSession,
      getExpiresAt: () => new Date(Date.now() + 30_000).toISOString(),
    })
    mock(200, { ok: true })
    await expect(http.get('/v1/auth/whoami')).resolves.toEqual({ ok: true })
    expect(refreshSession).toHaveBeenCalledTimes(1)
  })

  it('NO renueva cuando queda margen de sobra', async () => {
    // El control que impide que esto renueve en cada petición: si dispara siempre, rota la
    // credencial constantemente y es peor que no renovar.
    const refreshSession = vi.fn(async () => true)
    configureApiClient({
      getToken: () => 'olvs_x',
      refreshSession,
      getExpiresAt: () => new Date(Date.now() + 3_600_000).toISOString(),
    })
    mock(200, { ok: true })
    await expect(http.get('/v1/auth/whoami')).resolves.toEqual({ ok: true })
    expect(refreshSession).not.toHaveBeenCalled()
  })

  it('NO renueva sin fecha de caducidad ni con una ilegible', async () => {
    const refreshSession = vi.fn(async () => true)
    for (const exp of [null, 'no-es-una-fecha']) {
      configureApiClient({
        getToken: () => 'olvs_x',
        refreshSession,
        getExpiresAt: () => exp,
      })
      mock(200, { ok: true })
      await expect(http.get('/v1/x')).resolves.toEqual({ ok: true })
    }
    expect(refreshSession).not.toHaveBeenCalled()
  })

  it('NO renueva en una petición anónima', async () => {
    const refreshSession = vi.fn(async () => true)
    configureApiClient({
      getToken: () => null,
      refreshSession,
      getExpiresAt: () => new Date(Date.now() + 1_000).toISOString(),
    })
    mock(200, { ok: true })
    await expect(
      apiFetch('/v1/auth/login', { method: 'POST', body: {}, anonymous: true }),
    ).resolves.toEqual({ ok: true })
    expect(refreshSession).not.toHaveBeenCalled()
  })

  it('cinco peticiones a punto de caducar renuevan UNA vez', async () => {
    let refrescos = 0
    const refreshSession = vi.fn(async () => {
      refrescos += 1
      await new Promise((r) => setTimeout(r, 5))
      return true
    })
    let exp = new Date(Date.now() + 30_000).toISOString()
    configureApiClient({
      getToken: () => 'olvs_x',
      refreshSession,
      getExpiresAt: () => exp,
    })
    globalThis.fetch = vi.fn(async () => {
      exp = new Date(Date.now() + 3_600_000).toISOString()
      return new Response(JSON.stringify({ ok: true }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }) as never
    await Promise.all(['/a', '/b', '/c', '/d', '/e'].map((r) => http.get(r)))
    expect(refrescos).toBe(1)
  })

  it('ensureFreshSession renueva para los caminos que rodean apiFetch', async () => {
    // Las descargas (CSV, NDJSON, PDF, bundle) no pasan por `apiFetch`. Sin esta puerta
    // serían las ÚNICAS peticiones de la consola que siguen muriendo por caducidad.
    const refreshSession = vi.fn(async () => true)
    configureApiClient({
      getToken: () => 'olvs_x',
      refreshSession,
      getExpiresAt: () => new Date(Date.now() + 30_000).toISOString(),
    })
    await ensureFreshSession()
    expect(refreshSession).toHaveBeenCalledTimes(1)

    // Y el control: con margen de sobra no toca la credencial.
    refreshSession.mockClear()
    __resetRefreshState()
    configureApiClient({
      getExpiresAt: () => new Date(Date.now() + 3_600_000).toISOString(),
    })
    await ensureFreshSession()
    expect(refreshSession).not.toHaveBeenCalled()
  })

  it('does NOT call onUnauthorized for an anonymous 401 (e.g. a bad login)', async () => {
    const onUnauthorized = vi.fn()
    configureApiClient({ onUnauthorized })
    mock(401, { error: { code: 'unauthenticated', message: 'bad creds' } })
    await expect(
      apiFetch('/v1/auth/login', { method: 'POST', body: {}, anonymous: true }),
    ).rejects.toBeInstanceOf(ApiError)
    expect(onUnauthorized).not.toHaveBeenCalled()
  })

  it('exposes isForbidden on a 403', async () => {
    mock(403, { error: { code: 'forbidden', message: 'nope' } })
    await expect(http.get('/v1/access-edges')).rejects.toSatisfy(
      (e: unknown) => e instanceof ApiError && e.isForbidden,
    )
  })

  // A handler may attach EXTRA structured fields to the error envelope beyond
  // code/message — the workflow graph validator's `step_ref` anchors a
  // validation failure to one node of the DAG. They must survive to
  // ApiError.details, so a caller reads the machine-readable anchor instead of
  // re-parsing the human message (which is prose and may be reworded).
  it('carries extra structured error fields through to ApiError.details', async () => {
    mock(400, {
      error: {
        code: 'invalid_request',
        message: 'step deploy: depends_on references unknown step ghost',
        step_ref: 'deploy',
      },
    })
    await expect(http.get('/v1/m/orchestration/workflows/x')).rejects.toSatisfy(
      (e: unknown) =>
        e instanceof ApiError &&
        e.detailString('step_ref') === 'deploy' &&
        e.details.step_ref === 'deploy',
    )
  })

  it('leaves details empty (never code/message) when the envelope has no extras', async () => {
    mock(400, { error: { code: 'invalid_request', message: 'bad' } })
    await expect(http.get('/v1/access-edges')).rejects.toSatisfy(
      (e: unknown) =>
        e instanceof ApiError &&
        Object.keys(e.details).length === 0 &&
        e.detailString('step_ref') === undefined,
    )
  })

  it('reports a non-string or empty structured detail as absent', async () => {
    mock(400, {
      error: { code: 'invalid_request', message: 'bad', step_ref: 42 },
    })
    await expect(http.get('/v1/access-edges')).rejects.toSatisfy(
      (e: unknown) =>
        e instanceof ApiError && e.detailString('step_ref') === undefined,
    )
  })

  it('throws a NetworkError when the request never returns', async () => {
    globalThis.fetch = vi.fn(async () => {
      throw new TypeError('Failed to fetch')
    }) as never
    await expect(
      http.get('/v1/server-info', { anonymous: true }),
    ).rejects.toBeInstanceOf(NetworkError)
  })

  // The engine cannot serve `items: null` any more (core/api/listresponse.go plus
  // its static and clean-install gates). These pin the console's half of the same
  // contract: a violating response is REPORTED, never rendered as "empty".
  it('rejects a 200 whose list envelope carries items:null instead of crashing the view', async () => {
    mock(200, { items: null, has_more: false })
    await expect(http.get('/v1/m/compliance/evidence')).rejects.toSatisfy(
      (e: unknown) =>
        e instanceof ApiError &&
        e.code === 'invalid_response' &&
        e.status === 200 &&
        e.message.includes('/v1/m/compliance/evidence') &&
        e.detailString('path') === '/v1/m/compliance/evidence',
    )
  })

  it('does not substitute an empty list for the invalid response', async () => {
    // Masking would make "the server answered wrongly" and "you have no legal
    // holds" the same screen. In a compliance console that is the worse failure.
    mock(200, { items: null, has_more: false })
    await expect(http.get('/v1/m/compliance/holds')).rejects.toBeInstanceOf(
      ApiError,
    )
  })

  it('rejects every non-array items shape, not only null', async () => {
    // The original check named `null` because that was the shape the engine could
    // once produce. Any other wrong shape breaks the same promise, and a consumer
    // writing `data?.items ?? []` renders all of them as "you have no rows" — the
    // precise confusion this guard exists to prevent.
    for (const items of [{}, 'nope', 42, true] as unknown[]) {
      mock(200, { items, has_more: false })
      await expect(http.get('/v1/m/compliance/evidence')).rejects.toSatisfy(
        (e: unknown) => e instanceof ApiError && e.code === 'invalid_response',
      )
    }
  })

  it('passes a well-formed empty page through untouched', async () => {
    mock(200, { items: [], has_more: false })
    await expect(http.get('/v1/m/compliance/evidence')).resolves.toEqual({
      items: [],
      has_more: false,
    })
  })

  it('leaves a non-list response with an unrelated null field alone', async () => {
    // `acceptance_rate` is a declared value-or-nothing number; only the `items`
    // envelope key is a promise of an array.
    mock(200, { totals: { acceptance_rate: null }, items: [] })
    await expect(http.get('/v1/m/adoption/summary')).resolves.toEqual({
      totals: { acceptance_rate: null },
      items: [],
    })
  })
})
