// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// C07-04 — la utilización de asientos, y el día sin snapshot que NO es un día sin uso.
//
// `modules/finops/seats.go:137-140` lo dice: `has_seats` «distingue "no se publicó snapshot de
// asientos para este día" de un día real con cero asignados», y `utilization_pct` vale 0 cuando
// el denominador no existe — «no fabricated percentage».
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen, userEvent } from '@/test/intel'
import '@/features/_intel'
import './i18n'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))

const seatsMock = vi.fn()
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
      seatUtilization: (...a: unknown[]) => seatsMock(...a),
      valueSummary: () => Promise.resolve({ cancellation_risk: [] }),
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

async function abrirAsientos(user: ReturnType<typeof userEvent.setup>) {
  renderIntel(<FinOpsView />)
  await user.click(
    await screen.findByRole('tab', { name: /Seat utilisation/i }),
  )
}

beforeEach(() => {
  seatsMock.mockReset().mockResolvedValue({
    provider: 'anthropic',
    days: [
      {
        day: '2026-08-16',
        assigned_seats: 20,
        active_actors: 5,
        utilization_pct: 25,
        has_seats: true,
      },
    ],
  })
})

describe('la utilización de asientos', () => {
  /**
   * EL CONTROL: se pide SIEMPRE con proveedor. El motor contesta 400 sin él
   * (`modules/finops/seats.go:166-168`), así que una pantalla que lo omitiera fallaría en cada
   * carga sin decir por qué.
   */
  it('pide la utilización con un proveedor', async () => {
    const user = userEvent.setup()
    await abrirAsientos(user)
    expect(seatsMock).toHaveBeenCalledWith(
      expect.objectContaining({ provider: expect.any(String) }),
      { tenant: 't1' },
    )
  })

  it('enseña el porcentaje cuando hay snapshot', async () => {
    const user = userEvent.setup()
    await abrirAsientos(user)
    expect(await screen.findByText('25%')).toBeInTheDocument()
  })

  /**
   * ⛔ EL CONTROL QUE MÁS IMPORTA: un día SIN snapshot no es un día con uso cero.
   *
   * EL MUTANTE: pintar `utilization_pct` sin mirar `has_seats`. La fila diría **0 %**, que se lee
   * como «nadie usó sus asientos» — y esa lectura lleva a CANCELAR licencias. La verdad es
   * «nadie publicó cuántos asientos había», que lleva a arreglar la ingesta. **Acciones opuestas
   * a partir del mismo píxel.**
   */
  it('un día sin snapshot no se pinta como 0 %', async () => {
    seatsMock.mockResolvedValue({
      provider: 'anthropic',
      days: [
        {
          day: '2026-08-17',
          assigned_seats: 0,
          active_actors: 0,
          utilization_pct: 0,
          has_seats: false,
        },
      ],
    })
    const user = userEvent.setup()
    await abrirAsientos(user)
    expect(
      await screen.findByText(/no seat snapshot posted/i),
    ).toBeInTheDocument()
    expect(screen.queryByText('0%')).toBeNull()
  })

  /**
   * LA DIRECCIÓN QUE NO DEBE DISPARAR: un día CON snapshot y cero actores activos SÍ es un 0 %
   * legítimo, y se pinta. Sin esta casilla, una pantalla que escondiera todos los ceros pasaría
   * la de arriba y ocultaría justo el dato que hace falta para cancelar asientos de verdad.
   */
  it('un cero MEDIDO sí se pinta como 0 %', async () => {
    seatsMock.mockResolvedValue({
      provider: 'anthropic',
      days: [
        {
          day: '2026-08-17',
          assigned_seats: 20,
          active_actors: 0,
          utilization_pct: 0,
          has_seats: true,
        },
      ],
    })
    const user = userEvent.setup()
    await abrirAsientos(user)
    expect(await screen.findByText('0%')).toBeInTheDocument()
    expect(screen.queryByText(/no seat snapshot posted/i)).toBeNull()
  })
})
