// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Extractos de reparto — el flujo que el motor sirve desde y la consola no exponía.
//
// ⛔ LO QUE ESTAS CELDAS FIJAN NO ES QUE «SE PINTE UNA LISTA», sino las tres cosas que una pantalla
//    ingenua callaría y que costarían una llamada de soporte cada una: que un extracto es un
//    SNAPSHOT y regenerar no recalcula, que el motor sólo acepta `monthly`/`weekly` con un
//    `period_start` RFC3339, y que exportar pide el CSV DEL EXTRACTO ABIERTO y no de otro.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen, userEvent, waitFor } from '@/test/intel'
import '@/features/_intel'
import './i18n'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))

const listaMock = vi.fn()
const detalleMock = vi.fn()
const generarMock = vi.fn()
const exportarMock = vi.fn()
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  const {
    summaryFixture,
    forecastFixture,
    spendFixture,
    recommendationsFixture,
  } = await import('./fixtures')
  const vacio = () => Promise.resolve({ items: [], has_more: false })
  return {
    ...actual,
    fetchStatementExport: (...a: unknown[]) => exportarMock(...a),
    finopsApi: {
      ...actual.finopsApi,
      statements: (...a: unknown[]) => listaMock(...a),
      statement: (...a: unknown[]) => detalleMock(...a),
      generateStatements: (...a: unknown[]) => generarMock(...a),
      costCenters: vacio,
      costCenterMappings: vacio,
      modelRates: vacio,
      budgets: vacio,
      seatUtilization: () =>
        Promise.resolve({ provider: 'anthropic', days: [] }),
      valueSummary: () => Promise.resolve({ cancellation_risk: [] }),
      summary: () => Promise.resolve(summaryFixture),
      forecast: () => Promise.resolve(forecastFixture),
      spend: () => Promise.resolve(spendFixture),
      recommendations: () => Promise.resolve({ items: recommendationsFixture }),
    },
  }
})

const { FinOpsView } = await import('./finops-view')

const EXTRACTO = {
  id: 'st-1',
  cost_center_id: 'cc-1',
  cost_center_code: 'ENG-01',
  cost_center_name: 'Ingeniería',
  period: 'monthly',
  period_start: '2026-08-01T00:00:00Z',
  period_end: '2026-08-31T23:59:59Z',
  total_micro_usd: 1_250_000,
  line_count: 2,
  prior_period_total_micro_usd: 0,
  delta_pct: 0,
  status: 'draft',
  generated_at: '2026-08-17T00:00:00Z',
}

beforeEach(() => {
  listaMock.mockReset().mockResolvedValue({ items: [EXTRACTO] })
  detalleMock.mockReset().mockResolvedValue({
    ...EXTRACTO,
    lines: [
      {
        id: 'l1',
        statement_id: 'st-1',
        model_ref: 'claude-opus-4-8',
        provider_ref: 'anthropic',
        agent_ref: 'agent-7',
        input_tokens: 1200,
        output_tokens: 800,
        cost_micro_usd: 42_000,
        sample_count: 3,
      },
    ],
  })
  generarMock.mockReset().mockResolvedValue({ items: [EXTRACTO] })
  exportarMock
    .mockReset()
    .mockResolvedValue(new Blob(['a,b\n1,2'], { type: 'text/csv' }))
})

async function abrir(user: ReturnType<typeof userEvent.setup>) {
  renderIntel(<FinOpsView />)
  await user.click(await screen.findByRole('tab', { name: /chargeback/i }))
}

describe('los extractos de reparto', () => {
  it('lista los extractos del motor en la pestaña de reparto', async () => {
    const user = userEvent.setup()
    await abrir(user)
    expect(await screen.findByText(/ENG-01/)).toBeInTheDocument()
  })

  /**
   * ⛔ EL AVISO QUE MÁS IMPORTA, y no es de formato: un extracto se calcula AL GENERARLO y
   * denormaliza el nombre y el código del centro. El motor es idempotente y se salta el conflicto,
   * así que **regenerar el mismo periodo NO lo recalcula**: el gasto ingerido después no aparece.
   *
   * EL MUTANTE: quitar el aviso. La pantalla queda correcta y silenciosa, y quien ingiera gasto y
   * regenere vuelve a ver el mismo total y concluye que el producto no suma.
   */
  it('avisa de que un extracto es un snapshot y regenerar no recalcula', async () => {
    const user = userEvent.setup()
    await abrir(user)
    expect(
      await screen.findByText(/frozen snapshot|does NOT recalculate/i),
    ).toBeInTheDocument()
  })

  /**
   * ⛔ EL MOTOR RECHAZA cualquier periodo que no sea `monthly` o `weekly`
   * (`modules/finops/statements.go:283`) y exige `period_start` en RFC3339 (`:287`). Se afirma sobre
   * el CUERPO de la petición, no sobre «se llamó»: mandar un periodo libre o una fecha con otro
   * formato es un 400 que el usuario vería como «no funciona».
   */
  it('al generar, manda un periodo que el motor acepta y una fecha RFC3339', async () => {
    const user = userEvent.setup()
    await abrir(user)
    await user.click(await screen.findByRole('button', { name: /generate/i }))
    await waitFor(() => expect(generarMock).toHaveBeenCalled())
    const cuerpo = generarMock.mock.calls[0][0] as {
      period: string
      period_start: string
    }
    expect(['monthly', 'weekly']).toContain(cuerpo.period)
    expect(cuerpo.period_start).toMatch(
      /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z$/,
    )
    expect(new Date(cuerpo.period_start).toString()).not.toBe('Invalid Date')
  })

  /**
   * ⛔ EXPORTAR PIDE EL CSV DEL EXTRACTO ABIERTO. Se afirma el ID: un export que ignorase la
   * selección descargaría el extracto equivocado y nadie lo notaría, porque el fichero se descarga
   * y no se muestra.
   */
  it('exporta el CSV del extracto abierto, con su id', async () => {
    const user = userEvent.setup()
    await abrir(user)
    await user.click(await screen.findByText(/ENG-01/))
    await user.click(
      await screen.findByRole('button', { name: /export statement csv/i }),
    )
    await waitFor(() => expect(exportarMock).toHaveBeenCalled())
    expect(exportarMock.mock.calls[0][0]).toBe('st-1')
  })

  /**
   * El detalle trae `sample_count` por línea, que es un DENOMINADOR: un coste sobre una muestra y
   * sobre mil no son la misma afirmación. Sin él, la tabla presenta un número sin su base.
   */
  it('el detalle enseña el número de muestras de cada línea', async () => {
    const user = userEvent.setup()
    await abrir(user)
    await user.click(await screen.findByText(/ENG-01/))
    expect(await screen.findByText(/samples/i)).toBeInTheDocument()
  })
})
