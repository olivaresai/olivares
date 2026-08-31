// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// C07-04 — resultados graduados: el numerador del coste por resultado, y la trampa de
// idempotencia que haría contarlo dos veces.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen, userEvent } from '@/test/intel'
import '@/features/_intel'
import './i18n'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))

const outcomesMock = vi.fn()
const ingestMock = vi.fn()
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
      outcomes: (...a: unknown[]) => outcomesMock(...a),
      ingestOutcome: (...a: unknown[]) => ingestMock(...a),
      costCenters: lista,
      costCenterMappings: lista,
      modelRates: lista,
      seatUtilization: () =>
        Promise.resolve({ provider: 'anthropic', days: [] }),
      valueSummary: () => Promise.resolve({ cancellation_risk: [] }),
      summary: () => Promise.resolve(summaryFixture),
      forecast: () => Promise.resolve(forecastFixture),
      spend: () => Promise.resolve(spendFixture),
      recommendations: () => Promise.resolve({ items: recommendationsFixture }),
      budgets: lista,
      statements: lista,
    },
  }
})

const { FinOpsView } = await import('./finops-view')

async function abrir(user: ReturnType<typeof userEvent.setup>) {
  renderIntel(<FinOpsView />)
  await user.click(await screen.findByRole('tab', { name: /Cost centres/i }))
}

beforeEach(() => {
  outcomesMock.mockReset().mockResolvedValue({ items: [] })
  ingestMock.mockReset().mockResolvedValue({ accepted: true })
})

describe('los resultados graduados', () => {
  /**
   * ⛔ LA CASILLA QUE JUSTIFICA EL DISEÑO DEL FORMULARIO, y viene de la validación del motor
   * (`modules/finops/value.go`, `outcomeIngestRequest.validate`):
   *
   *   «with no outcome_ref the dedup key falls back to the instant, so a server-minted clock
   *    would make a retried POST a NEW row (double-counting value)»
   *
   * ⇒ Reintentar tiene que enviar **el MISMO instante**. El instante se captura al ABRIR el
   * diálogo, no al enviar.
   *
   * EL MUTANTE: `occurred_at: new Date().toISOString()` en el momento del envío. Dos envíos del
   * mismo formulario crean DOS filas y el valor se cuenta dos veces — inflando el numerador del
   * coste por resultado, que es la cifra con la que se decide si el producto se queda.
   */
  it('un REINTENTO tras un fallo manda el MISMO instante', async () => {
    // El reintento de verdad: el primer envío FALLA y el diálogo sigue abierto. Cerrarlo y
    // reabrirlo sería otra graduación, no un reintento, y entonces un instante nuevo es correcto.
    ingestMock.mockRejectedValueOnce(new Error('network'))
    const user = userEvent.setup()
    await abrir(user)
    await user.click(
      await screen.findByRole('button', { name: /Grade an outcome/i }),
    )
    await user.type(await screen.findByLabelText(/Agent ref/i), 'agent-1')

    await user.click(await screen.findByRole('button', { name: /^Send$/i }))
    await user.click(await screen.findByRole('button', { name: /^Send$/i }))

    expect(ingestMock).toHaveBeenCalledTimes(2)
    const primero = ingestMock.mock.calls[0][0] as { occurred_at?: string }
    const segundo = ingestMock.mock.calls[1][0] as { occurred_at?: string }
    expect(primero.occurred_at).toBeTruthy()
    expect(segundo.occurred_at).toBe(primero.occurred_at)
  })

  /**
   * ⛔ Y LO QUE LA PANTALLA NO PUEDE AFIRMAR: la respuesta es `202 {"accepted": true}` y el propio
   * handler advierte que «dedup can make the write a no-op». Así que se dice **aceptado**, no
   * «registrado», y la lista se recarga: la lista es el registro, el 202 sólo dice que se recibió.
   */
  it('dice ACEPTADO, no registrado, porque el 202 no distingue', async () => {
    const user = userEvent.setup()
    await abrir(user)
    await user.click(
      await screen.findByRole('button', { name: /Grade an outcome/i }),
    )
    expect(
      await screen.findByText(/re-sent unchanged on a retry/i),
    ).toBeInTheDocument()
  })

  /**
   * Un resultado SIN valor no es un valor cero: el campo es opcional en el motor
   * (`value_micro_usd,omitempty`). Pintar 0 afirmaría que alguien valoró el resultado en nada.
   */
  it('un resultado sin valor no se pinta como cero', async () => {
    outcomesMock.mockResolvedValue({
      items: [
        {
          subject_kind: 'agent',
          subject_ref: 'agent-1',
          verdict: 'satisfied',
          occurred_at: '2026-08-17T00:00:00Z',
        },
      ],
    })
    const user = userEvent.setup()
    await abrir(user)
    expect(await screen.findByText(/no value recorded/i)).toBeInTheDocument()
  })
})
