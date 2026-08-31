// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { __resetRefreshState, apiFetch, configureApiClient } from './client'

/**
 * La renovación no se renueva a sí misma.
 *
 * ⛔ EL PUNTO DE ESTOS CASOS ES EL CABLEADO REAL. Los testigos que ya había sustituyen
 *    `refreshSession` por una función que resuelve un booleano y no vuelve a entrar en el cliente
 *    — y por eso el defecto vivió aquí sin que nadie lo viera. En producción la renovación es otra
 *    `apiFetch('/v1/auth/refresh')` (`app/providers.tsx`), así que la forma del test tiene que ser
 *    ésa o no está midiendo el sistema.
 */

let envios: string[] = []

function respondeSiempre200(): void {
  globalThis.fetch = vi.fn(async (url: string) => {
    envios.push(String(url))
    return new Response(
      JSON.stringify({
        token: 'olvs_nuevo',
        session_id: 's1',
        expires_at: new Date(Date.now() + 30_000).toISOString(),
      }),
      {
        status: 200,
        headers: new Headers({ 'Content-Type': 'application/json' }),
      },
    )
  }) as never
}

/** El cableado de `app/providers.tsx`: la renovación es OTRA petición del mismo cliente. */
function cableadoReal(veces: { n: number }): () => Promise<boolean> {
  return async () => {
    veces.n++
    await apiFetch('/v1/auth/refresh', { method: 'POST' })
    return true
  }
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
  envios = []
})

