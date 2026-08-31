// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// C07-04 — LA MITAD DE PANTALLA del panel de coste por resultado.
//
// La mitad de cable está en `value-contract.test.ts`. Ésta existe porque el cliente sin pantalla
// pasa todas sus pruebas de contrato y no hace la operación posible — lo medí el mismo día:
// 711 métodos de cliente, 145 sin ningún llamante, y 97 de ellos añadidos por mí esa jornada.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen, userEvent } from '@/test/intel'
import '@/features/_intel'
import './i18n'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))

const valueSummaryMock = vi.fn()
// ⛔ LAS FIXTURES DEL REPO, NO UNA FORMA INVENTADA. Mi primera versión devolvía
// `{ total_micro_usd: 0, by_provider: [] }` para `summary` y **`<SpendBreakdown>` reventaba**:
// la vista entera se caía antes de poder pulsar la pestaña. Un doble que no puede lo que
// producción sí no mide nada — sólo que aquí falló ruidosamente en vez de pasar en verde.
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  const {
    summaryFixture,
    forecastFixture,
    spendFixture,
    recommendationsFixture,
  } = await import('./fixtures')
  const lista = () => Promise.resolve({ items: [], has_more: false })
  return {
    ...actual,
    finopsApi: {
      ...actual.finopsApi,
      valueSummary: (...a: unknown[]) => valueSummaryMock(...a),
      summary: () => Promise.resolve(summaryFixture),
      forecast: () => Promise.resolve(forecastFixture),
      spend: () => Promise.resolve(spendFixture),
      recommendations: () => Promise.resolve({ items: recommendationsFixture }),
      budgets: lista,
      statements: lista,
      costCenters: lista,
      modelRates: lista,
    },
  }
})

const { FinOpsView } = await import('./finops-view')

const RESUMEN = {
  dimension: 'agent',
  total_cost_micro_usd: 1_000_000,
  unattributed_cost_micro_usd: 400_000,
  creditable_micro_usd: 0,
  total_value_micro_usd: 0,
  net_value_micro_usd: -1_000_000,
  total_outcomes: 4,
  satisfied: 1,
  unsatisfied: 3,
  satisfied_rate_pct: 25,
  cost_per_outcome_micro_usd: 250_000,
  cancellation_risk: [
    {
      dimension: 'agent',
      key: 'agent-sin-resultados',
      cost_micro_usd: 600_000,
      outcomes: 0,
      satisfied: 0,
      reason: 'spend with no graded outcomes',
    },
  ],
}

async function abrirValor(user: ReturnType<typeof userEvent.setup>) {
  renderIntel(<FinOpsView />)
  await user.click(
    await screen.findByRole('tab', { name: /Cost per outcome/i }),
  )
}

beforeEach(() => {
  valueSummaryMock.mockReset().mockResolvedValue(RESUMEN)
})

describe('el panel de coste por resultado', () => {
  /**
   * EL CONTROL: la pestaña llama al panel del CFO. Antes de esto,
   * `GET /v1/m/finops/value/summary` tenía cliente y prueba de contrato, y ninguna pantalla.
   */
  it('pide el resumen al abrirse', async () => {
    const user = userEvent.setup()
    await abrirValor(user)
    expect(valueSummaryMock).toHaveBeenCalled()
  })

  /**
   * ⛔ EL CONTROL QUE MÁS IMPORTA: el gasto NO ATRIBUIDO se enseña con su propia cifra, no
   * fundido en el total. El motor promete `sum(buckets) + unattributed == total`
   * (`modules/finops/dto.go:236-238`), así que una pantalla que sólo pinte el total esconde
   * 400.000 de gasto sin dueño, y una que sólo sume los cubos infra-declara la factura.
   *
   * EL MUTANTE: quitar la tarjeta de «no atribuido». El total sigue ahí y la pantalla parece
   * correcta — que es precisamente por qué esta afirmación tiene que existir.
   */
  it('enseña lo no atribuido aparte del total', async () => {
    const user = userEvent.setup()
    await abrirValor(user)
    // Exacto, no substring: «Unattributed» aparece también en la descripción del panel, y una
    // aserción laxa la encontraría ahí — pasando aunque la TARJETA no existiera.
    expect(await screen.findByText('Total spend')).toBeInTheDocument()
    expect(await screen.findByText('Unattributed')).toBeInTheDocument()
  })

  /**
   * EL CONTROL: la lista de riesgo de cancelación nombra al sujeto y su motivo. Es la única
   * parte de este panel que señala a alguien concreto, y sin el motivo sería una acusación sin
   * fundamento visible.
   */
  it('nombra a cada sujeto en riesgo con su motivo', async () => {
    const user = userEvent.setup()
    await abrirValor(user)
    expect(await screen.findByText('agent-sin-resultados')).toBeInTheDocument()
    expect(
      await screen.findByText(/spend with no graded outcomes/i),
    ).toBeInTheDocument()
  })

  /**
   * LA DIRECCIÓN QUE NO DEBE DISPARAR: sin nadie en riesgo, la pantalla lo DICE en vez de
   * dejar un hueco. Un panel vacío se lee como un panel roto.
   */
  it('dice que no hay riesgo, en vez de dejar un hueco', async () => {
    valueSummaryMock.mockResolvedValue({ ...RESUMEN, cancellation_risk: [] })
    const user = userEvent.setup()
    await abrirValor(user)
    expect(
      await screen.findByText(/No subject is burning spend/i),
    ).toBeInTheDocument()
  })

  /**
   * EL CONTROL: sin coste por resultado calculable, se pinta «—», no un cero. El motor omite
   * el campo cuando no hay resultados (`cost_per_outcome_micro_usd,omitempty`), y un cero ahí
   * diría que cada resultado sale gratis.
   */
  it('dice que no lo sabe, en vez de pintar un cero', async () => {
    valueSummaryMock.mockResolvedValue({
      ...RESUMEN,
      total_outcomes: 0,
      satisfied: 0,
      unsatisfied: 0,
      cost_per_outcome_micro_usd: undefined,
    })
    const user = userEvent.setup()
    await abrirValor(user)

    // ⛔ SE CUENTAN LOS GUIONES, no se comprueba que exista uno. Mi primera versión afirmaba
    // `findAllByText('—').length > 0` y **el mutante SOBREVIVIÓ**: con el coste por resultado
    // pintado como cero, la tarjeta de resultados seguía enseñando el suyo y la aserción se
    // cumplía igual. Una afirmación que cualquier tarjeta puede satisfacer no fija ninguna.
    //
    // Con esta fixture SON DOS los que no se saben —los resultados y el coste por resultado—
    // y el mutante deja uno. La cuenta los distingue; la existencia, no.
    const guiones = await screen.findAllByText('—')
    expect(guiones).toHaveLength(2)
  })
})
