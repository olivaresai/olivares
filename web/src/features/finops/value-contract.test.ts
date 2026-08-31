// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// C07-04 — las doce rutas de `finops` que la consola nunca llamaba, y las dos formas de mentir
// con ellas.
//
// `modules/finops/api.go:52-113` registra 42 rutas y el cliente llamaba 30. Entre las doce que
// faltaban está el panel del CFO entero — gasto frente a resultados y la lista de riesgo de
// cancelación.
//
// ⛔ LA RUTA ES LA ASERCIÓN: corre el cliente REAL contra un `fetch` sustituido, así que lo que
// se afirma son los bytes que saldrían del navegador.
import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import { configureApiClient } from '@/lib/api/client'
import { fetchStatementExport, finopsApi } from './api'

let sentUrl = ''
let sentMethod = ''

function stubFetch(payload: unknown = {}) {
  globalThis.fetch = vi.fn(async (url: string, init?: RequestInit) => {
    sentUrl = String(url)
    sentMethod = String(init?.method ?? 'GET')
    return new Response(JSON.stringify(payload), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  }) as never
}

const url = () => new URL(sentUrl, 'https://console.invalid')

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
    que: 'el panel del CFO',
    invoca: () => finopsApi.valueSummary(),
    metodo: 'GET',
    ruta: '/v1/m/finops/value/summary',
  },
  {
    que: 'el desglose coste-por-resultado',
    invoca: () => finopsApi.value({ dimension: 'agent' }),
    metodo: 'GET',
    ruta: '/v1/m/finops/value',
  },
  {
    que: 'la evidencia de resultados',
    invoca: () => finopsApi.outcomes(undefined, { tenant: 't1' }),
    metodo: 'GET',
    ruta: '/v1/m/finops/outcomes',
  },
  {
    que: 'ingerir un resultado calificado',
    invoca: () =>
      finopsApi.ingestOutcome(
        {
          subject_kind: 'agent',
          subject_ref: 'a1',
          verdict: 'satisfied',
        },
        { tenant: 't1' },
      ),
    metodo: 'POST',
    ruta: '/v1/m/finops/outcomes',
  },
  {
    que: 'el gasto unificado entre superficies',
    invoca: () => finopsApi.spendUnified(),
    metodo: 'GET',
    ruta: '/v1/m/finops/spend/unified',
  },
  {
    que: 'ingerir una muestra de coste',
    invoca: () => finopsApi.ingestCost({ provider: 'anthropic' }),
    metodo: 'POST',
    ruta: '/v1/m/finops/cost',
  },
  {
    que: 'el snapshot de asientos',
    invoca: () =>
      finopsApi.upsertSeats({ provider: 'anthropic', day: '2026-08-17' }),
    metodo: 'POST',
    ruta: '/v1/m/finops/seats',
  },
  {
    que: 'la utilización de asientos',
    invoca: () =>
      finopsApi.seatUtilization({ provider: 'anthropic' }, { tenant: 't1' }),
    metodo: 'GET',
    ruta: '/v1/m/finops/seats/utilization',
  },
  {
    que: 'leer UN centro de coste',
    invoca: () => finopsApi.costCenter('cc-1'),
    metodo: 'GET',
    ruta: '/v1/m/finops/cost-centers/cc-1',
  },
  {
    que: 'leer UNA tarifa de modelo',
    invoca: () => finopsApi.modelRate('mr-1'),
    metodo: 'GET',
    ruta: '/v1/m/finops/model-rates/mr-1',
  },
  {
    que: 'leer UN presupuesto',
    invoca: () => finopsApi.budget('b-1'),
    metodo: 'GET',
    ruta: '/v1/m/finops/budgets/b-1',
  },
]

describe('las doce rutas de finops que no se llamaban, ahora en el cable', () => {
  it.each(LLAMADAS)('$que: $metodo $ruta', async ({ invoca, metodo, ruta }) => {
    stubFetch()
    await invoca()
    expect(sentMethod).toBe(metodo)
    expect(url().pathname).toBe(ruta)
  })
})

