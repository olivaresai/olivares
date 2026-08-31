// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repo root.
//
// ⛔ EL CÓDIGO DEL MOTOR SE CONSERVA, Y ÉSTE ES EL INVARIANTE QUE LO FIJA.
//
// `fetchSupportBundle` no puede usar el cliente JSON compartido porque la respuesta buena es
// BINARIA. De ahí venía el defecto: al reimplementar la lectura del sobre de error se fijaba
// `code: 'support_bundle_failed'` para CUALQUIER fallo y sólo se leía el mensaje.
//
// Consecuencia, y no es teórica: el handler responde 403 con `step_up_required`
// (`core/api/handlers_support_bundle.go:28-37`, con prueba de wire en
// `core/api/handlers_console_wave2_test.go:160-168`), pero `ApiError.isStepUpRequired` compara el
// **CÓDIGO** (`lib/api/errors.ts:71-79`). Con el código aplastado ese predicado era **falso por
// construcción**: la consola no podía distinguir una ceremonia pendiente de una negativa de rol
// en esta ruta, hiciera lo que hiciera la pantalla.
//
// Por eso la celda vive aquí y no en la pantalla: arreglar `system-health-tab` sin esto habría
// sido pintar una rama inalcanzable — que es exactamente lo que hice, y lo que el contraste
// Codex `sol max` demostró.
import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@/lib/api/errors'

vi.mock('@/stores/session', () => ({
  useSessionStore: { getState: () => ({ token: 't' }) },
}))
vi.mock('@/stores/tenant', () => ({
  useTenantStore: { getState: () => ({ activeTenant: 'acme' }) },
}))

import { fetchSupportBundle } from './api'

const respuesta = (status: number, cuerpo: string, tipo = 'application/json') =>
  new Response(cuerpo, {
    status,
    headers: { 'Content-Type': tipo, 'X-Request-ID': 'req-1' },
  })

const conFetch = (r: Response) => {
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => r),
  )
}

afterEach(() => vi.unstubAllGlobals())

describe('fetchSupportBundle conserva el código del motor', () => {
  it('un 403 `step_up_required` llega como ceremonia, no como fallo genérico', async () => {
    conFetch(
      respuesta(
        403,
        JSON.stringify({
          error: { code: 'step_up_required', message: 'assurance too low' },
        }),
      ),
    )

    const err = await fetchSupportBundle().catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    const api = err as ApiError
    // ⛔ LA ASERCIÓN QUE ESTABA ROTA: antes esto era `support_bundle_failed` y por tanto
    //    `isStepUpRequired` salía false con un 403 que SÍ era una ceremonia.
    expect(api.code).toBe('step_up_required')
    expect(api.isStepUpRequired).toBe(true)
    expect(api.status).toBe(403)
    expect(api.message).toBe('assurance too low')
  })

  it('y un 403 de ROL sigue siendo rol — el código NO se inventa en la otra dirección', async () => {
    // ⛔ CONTROL NEGATIVO. Sin él, «devuelve step_up_required» se cumpliría también con un
    //    fetcher que pusiera ese código a todo, que es el mismo defecto con otro literal.
    conFetch(
      respuesta(
        403,
        JSON.stringify({
          error: { code: 'forbidden', message: 'your role cannot do this' },
        }),
      ),
    )

    const api = (await fetchSupportBundle().catch(
      (e: unknown) => e,
    )) as ApiError
    expect(api.code).toBe('forbidden')
    expect(api.isStepUpRequired).toBe(false)
    expect(api.isForbidden).toBe(true)
  })

  it('un cuerpo que no es JSON no rompe: cae al texto del status', async () => {
    // El motivo por el que el parseo iba en un `try`: una pasarela puede contestar HTML.
    conFetch(respuesta(502, '<html>bad gateway</html>', 'text/html'))

    const api = (await fetchSupportBundle().catch(
      (e: unknown) => e,
    )) as ApiError
    expect(api).toBeInstanceOf(ApiError)
    expect(api.status).toBe(502)
    expect(api.message.length).toBeGreaterThan(0)
  })

  it('y el request-id del motor viaja con el error', async () => {
    // Sin esto, «conserva el código» podría cumplirse tirando el resto del contexto, y un
    // operador que llama a soporte se queda sin la única referencia que correlaciona su fallo.
    conFetch(
      respuesta(
        403,
        JSON.stringify({ error: { code: 'step_up_required', message: 'x' } }),
      ),
    )
    const api = (await fetchSupportBundle().catch(
      (e: unknown) => e,
    )) as ApiError
    expect(api.requestId).toBe('req-1')
  })
})
