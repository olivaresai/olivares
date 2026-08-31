// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { __resetRefreshState, apiFetch, configureApiClient } from './client'

/**
 * El inquilino se fija AL ENTRAR en la petición, no al componer las cabeceras.
 *
 * Las dos esperas que hacen falta para que esto importe las crea este mismo fichero: el
 * `await refreshOnce()` preventivo y el replay tras un 401. Los casos de abajo cambian el
 * inquilino DURANTE esas esperas y exigen que la petición siga diciendo el de antes — porque el
 * acto se pidió estando allí. En una escritura, la alternativa es aplicarla en el inquilino
 * equivocado.
 */

/** Inquilino "vivo": lo que devolvería el store en cada instante. */
let inquilinoVivo: string | null = 'inquilino-A'
const cabeceras: Array<string | null> = []

function respondeCon(
  secuencia: Array<{ status: number; body?: unknown }>,
): void {
  let i = 0
  globalThis.fetch = vi.fn(async (_url: string, init?: RequestInit) => {
    cabeceras.push(new Headers(init?.headers).get('X-Olivares-Tenant'))
    const paso = secuencia[Math.min(i, secuencia.length - 1)]
    i++
    return new Response(
      paso.body === undefined ? null : JSON.stringify(paso.body),
      {
        status: paso.status,
        headers: new Headers({ 'Content-Type': 'application/json' }),
      },
    )
  }) as never
}

/**
 * Un `refreshSession` que AVISA cuando empieza y espera a que lo sueltes.
 *
 * ⛔ La señal `empezado` no es adorno: sin ella el caso tendría que contar microturnos hasta
 *    creer que la renovación está en curso, y ese número depende de cuántos consuman el mock de
 *    `fetch` y `Response.text()`. Con un número de menos, el cambio de inquilino ocurriría ANTES
 *    de la renovación y el caso pasaría midiendo otra cosa.
 */
function renovacionControlada(): {
  fn: () => Promise<boolean>
  empezado: Promise<void>
  abre: () => void
  veces: () => number
} {
  let avisa!: () => void
  let abre!: () => void
  let veces = 0
  const empezado = new Promise<void>((r) => {
    avisa = r
  })
  const puerta = new Promise<boolean>((r) => {
    abre = () => r(true)
  })
  return {
    fn: () => {
      veces++
      avisa()
      return puerta
    },
    empezado,
    abre,
    veces: () => veces,
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
  inquilinoVivo = 'inquilino-A'
  cabeceras.length = 0
})

describe('el inquilino se fija al entrar, no al enviar', () => {
  it('VENTANA 1 · un cambio durante la renovación preventiva NO cambia la cabecera', async () => {
    const renov = renovacionControlada()
    configureApiClient({
      getToken: () => 'olvs_abc',
      getTenant: () => inquilinoVivo,
      // Caduca dentro del margen de 120 s ⇒ `caducaPronto()` es cierto y la petición espera.
      getExpiresAt: () => new Date(Date.now() + 30_000).toISOString(),
      refreshSession: renov.fn,
    })
    respondeCon([{ status: 200, body: { ok: true } }])

    const p = apiFetch('/v1/agents', { method: 'DELETE' })
    await renov.empezado

    // ⛔ COTAS ANTES DEL VEREDICTO: la renovación empezó y la petición TODAVÍA no ha salido.
    //    Sin esto, un mutante que quitara la renovación preventiva entera dejaría el caso verde
    //    —saldría con A por llegar antes del cambio— sin medir nada.
    expect(renov.veces()).toBe(1)
    expect(cabeceras).toHaveLength(0)

    inquilinoVivo = 'inquilino-B' // el operador cambia MIENTRAS se renueva
    renov.abre()
    await p

    expect(cabeceras).toEqual(['inquilino-A'])
  })

  it('VENTANA 3 · el replay tras un 401 repite el inquilino ORIGINAL, no el nuevo', async () => {
    const renov = renovacionControlada()
    configureApiClient({
      getToken: () => 'olvs_abc',
      getTenant: () => inquilinoVivo,
      refreshSession: renov.fn,
    })
    respondeCon([
      {
        status: 401,
        body: { error: { code: 'unauthenticated', message: 'no' } },
      },
      { status: 200, body: { ok: true } },
    ])

    const p = apiFetch('/v1/agents/a1', { method: 'DELETE' })
    // Sin contar microturnos: la renovación reactiva sólo empieza DESPUÉS del 401.
    await renov.empezado

    // Cotas: el primer envío ya ocurrió y llevaba A; el segundo todavía no.
    expect(cabeceras).toEqual(['inquilino-A'])

    inquilinoVivo = 'inquilino-B' // cambia mientras se renueva, antes del replay
    renov.abre()
    await p

    // Hubo DOS envíos —o sea, el replay ocurrió— y los dos dijeron lo mismo.
    expect(cabeceras).toEqual(['inquilino-A', 'inquilino-A'])
  })

  it('CONTRAFACTUAL · sin espera de por medio, la petición usa el inquilino del momento', async () => {
    configureApiClient({
      getToken: () => 'olvs_abc',
      getTenant: () => inquilinoVivo,
    })
    respondeCon([{ status: 200, body: { ok: true } }])

    inquilinoVivo = 'inquilino-B' // cambió ANTES de pedir
    await apiFetch('/v1/agents')

    expect(cabeceras).toEqual(['inquilino-B'])
  })

  it('CONTRAFACTUAL · un `tenant` explícito sigue mandando sobre el fijado', async () => {
    configureApiClient({
      getToken: () => 'olvs_abc',
      getTenant: () => inquilinoVivo,
    })
    respondeCon([{ status: 200, body: { ok: true } }])

    await apiFetch('/v1/tokens', {
      method: 'POST',
      body: {},
      tenant: 'inquilino-Z',
    })

    expect(cabeceras).toEqual(['inquilino-Z'])
  })

  it('CONTRAFACTUAL · `tenant: null` sigue significando "sin cabecera", no "usa el activo"', async () => {
    configureApiClient({
      getToken: () => 'olvs_abc',
      getTenant: () => inquilinoVivo,
    })
    respondeCon([{ status: 200, body: { ok: true } }])

    await apiFetch('/v1/agents', { tenant: null })

    expect(cabeceras).toEqual([null])
  })

  it('CONTRAFACTUAL · una petición anónima sigue sin cabecera de inquilino', async () => {
    configureApiClient({
      getToken: () => 'olvs_abc',
      getTenant: () => inquilinoVivo,
    })
    respondeCon([{ status: 200, body: { setup_required: false } }])

    await apiFetch('/v1/server-info', { anonymous: true })

    expect(cabeceras).toEqual([null])
  })
})
