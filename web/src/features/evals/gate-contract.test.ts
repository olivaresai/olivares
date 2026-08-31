// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// C07-04 — las dieciséis rutas de evals que la consola nunca llamaba, contrastadas contra el
// contrato que el motor EXIGE.
//
// ⛔ POR QUÉ ESTE FICHERO NO MOCKEA `./api`, que es lo que haría el camino corto: `evals.test.tsx`
// hace `vi.mock('./api')` y por tanto afirma contra un doble que acepta lo que la vista le dé. Ese
// doble estuvo contento durante meses mientras el endpoint real contestaba 400 — está escrito en
// `ab-contract.test.ts:5-9`. Aquí corre el cliente REAL contra `http.*`, y sólo se sustituye
// `fetch`, así que la aserción es sobre **los bytes que saldrían del navegador**.
//
// ⛔ Y LA RUTA ES LA ASERCIÓN, no que la función se llamara. Un botón cableado al generador
// equivocado manda una petición bien formada a otro sitio: el mismo defecto que el contraste
// the model encontró en la pestaña de regops (F4), donde el botón de OSCAL apuntaba a
// `generateUsLawPack` y las 40 casillas seguían verdes.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { configureApiClient } from '@/lib/api/client'
import { evalsApi } from './api'
import { veredictoRegresion } from './components'

let sentUrl = ''
let sentMethod = ''
let sentBody: unknown
const tenantOptions = { tenant: 'tenant-test' } as const

