// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// C07-04 — las diez rutas de `reporting` que la consola nunca llamaba.
//
// `modules/reporting` registra 15 rutas y el cliente llamaba 5. Entre las diez que faltaban está
// **el paquete de evidencia de auditoría firmado**, que es lo que se entrega a un auditor externo
// y no tenía superficie ninguna.
//
// ⛔ LA RUTA ES LA ASERCIÓN. Aquí no se mockea el cliente: corre el de verdad contra un `fetch`
// sustituido, así que lo que se afirma son los bytes que saldrían del navegador.
import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import { configureApiClient } from '@/lib/api/client'
import { ApiError, isOpenCoreSeam } from '@/lib/api/errors'
import { fetchReportTemplate, reportingApi } from './api'

let sentUrl = ''
let sentMethod = ''

function stubFetch(status = 200, payload: unknown = {}) {
  globalThis.fetch = vi.fn(async (url: string, init?: RequestInit) => {
    sentUrl = String(url)
    sentMethod = String(init?.method ?? 'GET')
    return new Response(JSON.stringify(payload), {
      status,
      headers: { 'Content-Type': 'application/json' },
    })
  }) as never
}

function path() {
  return new URL(sentUrl, 'https://console.invalid').pathname
}

afterEach(() => {
  configureApiClient({
    getToken: () => null,
    getTenant: () => null,
    onUnauthorized: () => {},
  })
  sentUrl = ''
  sentMethod = ''
})

const LLAMADAS: Array<{
  que: string
  invoca: () => Promise<unknown>
  metodo: string
  ruta: string
}> = [
  {
    que: 'la postura de cumplimiento en vivo',
    invoca: () => reportingApi.enterprisePosture(),
    metodo: 'GET',
    ruta: '/v1/m/reporting/enterprise/posture',
  },
  {
    que: 'el resumen ejecutivo de riesgo',
    invoca: () => reportingApi.enterpriseRisk(),
    metodo: 'GET',
    ruta: '/v1/m/reporting/enterprise/risk',
  },
  {
    que: 'el paquete de evidencia FIRMADO',
    invoca: () => reportingApi.enterpriseBundle(),
    metodo: 'GET',
    ruta: '/v1/m/reporting/enterprise/bundle',
  },
  {
    que: 'el historial de una programación',
    invoca: () =>
      reportingApi.scheduleRuns('sch-1', { tenant: 'tenant-contract' }),
    metodo: 'GET',
    ruta: '/v1/m/reporting/schedules/sch-1/runs',
  },
  {
    que: 'el artefacto de UNA ejecución',
    invoca: () => reportingApi.scheduleRun('sch-1', 'run-9'),
    metodo: 'GET',
    ruta: '/v1/m/reporting/schedules/sch-1/runs/run-9',
  },
  {
    que: 'leer la marca del tenant',
    invoca: () => reportingApi.branding(),
    metodo: 'GET',
    ruta: '/v1/m/reporting/branding',
  },
  {
    que: 'fijar la marca del tenant',
    invoca: () => reportingApi.setBranding({ header: 'ACME' }),
    metodo: 'PUT',
    ruta: '/v1/m/reporting/branding',
  },
  {
    que: 'subir una plantilla propia',
    invoca: () => reportingApi.setTemplate('posture', '<h1>x</h1>'),
    metodo: 'PUT',
    ruta: '/v1/m/reporting/templates/posture',
  },
  {
    que: 'volver a la plantilla integrada',
    invoca: () => reportingApi.deleteTemplate('posture'),
    metodo: 'DELETE',
    ruta: '/v1/m/reporting/templates/posture',
  },
]

describe('las diez rutas de reporting que no se llamaban, ahora en el cable', () => {
  it.each(LLAMADAS)('$que: $metodo $ruta', async ({ invoca, metodo, ruta }) => {
    stubFetch()
    await invoca()
    expect(sentMethod).toBe(metodo)
    expect(path()).toBe(ruta)
  })
})

