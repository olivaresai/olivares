// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { finopsApi, finopsKeys } from './api'

/**
 * ⛔ ESTE TESTIGO MIRA LA PETICIÓN QUE SALE, y existe por la misma razón que el de
 *    `api-transport`: el motor sirve DOS conciliaciones de admisión y sólo una de ellas
 *    es una lectura.
 *
 *    `GET /admission/reconciliation` informa (permiso de lectura de presupuestos);
 *    `POST /admission/reconcile` barre retenciones vencidas y emite el hallazgo de
 *    postura (permiso de escritura). Una consola que llamara a la segunda movería el
 *    libro cada vez que alguien abre una pantalla. Por eso lo que se comprueba aquí es
 *    el VERBO y la RUTA, no que «hubo una llamada».
 */

let peticiones: Array<{ url: string; method: string }> = []

function capturaFetch(cuerpo: unknown): void {
  globalThis.fetch = vi.fn(async (url: string, init?: RequestInit) => {
    peticiones.push({ url: String(url), method: init?.method ?? 'GET' })
    return new Response(JSON.stringify(cuerpo), {
      status: 200,
      headers: new Headers({ 'Content-Type': 'application/json' }),
    })
  }) as never
}

afterEach(() => {
  peticiones = []
})

describe('la conciliación de admisión de la consola', () => {
  it('pide la LECTURA, con GET, y no el trabajo de escritura', async () => {
    capturaFetch({
      swept_expired: 0,
      active: 2,
      committed: 5,
      released: 1,
      expired_unsettled: 0,
      active_lapsed: 0,
      idempotency_orphans: 0,
      drift: false,
      note: 'reservation ledger matches commits and releases',
    })

    await finopsApi.admissionReconciliation({ tenant: 't-admision' })

    expect(peticiones).toHaveLength(1)
    expect(new URL(peticiones[0].url, 'http://test').pathname).toBe(
      '/v1/m/finops/admission/reconciliation',
    )
    expect(peticiones[0].method).toBe('GET')
  })

  it('devuelve la deriva tal como la publica el motor, sin recalcularla', async () => {
    capturaFetch({
      swept_expired: 0,
      active: 3,
      committed: 0,
      released: 0,
      expired_unsettled: 0,
      active_lapsed: 2,
      idempotency_orphans: 1,
      drift: true,
      note: 'reservation ledger drifted from caller settlement',
    })

    const informe = await finopsApi.admissionReconciliation({
      tenant: 't-admision',
    })

    expect(informe.drift).toBe(true)
    // Una retención vencida que el trabajo aún no barrió se lee aquí, y `swept_expired`
    // sigue en 0 justamente porque esta ruta no barre.
    expect(informe.active_lapsed).toBe(2)
    expect(informe.idempotency_orphans).toBe(1)
    expect(informe.swept_expired).toBe(0)
    expect(informe.finding_ref).toBeUndefined()
  })

  it('tiene clave de consulta propia por inquilino', () => {
    expect(finopsKeys.admissionReconciliation('t-admision')).toEqual([
      'finops',
      't-admision',
      'admission',
      'reconciliation',
    ])
  })
})