function stubFetch(status = 200, payload: unknown = {}) {
  globalThis.fetch = vi.fn(async (url: string, init?: RequestInit) => {
    sentUrl = String(url)
    sentMethod = String(init?.method ?? 'GET')
    sentBody =
      init?.body === undefined ? undefined : JSON.parse(String(init.body))
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
  sentBody = undefined
})

/** Cada llamada nueva, con la ruta EXACTA que `modules/evals/evals.go:182-221` registra.
 *  Un desajuste aquí es una petición bien formada a la operación equivocada. */
const LLAMADAS: Array<{
  que: string
  invoca: () => Promise<unknown>
  metodo: string
  ruta: string
}> = [
  {
    que: 'el detalle de un suite',
    invoca: () => evalsApi.suite('s-1'),
    metodo: 'GET',
    ruta: '/v1/m/evals/suites/s-1',
  },
  {
    que: 'los casos dorados de un suite',
    invoca: () => evalsApi.suiteCases('s-1'),
    metodo: 'GET',
    ruta: '/v1/m/evals/suites/s-1/cases',
  },
  {
    que: 'archivar un suite',
    invoca: () => evalsApi.archiveSuite('s-1'),
    metodo: 'POST',
    ruta: '/v1/m/evals/suites/s-1/archive',
  },
  {
    que: 'el monitor de sesiones reales',
    invoca: () => evalsApi.monitor({ suite_ref: 's-1' }),
    metodo: 'POST',
    ruta: '/v1/m/evals/monitor',
  },
  {
    que: 'fijar el baseline (ADMIN)',
    invoca: () =>
      evalsApi.pinBaseline({
        suite_ref: 's-1',
        subject_ref: 'agent-a',
        run_ref: 'r-9',
      }),
    metodo: 'POST',
    ruta: '/v1/m/evals/baselines',
  },
  {
    que: 'los items etiquetados por humanos',
    invoca: () => evalsApi.calibrationItems('set-1', tenantOptions),
    metodo: 'GET',
    ruta: '/v1/m/evals/calibration/items',
  },
  {
    que: 'etiquetar items',
    invoca: () =>
      evalsApi.addCalibrationItems({
        set_name: 'set-1',
        items: [{ case_key: 'c1', output: 'o', human_passed: true }],
      }),
    metodo: 'POST',
    ruta: '/v1/m/evals/calibration/items',
  },
  {
    que: 'los informes de calibración',
    invoca: () => evalsApi.calibrationReports({ set: 'set-1' }),
    metodo: 'GET',
    ruta: '/v1/m/evals/calibration/reports',
  },
  {
    que: 'medir al juez',
    invoca: () => evalsApi.runCalibration({ set_name: 'set-1' }, tenantOptions),
    metodo: 'POST',
    ruta: '/v1/m/evals/calibration/run',
  },
  {
    que: 'listar el gate de CI',
    invoca: () => evalsApi.gates({ suite_ref: 's-1' }),
    metodo: 'GET',
    ruta: '/v1/m/evals/gate',
  },
  {
    que: 'ejecutar el gate de CI',
    invoca: () => evalsApi.runGate({ suite_ref: 's-1', outputs: { c1: 'o' } }),
    metodo: 'POST',
    ruta: '/v1/m/evals/gate',
  },
  {
    que: 'leer UNA evaluación del gate',
    invoca: () => evalsApi.gate('g-1'),
    metodo: 'GET',
    ruta: '/v1/m/evals/gate/g-1',
  },
  {
    que: 'anular un gate (ADMIN)',
    invoca: () =>
      evalsApi.overrideGate('g-1', 'release bloqueada por un falso positivo'),
    metodo: 'POST',
    ruta: '/v1/m/evals/gate/g-1/override',
  },
]

const RUN_BASE = { regressed: false, n_scored: 40, baseline_ref: 'run-0' }

describe('las rutas que la consola no llamaba, ahora en el cable', () => {
  it.each(LLAMADAS)('$que: $metodo $ruta', async ({ invoca, metodo, ruta }) => {
    stubFetch()
    await invoca()
    expect(sentMethod).toBe(metodo)
    expect(path()).toBe(ruta)
  })
})

describe('el cuerpo que el motor EXIGE', () => {
  /**
   * EL CONTROL: el motor rechaza con 400 un motivo vacío
   * (`modules/evals/gate.go:588` — «a written reason is required to override a gate»), así que
   * el motivo tiene que viajar en el cuerpo con ESE nombre.
   *
   * EL MUTANTE: mandar `{ motivo }` en vez de `{ reason }`, o mandarlo por query. El motor
   * contestaría 400 y la anulación no ocurriría — con la consola diciendo que sí.
   */
  it('la anulación manda el motivo escrito como `reason`', async () => {
    stubFetch()
    await evalsApi.overrideGate('g-1', 'el fallo era del runner, no del modelo')
    expect(sentBody).toEqual({
      reason: 'el fallo era del runner, no del modelo',
    })
  })

  /**
   * EL CONTROL: el id va PERCODIFICADO en la ruta. Un id con `/` o `?` sin codificar cambiaría
   * la ruta de destino, que es la clase de defecto que esta batería existe para ver.
   */
  it('codifica el id en la ruta en vez de pegarlo', async () => {
    stubFetch()
    await evalsApi.gate('g 1/2')
    expect(path()).toBe('/v1/m/evals/gate/g%201%2F2')
  })

  /**
   * EL CONTROL: fijar un baseline manda las TRES referencias. El motor las lee del cuerpo
   * (`modules/evals/baselines.go:20-24`); si falta `run_ref`, el baseline apuntaría a nada y la
   * detección de regresión se quedaría sin referencia — silenciosamente.
   */
  it('fijar el baseline manda suite, sujeto y run', async () => {
    stubFetch()
    await evalsApi.pinBaseline({
      suite_ref: 's-1',
      subject_ref: 'agent-a',
      run_ref: 'r-9',
    })
    expect(sentBody).toEqual({
      suite_ref: 's-1',
      subject_ref: 'agent-a',
      run_ref: 'r-9',
    })
  })

  /**
   * EL CONTROL, y es el que separa esta pantalla de una que miente: el gate tiene DOS
   * veredictos. `verdict` es lo que midió; `effective_verdict` es lo que CI obedece — el mismo,
   * o `pass` tras una anulación gobernada (`gate.go:70-72`). El cliente no los funde: devuelve
   * el DTO entero y la pantalla decide qué enseña.
   *
   * EL MUTANTE: que el cliente proyecte sólo `effective_verdict`. Una release desbloqueada por
   * una persona se leería como una que pasó por sí sola.
   */
  it('conserva los dos veredictos, el medido y el efectivo', async () => {
    stubFetch(200, {
      id: 'g-1',
      suite_ref: 's-1',
      verdict: 'fail',
      effective_verdict: 'pass',
      overridden: true,
      override_by: 'ciso@example.com',
      override_reason: 'el fallo era del runner',
      sampled: 10,
      total_cases: 40,
      occurred_at: '2026-08-17T10:00:00Z',
    })
    const g = await evalsApi.gate('g-1')
    expect(g.verdict).toBe('fail')
    expect(g.effective_verdict).toBe('pass')
    expect(g.overridden).toBe(true)
    expect(g.override_reason).toBe('el fallo era del runner')
  })
})

describe('la tasa de aprobados no viaja sin su denominador', () => {
  /**
   * ⛔ EL CONTROL: el cliente CONSERVA `n_scored`, que es el denominador detrás de `pass_rate`.
   * El motor lo manda a propósito y dice por qué (`modules/evals/runs.go:400-402`): «reported so
   * a reader can weigh the aggregate — **n=2 and n=200 are different claims**».
   *
   * La consola lo ignoraba: la columna de la tabla pintaba «100 %» sin decir sobre cuántos casos,
   * así que un 100 % sobre DOS casos y otro sobre DOSCIENTOS se leían idénticos. Quien decide una
   * release con eso decide sobre una cifra sin método.
   *
   * EL MUTANTE: que el cliente proyecte sólo `pass_rate`. La columna no tendría qué enseñar y la
   * cifra volvería a quedarse sola.
   */
  it('conserva n_scored y el intervalo junto a la tasa', async () => {
    stubFetch(200, {
      id: 'r-1',
      suite_ref: 's-1',
      pass_rate: 1,
      score: 1,
      n_scored: 2,
      pass_rate_ci: { lo: 0.34, hi: 1 },
      total: 2,
      passed: 2,
      failed: 0,
      errors: 0,
      skipped: 0,
    })
    const r = await evalsApi.run('r-1')
    expect(r.pass_rate).toBe(1)
    // Una tasa del 100 % sobre n=2 con un intervalo que baja al 34 %: la cifra sola miente.
    expect(r.n_scored).toBe(2)
    expect(r.pass_rate_ci).toEqual({ lo: 0.34, hi: 1 })
  })

  /**
   * LA DIRECCIÓN QUE NO DEBE DISPARAR: cuando no se puntuó nada el intervalo llega NULO, y eso
   * NO es un intervalo de cero. El cliente lo pasa tal cual en vez de inventarse uno.
   */
  it('un intervalo ausente llega nulo, no como cero', async () => {
    stubFetch(200, {
      id: 'r-2',
      suite_ref: 's-1',
      pass_rate: 0,
      score: 0,
      n_scored: 0,
      pass_rate_ci: null,
      total: 0,
      passed: 0,
      failed: 0,
      errors: 0,
      skipped: 0,
    })
    const r = await evalsApi.run('r-2')
    expect(r.n_scored).toBe(0)
    expect(r.pass_rate_ci).toBeNull()
  })
  /**
   * ⛔ EL HALLAZGO MÁS GRAVE DE ESTA TABLA, y estaba vivo: `regressed: false` significa CUATRO
   * cosas y la columna las pintaba todas igual. `resolveRegression` (`runs.go:257-276`) sale con
   * el veredicto vacío en TRES caminos antes de comparar nada — no se puntuó, la suite no
   * comprueba regresiones, o no había línea base.
   *
   * EL MUTANTE: volver al ternario `regressed ? … : 'sin regresión'`. Una ejecución que **no midió
   * nada** se lee como una que midió y salió bien, en la señal que decide si una release sale.
   * Es la tasa sin denominador otra vez, sobre la puerta de calidad.
   */
  it('una ejecución sin puntuar no se lee como «sin regresión»', () => {
    const fila = {
      ...RUN_BASE,
      regressed: false,
      n_scored: 0,
      baseline_ref: undefined,
    }
    expect(veredictoRegresion(fila)).toBe('notScored')
  })

  /** Sin línea base NO hubo comparación, y eso tampoco es un aprobado. */
  it('sin línea base se dice que no se comparó nada', () => {
    const fila = {
      ...RUN_BASE,
      regressed: false,
      n_scored: 40,
      baseline_ref: undefined,
    }
    expect(veredictoRegresion(fila)).toBe('noBaseline')
  })

  /**
   * LA DIRECCIÓN QUE NO DEBE DISPARAR: puntuada y con línea base, «sin regresión» es la lectura
   * correcta. Sin esta casilla, una columna que nunca dijera «sin regresión» pasaría las dos de
   * arriba y borraría la única lectura que sí es un aprobado.
   */
  it('puntuada y con línea base, sí es «sin regresión»', () => {
    const fila = {
      ...RUN_BASE,
      regressed: false,
      n_scored: 40,
      baseline_ref: 'run-anterior',
    }
    expect(veredictoRegresion(fila)).toBe('noRegression')
  })
})
