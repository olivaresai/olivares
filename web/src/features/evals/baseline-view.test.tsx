// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// C07-04 — fijar la línea base: «la superficie de decisión» del motor, y las tres cosas que la
// palabra «fijar» esconde.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen, userEvent } from '@/test/intel'
import '@/features/_intel'
import './i18n'

let permisos: (p: string) => boolean = () => true
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: (p: string) => permisos(p) }),
}))

const runsMock = vi.fn()
const pinMock = vi.fn()
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  const lista = () => Promise.resolve({ items: [], has_more: false })
  return {
    ...actual,
    evalsApi: {
      ...actual.evalsApi,
      runs: (...a: unknown[]) => runsMock(...a),
      pinBaseline: (...a: unknown[]) => pinMock(...a),
      runResults: lista,
      scorecards: lista,
      suites: lista,
      gates: lista,
      calibrationReports: lista,
      calibrationItems: lista,
    },
  }
})

const { EvalsView } = await import('./evals-view')

const RUN = {
  id: 'run-9',
  suite_ref: 'suite-tono',
  subject_ref: 'agent-1',
  subject_kind: 'agent',
  status: 'completed',
  total: 40,
  passed: 30,
  failed: 10,
  errors: 0,
  skipped: 0,
  score: 0.75,
  pass_rate: 0.75,
  n_scored: 40,
  regressed: false,
  drift: 0,
  baseline_ref: 'run-0',
  started_at: '2026-08-17T00:00:00Z',
}

async function abrirDetalle(user: ReturnType<typeof userEvent.setup>) {
  renderIntel(<EvalsView />)
  // La vista abre en «scorecards»; la tabla de ejecuciones vive en su propia pestaña.
  await user.click(await screen.findByRole('tab', { name: /^Runs$/i }))
  await user.click(await screen.findByText('agent-1'))
}

beforeEach(() => {
  permisos = () => true
  runsMock.mockReset().mockResolvedValue({ items: [RUN], has_more: false })
  pinMock.mockReset().mockResolvedValue({})
})

describe('fijar la línea base', () => {
  /**
   * ⛔ EL CONTROL QUE MÁS IMPORTA: fijar **NO AÑADE, SUSTITUYE**. Hay una sola línea base por
   * (suite, sujeto) y es mutable (`modules/evals/schema.go:107,297`), y toda regresión futura se
   * mide contra ella.
   *
   * EL MUTANTE: un diálogo que diga «fijar» y ya. Fijar una ejecución PEOR baja el listón, y ésa
   * es exactamente la forma de que una puerta de calidad deje de disparar **sin que nadie la
   * desactive**. El aviso va antes, no después.
   */
  it('avisa de que SUSTITUYE la línea base vigente', async () => {
    const user = userEvent.setup()
    await abrirDetalle(user)
    await user.click(
      await screen.findByRole('button', { name: /Pin as baseline/i }),
    )
    expect(
      await screen.findByText(
        /it REPLACES whatever is currently the baseline/i,
      ),
    ).toBeInTheDocument()
  })

  /**
   * ⛔ EL SEGUNDO, y es una limitación que se DICE en vez de taparse: `POST /baselines` es la
   * única ruta que el motor expone — no hay GET—, así que la pantalla **no puede** enseñar qué va
   * a reemplazar. Fingir que lo sabe sería peor que admitirlo.
   */
  it('admite que no puede enseñar la línea base actual', async () => {
    const user = userEvent.setup()
    await abrirDetalle(user)
    await user.click(
      await screen.findByRole('button', { name: /Pin as baseline/i }),
    )
    expect(
      await screen.findByText(/cannot show what this replaces/i),
    ).toBeInTheDocument()
  })

  /** Y la cifra de ESTA ejecución va delante, para que la decisión se tome con ella a la vista. */
  it('enseña la puntuación de esta ejecución antes de decidir', async () => {
    const user = userEvent.setup()
    await abrirDetalle(user)
    await user.click(
      await screen.findByRole('button', { name: /Pin as baseline/i }),
    )
    expect(await screen.findByText(/over n=40/i)).toBeInTheDocument()
  })

  /**
   * ⛔ Y EL PERMISO ES EL DE LA DECISIÓN: `evals:run:admin` (`evals.go:201-202`), no el de lanzar
   * ejecuciones. Un editor puede lanzar una ejecución y NO puede mover el listón contra el que se
   * mide todo lo demás.
   */
  it('sin evals:run:admin no aparece la acción', async () => {
    permisos = (p: string) => p !== 'evals:run:admin'
    const user = userEvent.setup()
    await abrirDetalle(user)
    expect(
      screen.queryByRole('button', { name: /Pin as baseline/i }),
    ).toBeNull()
  })
})