describe('la renovación no se renueva a sí misma', () => {
  it('con el cableado REAL, una petición hace UNA renovación y DOS envíos', async () => {
    const veces = { n: 0 }
    respondeSiempre200()
    configureApiClient({
      getToken: () => 'olvs_abc',
      getTenant: () => 'org-A',
      // Siempre dentro del margen: es el estado en que la renovación se dispara, y era el que
      // convertía cada renovación en la siguiente.
      getExpiresAt: () => new Date(Date.now() + 30_000).toISOString(),
      refreshSession: cableadoReal(veces),
    })

    await apiFetch('/v1/agents')

    // Cotas: la renovación OCURRIÓ (si no, el caso no mide la reentrada) …
    expect(veces.n).toBe(1)
    // … y salieron exactamente dos peticiones: el refresco y la de verdad.
    expect(envios).toEqual(['/v1/auth/refresh', '/v1/agents'])
  })

  it('la propia petición de refresco no dispara una renovación previa', async () => {
    const veces = { n: 0 }
    respondeSiempre200()
    configureApiClient({
      getToken: () => 'olvs_abc',
      getTenant: () => 'org-A',
      getExpiresAt: () => new Date(Date.now() + 30_000).toISOString(),
      refreshSession: cableadoReal(veces),
    })

    await apiFetch('/v1/auth/refresh', { method: 'POST' })

    expect(veces.n).toBe(0)
    expect(envios).toEqual(['/v1/auth/refresh'])
  })

  it('CONTRAFACTUAL · otra ruta de auth SÍ renueva antes (el predicado discrimina)', async () => {
    const veces = { n: 0 }
    respondeSiempre200()
    configureApiClient({
      getToken: () => 'olvs_abc',
      getTenant: () => 'org-A',
      getExpiresAt: () => new Date(Date.now() + 30_000).toISOString(),
      refreshSession: cableadoReal(veces),
    })

    // ⛔ Vecina de la de refresco pero distinta. El predicado compara la ruta EXACTA —los
    //    parámetros van aparte por `opts.query`—, así que una hermana como
    //    `/v1/auth/refresh-policy` sí renovaría. Medido en el motor: la única ruta REGISTRADA bajo
    //    ese prefijo es `POST /auth/refresh` (`core/api/server.go`); el literal aparece además en
    //    OpenAPI, SDK generados y tests, que no son rutas.
    await apiFetch('/v1/auth/login', { method: 'POST', body: {} })

    expect(veces.n).toBe(1)
  })

  it('si el REFRESCO responde 401 dentro de otra petición, todo termina — no se queda colgado', async () => {
    // ⛔ ESTE CASO LO SEÑALÓ EL CONTRASTE, y el fallo que vigila NO se ve contando envíos: se ve
    //    porque la promesa **nunca resuelve**. Un mutante de una condición
    //    (`esLaRutaDeRenovacion(path) && !refreshInFlight`) deja pasar exactamente este camino
    //    —la exclusión no aplica porque ya hay vuelo— y entonces el 401 del propio refresco pide
    //    `refreshOnce()`, que le devuelve el vuelo del que forma parte: espera circular. Los 41
    //    casos anteriores seguían verdes con ese mutante puesto.
    const veces = { n: 0 }
    globalThis.fetch = vi.fn(async (url: string) => {
      envios.push(String(url))
      return new Response(
        JSON.stringify({ error: { code: 'unauthenticated', message: 'no' } }),
        {
          status: 401,
          headers: new Headers({ 'Content-Type': 'application/json' }),
        },
      )
    }) as never
    configureApiClient({
      getToken: () => 'olvs_abc',
      getTenant: () => 'org-A',
      onUnauthorized: () => {},
      refreshSession: cableadoReal(veces),
    })

    const carrera = await Promise.race([
      apiFetch('/v1/agents').then(
        () => 'resuelta',
        () => 'rechazada',
      ),
      new Promise<string>((r) => setTimeout(() => r('COLGADA'), 300)),
    ])

    // Cotas: la renovación se intentó (o sea, el camino se ejerció) y hubo envíos.
    expect(veces.n).toBe(1)
    expect(envios.length).toBeGreaterThanOrEqual(2)
    // Y el veredicto: termina. Un 401 es un final; quedarse pendiente no lo es.
    expect(carrera).toBe('rechazada')
  })

  it('cinco peticiones a la vez comparten UNA sola renovación', async () => {
    const veces = { n: 0 }
    respondeSiempre200()
    configureApiClient({
      getToken: () => 'olvs_abc',
      getTenant: () => 'org-A',
      getExpiresAt: () => new Date(Date.now() + 30_000).toISOString(),
      refreshSession: cableadoReal(veces),
    })

    await Promise.all([
      apiFetch('/v1/a'),
      apiFetch('/v1/b'),
      apiFetch('/v1/c'),
      apiFetch('/v1/d'),
      apiFetch('/v1/e'),
    ])

    expect(veces.n).toBe(1)
    expect(envios).toHaveLength(6) // el refresco + las cinco
  })

  it('un `refreshSession` que pide OTRA ruta no arranca una segunda renovación', async () => {
    // ⛔ ÉSTE VIGILA LA VENTANA SÍNCRONA, que la guarda de ruta NO cubre. Una renovación que
    //    aproveche para releer algo —`/v1/whoami`, digamos— llama a `apiFetch` desde su propio
    //    prefijo síncrono, y esa ruta no está excluida: sin la bandera de fase,
    //    `refreshInFlight` todavía vale `null` ahí dentro y arranca otra renovación, y otra.
    //    Medido sin la bandera: 50 renovaciones y 49 envíos antes de rendirse.
    const veces = { n: 0 }
    respondeSiempre200()
    configureApiClient({
      getToken: () => 'olvs_abc',
      getTenant: () => 'org-A',
      getExpiresAt: () => new Date(Date.now() + 30_000).toISOString(),
      refreshSession: async () => {
        veces.n++
        await apiFetch('/v1/whoami')
        return true
      },
    })

    await apiFetch('/v1/agents')

    // Cotas: se renovó UNA vez y el número de envíos es el de las tres peticiones reales.
    expect(veces.n).toBe(1)
    expect(envios).toEqual(['/v1/whoami', '/v1/agents'])
  })

  it('una petición ANÓNIMA no puede colar Authorization ni el inquilino por `headers`', async () => {
    respondeSiempre200()
    configureApiClient({ getToken: () => 'olvs_abc', getTenant: () => 'org-A' })
    let vistas: Headers | undefined
    globalThis.fetch = vi.fn(async (url: string, init?: RequestInit) => {
      envios.push(String(url))
      vistas = new Headers(init?.headers)
      return new Response('{}', {
        status: 200,
        headers: new Headers({ 'Content-Type': 'application/json' }),
      })
    }) as never

    await apiFetch('/v1/server-info', {
      anonymous: true,
      headers: {
        Authorization: 'Bearer robado',
        'X-Olivares-Tenant': 'org-de-otro',
        'X-Algo-Legitimo': 'sí',
      },
    })

    // Cota: la petición salió y sus otras cabeceras siguen ahí (no se barrió de más).
    expect(envios).toHaveLength(1)
    expect(vistas?.get('X-Algo-Legitimo')).toBe('sí')
    expect(vistas?.get('Authorization')).toBeNull()
    expect(vistas?.get('X-Olivares-Tenant')).toBeNull()
  })
})