describe('las dos formas de infra-declarar la factura', () => {
  /**
   * ⛔ EL CONTROL QUE MÁS IMPORTA: `total_cost_micro_usd` es el gasto ENTERO — los cubos
   * atribuidos MÁS `unattributed_cost_micro_usd`. El motor lo declara en `dto.go:236-238`:
   * `sum(buckets[].cost_micro_usd) + unattributed == total`.
   *
   * Esta casilla fija que el cliente **entrega el total y lo no atribuido tal cual**, sin
   * proyectar ni recalcular. Una pantalla que sume los cubos y llame a eso «el gasto»
   * infra-declara la factura, y lo hace en la dirección cómoda — hacia abajo.
   *
   * EL MUTANTE: que el cliente devolviera sólo `buckets`. El total dejaría de estar disponible
   * y quien pintase la pantalla no tendría más remedio que sumarlos.
   */
  it('entrega el total Y lo no atribuido, no sólo los cubos', async () => {
    stubFetch({
      dimension: 'agent',
      total_cost_micro_usd: 1_000_000,
      unattributed_cost_micro_usd: 400_000,
      total_value_micro_usd: 0,
      net_value_micro_usd: -1_000_000,
      total_outcomes: 0,
      buckets: [
        {
          key: 'agent-a',
          cost_micro_usd: 600_000,
          value_micro_usd: 0,
          net_value_micro_usd: -600_000,
          outcomes: 0,
          satisfied: 0,
          unsatisfied: 0,
          satisfied_rate_pct: 0,
          has_outcomes: false,
          cancellation_risk: true,
          risk_reason: 'spend with no graded outcomes',
        },
      ],
    })
    const v = await finopsApi.value({ dimension: 'agent' })
    expect(v.total_cost_micro_usd).toBe(1_000_000)
    expect(v.unattributed_cost_micro_usd).toBe(400_000)
    // La identidad que el motor promete, comprobada aquí para que se note si alguna vez deja
    // de cumplirse: los cubos por sí solos NO son el gasto.
    const sumaCubos = v.buckets.reduce((a, b) => a + b.cost_micro_usd, 0)
    expect(sumaCubos).toBe(600_000)
    expect(sumaCubos + (v.unattributed_cost_micro_usd ?? 0)).toBe(
      v.total_cost_micro_usd,
    )
  })

  /**
   * ⛔ EL SEGUNDO CONTROL: `has_outcomes: false` no es `outcomes: 0`.
   *
   * El primero dice «no tenemos datos de resultado para esto»; el segundo, «los medimos y
   * salieron cero». Un panel que los funda pinta un coste-por-resultado de algo que nunca
   * midió — y encima lo marca como riesgo de cancelación por una ausencia de dato.
   *
   * El cliente conserva los dos campos por separado; esta casilla lo fija.
   */
  it('conserva la diferencia entre «no medido» y «cero»', async () => {
    stubFetch({
      dimension: 'agent',
      total_cost_micro_usd: 10,
      total_value_micro_usd: 0,
      net_value_micro_usd: -10,
      total_outcomes: 0,
      buckets: [
        {
          key: 'sin-datos',
          cost_micro_usd: 5,
          value_micro_usd: 0,
          net_value_micro_usd: -5,
          outcomes: 0,
          satisfied: 0,
          unsatisfied: 0,
          satisfied_rate_pct: 0,
          has_outcomes: false,
          cancellation_risk: false,
        },
        {
          key: 'medido-en-cero',
          cost_micro_usd: 5,
          value_micro_usd: 0,
          net_value_micro_usd: -5,
          outcomes: 0,
          satisfied: 0,
          unsatisfied: 0,
          satisfied_rate_pct: 0,
          has_outcomes: true,
          cancellation_risk: true,
        },
      ],
    })
    const v = await finopsApi.value()
    const sinDatos = v.buckets.find((b) => b.key === 'sin-datos')
    const medido = v.buckets.find((b) => b.key === 'medido-en-cero')
    expect(sinDatos?.outcomes).toBe(0)
    expect(medido?.outcomes).toBe(0)
    // Mismo `outcomes`, distinto significado. Si el cliente proyectara uno solo de los dos
    // campos, esta afirmación no podría escribirse.
    expect(sinDatos?.has_outcomes).toBe(false)
    expect(medido?.has_outcomes).toBe(true)
  })

  /**
   * EL CONTROL: la dimensión viaja como query, que es como el motor la lee. Mandarla en la
   * ruta daría 404 y la pantalla se quedaría sin desglose sin decir por qué.
   */
  it('manda la dimensión y la ventana como query', async () => {
    stubFetch()
    await finopsApi.valueSummary({
      dimension: 'identity',
      since: '2026-08-01',
      until: '2026-08-17',
    })
    expect(url().pathname).toBe('/v1/m/finops/value/summary')
    expect(url().searchParams.get('dimension')).toBe('identity')
    expect(url().searchParams.get('since')).toBe('2026-08-01')
    expect(url().searchParams.get('until')).toBe('2026-08-17')
  })
})

/**
 * ⛔ EL EXTRACTO NO CABE EN LA TABLA DE ARRIBA, y por eso estaba roto: `handleExportStatement`
 *    (`modules/finops/statements.go`) escribe `text/csv` **sin ninguna rama por formato** — su
 *    único `writeJSON` es el 400 de id inválido—. El cliente compartido hace `JSON.parse(text)`
 *    dentro de un `catch` que deja `parsed = undefined`, así que `exportStatement` devolvía
 *    **nada en el caso de ÉXITO**, declarándose además como `string`: el tipo correcto para un
 *    cuerpo que jamás llegaba.
 */
describe('el extracto de chargeback', () => {
  let pedido = ''
  beforeEach(() => {
    pedido = ''
    vi.stubGlobal('fetch', (u: string) => {
      pedido = String(u)
      return Promise.resolve(
        new Response('a,b\n1,2\n', {
          status: 200,
          headers: { 'Content-Type': 'text/csv' },
        }),
      )
    })
  })
  afterEach(() => vi.unstubAllGlobals())

  it('pide la ruta del extracto y devuelve el CSV, no undefined', async () => {
    const blob = await fetchStatementExport('st-1')
    expect(new URL(pedido, 'https://console.invalid').pathname).toBe(
      '/v1/m/finops/statements/st-1/export',
    )
    expect(await blob.text()).toContain('a,b')
  })
})