describe('la frontera comercial no es una avería', () => {
  /**
   * EL CONTROL: los tres informes enterprise contestan **501** cuando el motor comercial no está
   * cableado (`modules/reporting/enterprise.go:104-106`, `writeNotWired`). Eso NO es un fallo del
   * servidor: es la frontera open-core, y quien lo consuma tiene que poder distinguirlo.
   *
   * Y el propio motor explica por qué importa (`enterprise.go:141-148`): un add-on caducado
   * llegaba antes como 500 «failed to build the report», así que **a un cliente que había pagado
   * todo menos ese add-on se le decía que el servidor estaba roto**.
   *
   * EL MUTANTE: que `isOpenCoreSeam` mire el MENSAJE en vez del estado. El mensaje es prosa y se
   * reescribe; el estado es el contrato.
   */
  it('un 501 se reconoce como frontera, por ESTADO y no por mensaje', async () => {
    stubFetch(501, {
      error: { message: 'the enterprise evidence bundle is not wired' },
    })
    let capturado: unknown
    try {
      await reportingApi.enterpriseBundle()
    } catch (e) {
      capturado = e
    }
    expect(capturado).toBeInstanceOf(ApiError)
    expect(isOpenCoreSeam(capturado)).toBe(true)
  })

  /**
   * ⛔ LA CASILLA QUE DE VERDAD SEPARA «POR ESTADO» DE «POR MENSAJE», y no estaba: la de arriba
   * usa un 501 cuyo mensaje dice «not wired», así que **una implementación que casara el MENSAJE
   * la pasaba igual**. Lo comprobé por mutación y sobrevivió — mi propia prueba no medía lo que
   * su comentario afirmaba.
   *
   * Aquí el 501 llega con una prosa que no menciona nada reconocible. Sigue siendo la frontera,
   * porque el ESTADO es el contrato y el mensaje se reescribe.
   */
  it('un 501 con OTRA prosa sigue siendo la frontera', async () => {
    stubFetch(501, { error: { message: 'no disponible en esta edición' } })
    let capturado: unknown
    try {
      await reportingApi.enterprisePosture()
    } catch (e) {
      capturado = e
    }
    expect(isOpenCoreSeam(capturado)).toBe(true)
  })

  /**
   * LA DIRECCIÓN QUE NO DEBE DISPARAR: un 500 de verdad NO es la frontera. Sin esta afirmación,
   * un `isOpenCoreSeam` que devolviera siempre `true` pasaría la casilla de arriba y la consola
   * se tragaría en silencio las averías reales.
   */
  it('un 500 de verdad NO se confunde con la frontera', async () => {
    stubFetch(500, { error: { message: 'boom' } })
    let capturado: unknown
    try {
      await reportingApi.enterpriseRisk()
    } catch (e) {
      capturado = e
    }
    expect(capturado).toBeInstanceOf(ApiError)
    expect(isOpenCoreSeam(capturado)).toBe(false)
  })
})

/**
 * ⛔ LAS PLANTILLAS NO CABEN EN LA TABLA DE ARRIBA, y su historia son DOS defectos que sólo
 *    existían porque ninguna pantalla las llamaba.
 *
 *    LEER: `handleGetTemplate` escribe `text/html`; el cliente compartido hace `JSON.parse` con un
 *    `catch` que deja `undefined`, así que `reportingApi.template` devolvía **`undefined` en el
 *    ÉXITO**. Ahora es `fetchReportTemplate`, con `fetch` propio.
 *
 *    ESCRIBIR: era `http.put(url, html)`, y el cliente le aplicaba `JSON.stringify`. El motor lee
 *    el cuerpo EN CRUDO (`io.ReadAll`) y persistía **la cadena JSON entrecomillada y escapada**
 *    como si fuese HTML. La plantilla de informe con la marca del cliente se guardaba corrompida.
 */
describe('las plantillas de informe', () => {
  let pedido = { url: '', method: '', body: '' }
  beforeEach(() => {
    pedido = { url: '', method: '', body: '' }
    vi.stubGlobal('fetch', (u: string, init?: RequestInit) => {
      pedido = {
        url: String(u),
        method: String(init?.method ?? 'GET'),
        body: typeof init?.body === 'string' ? init.body : '',
      }
      return Promise.resolve(
        new Response('<h1>x</h1>', {
          status: 200,
          headers: { 'Content-Type': 'text/html' },
        }),
      )
    })
  })
  afterEach(() => vi.unstubAllGlobals())

  it('la lectura devuelve el HTML, no undefined', async () => {
    const html = await fetchReportTemplate('posture')
    expect(html).toBe('<h1>x</h1>')
    expect(new URL(pedido.url, 'https://console.invalid').pathname).toBe(
      '/v1/m/reporting/templates/posture',
    )
  })

  /** Sin plantilla propia el motor da 404, y eso es el estado NORMAL, no un fallo. */
  it('un 404 es «no hay plantilla propia», no un error', async () => {
    vi.stubGlobal('fetch', () =>
      Promise.resolve(new Response('', { status: 404 })),
    )
    await expect(fetchReportTemplate('posture')).resolves.toBeNull()
  })

  /**
   * ⛔ EL CONTROL QUE MÁS IMPORTA: el cuerpo viaja VERBATIM. Con `JSON.stringify` el motor
   * guardaba `"<h1>x</h1>"` —con comillas— como si fuese el HTML.
   */
  it('la escritura manda el HTML verbatim, sin comillas JSON', async () => {
    await reportingApi.setTemplate('posture', '<h1>x</h1>')
    expect(pedido.method).toBe('PUT')
    expect(pedido.body).toBe('<h1>x</h1>')
    expect(pedido.body).not.toContain('\\"')
    expect(pedido.body.startsWith('"')).toBe(false)
  })
})
