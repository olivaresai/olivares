// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// C07-04 — la calibración del juez, y las tres veces que «no se puede medir» NO es «salió mal».
//
// Es la pantalla que dice cuánto se puede uno fiar del juez LLM, y su DTO
// (`modules/evals/calibration.go:224-249`) distingue con cuidado lo que no se ha podido medir.
// Fundirlo convierte una incertidumbre en un veredicto, en la pantalla donde eso cuesta más.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen, userEvent } from '@/test/intel'
import '@/features/_intel'
import { ApiError } from '@/lib/api/errors'
import './i18n'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))

const reportsMock = vi.fn()
const itemsMock = vi.fn()
const runMock = vi.fn()
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  const lista = () => Promise.resolve({ items: [], has_more: false })
  return {
    ...actual,
    evalsApi: {
      ...actual.evalsApi,
      calibrationReports: (...a: unknown[]) => reportsMock(...a),
      calibrationItems: (...a: unknown[]) => itemsMock(...a),
      runCalibration: (...a: unknown[]) => runMock(...a),
      scorecards: lista,
      runs: lista,
      suites: lista,
      gates: lista,
    },
  }
})

const { EvalsView } = await import('./evals-view')

const BASE = {
  id: 'c-1',
  set_name: 'set-tono',
  judge_model: 'claude-opus-5',
  status: 'completed',
  items_total: 40,
  items_scored: 40,
  items_error: 0,
  agreement: 0.93,
  agreement_ci: { lo: 0.81, hi: 0.98 },
  verbosity_corr: 0.12,
  verbosity_corr_defined: true,
  kappa: 0.71,
  kappa_defined: true,
  sensitivity: 0.9,
  sensitivity_n: 20,
  specificity: 0.88,
  specificity_n: 18,
  target: 0.9,
  kappa_floor: 0.6,
  meets_target: true,
}

async function abrirCalibracion(user: ReturnType<typeof userEvent.setup>) {
  renderIntel(<EvalsView />)
  await user.click(
    await screen.findByRole('tab', { name: /Judge calibration/i }),
  )
}

const CASO = { case_key: 'c1', human_passed: true, human_score: 0.9 }

beforeEach(() => {
  reportsMock.mockReset().mockResolvedValue({ items: [BASE] })
  itemsMock.mockReset().mockResolvedValue({
    items: [CASO, { case_key: 'c2', human_passed: false, human_score: 0.1 }],
  })
  runMock.mockReset().mockResolvedValue({})
})

describe('la calibración del juez', () => {
  it('lee los informes al abrir la pestaña', async () => {
    const user = userEvent.setup()
    await abrirCalibracion(user)
    expect(reportsMock).toHaveBeenCalled()
    expect(await screen.findByText('set-tono')).toBeInTheDocument()
  })

  /**
   * ⛔ EL CONTROL QUE MÁS IMPORTA: con `kappa_defined: false` el informe **no puede certificar**
   * nada (`calibration.go:34-36`: un conjunto cuyas etiquetas humanas son todas «pasa» no puede
   * medir acuerdo corregido por azar). Eso NO es un suspenso.
   *
   * EL MUTANTE: pintar `meets_target` sin mirar `kappa_defined`. Con el acuerdo alto saldría
   * VERDE — la pantalla certificaría al juez sobre una medición que no existe. Con el acuerdo
   * bajo saldría ROJO, culpando al juez de algo que no se midió. Las dos son falsas.
   */
  it('sin kappa definida dice que NO se puede certificar, ni aprueba ni suspende', async () => {
    reportsMock.mockResolvedValue({
      items: [{ ...BASE, kappa_defined: false, meets_target: true }],
    })
    const user = userEvent.setup()
    await abrirCalibracion(user)
    expect(
      await screen.findByText('cannot certify — kappa undefined'),
    ).toBeInTheDocument()
    expect(screen.queryByText('meets target')).toBeNull()
    expect(screen.queryByText('below target')).toBeNull()
  })

  /**
   * ⛔ EL SEGUNDO, y lo dice el motor con todas las letras: **`sensitivity_n: 0` significa que la
   * tasa está SIN MEDIR, no que sea cero**. Pintar `0%` ahí afirma que el juez no acierta ni un
   * solo caso bueno cuando nadie lo ha medido.
   *
   * EL MUTANTE: pintar `sensitivity` sin mirar su denominador.
   */
  it('una tasa sin denominador se dice «—», no «0%»', async () => {
    reportsMock.mockResolvedValue({
      items: [{ ...BASE, sensitivity: 0, sensitivity_n: 0 }],
    })
    const user = userEvent.setup()
    await abrirCalibracion(user)
    // El acuerdo sí se mide (93%), así que un «0%» en pantalla sólo puede venir de la tasa.
    expect(await screen.findByText('93%')).toBeInTheDocument()
    expect(screen.queryByText('0%')).toBeNull()
  })

  /**
   * LA DIRECCIÓN QUE NO DEBE DISPARAR: con todo medido, SÍ se emite el veredicto. Sin esta
   * casilla, una pantalla que dijera siempre «no se puede certificar» pasaría las dos de arriba
   * y no serviría para nada.
   */
  it('con todo medido, sí certifica', async () => {
    const user = userEvent.setup()
    await abrirCalibracion(user)
    expect(await screen.findByText('meets target')).toBeInTheDocument()
    // Exacto: la DESCRIPCIÓN del panel también dice «cannot certify anything», y un regex
    // laxo la encontraría ahí — pasando aunque el badge no existiera.
    expect(screen.queryByText('cannot certify — kappa undefined')).toBeNull()
  })

  /**
   * ⛔ EL TERCERO, y es el que no se ve venir: **`agreement_ci` LLEGA SIEMPRE**. Su tipo
   * (`runs.go:378-381`) es un struct plano — sin puntero, sin `omitempty` — así que un informe
   * sin nada puntuado trae `{lo: 0, hi: 0}`.
   *
   * EL MUTANTE: pintar el intervalo cuando existe el campo, en vez de cuando existe el
   * denominador. La pantalla escribiría **«0 %–0 %»**, que no se lee como «no se midió»: se lee
   * como **certeza perfecta de que el juez no acierta nunca**. Un campo que siempre está no
   * puede señalar su propia ausencia, y por eso la guarda es `items_scored`.
   */
  it('un informe sin puntuar no inventa un intervalo «0 %–0 %»', async () => {
    reportsMock.mockResolvedValue({
      items: [
        {
          ...BASE,
          items_scored: 0,
          agreement: 0,
          agreement_ci: { lo: 0, hi: 0 },
        },
      ],
    })
    const user = userEvent.setup()
    await abrirCalibracion(user)
    expect(await screen.findByText('set-tono')).toBeInTheDocument()
    expect(screen.queryByText(/0%–0%/)).toBeNull()
    expect(screen.queryByText(/over n=0/i)).toBeNull()
  })

  /**
   * LA DIRECCIÓN QUE NO DEBE DISPARAR: con casos puntuados el intervalo SÍ se pinta, y con su n.
   * Sin esta casilla, una pantalla que no enseñara nunca el intervalo pasaría la de arriba — y el
   * acuerdo volvería a ser una cifra sin método, que es el defecto que esto viene a cerrar.
   */
  it('con casos puntuados enseña el intervalo y su denominador', async () => {
    const user = userEvent.setup()
    await abrirCalibracion(user)
    expect(
      await screen.findByText('95% CI 81%–98% over n=40'),
    ).toBeInTheDocument()
  })

  /**
   * El CUARTO «no se puede medir» que el comentario de la vista enumeraba y la pantalla no
   * pintaba: un juez cuyas notas no varían no tiene correlación con la verbosidad que medir.
   *
   * EL MUTANTE: pintar `verbosity_corr` sin su `_defined`. Saldría **0,00**, que se lee como «no
   * hay sesgo de verbosidad» — la conclusión tranquilizadora, y falsa.
   */
  it('una correlación indefinida se dice «—», no «0,00»', async () => {
    reportsMock.mockResolvedValue({
      items: [{ ...BASE, verbosity_corr: 0, verbosity_corr_defined: false }],
    })
    const user = userEvent.setup()
    await abrirCalibracion(user)
    expect(await screen.findByText('Verbosity corr.')).toBeInTheDocument()
    expect(screen.queryByText('0.00')).toBeNull()
  })

  /**
   * Y `degraded` sin su cuenta de errores es una etiqueta sin tamaño: 1 error de 40 y 39 de 40
   * pintaban la MISMA insignia, y sólo una de las dos invalida el informe.
   */
  it('el estado degradado dice CUÁNTOS fallaron', async () => {
    reportsMock.mockResolvedValue({
      items: [
        { ...BASE, status: 'degraded', items_scored: 1, items_error: 39 },
      ],
    })
    const user = userEvent.setup()
    await abrirCalibracion(user)
    expect(await screen.findByText('degraded · 39 errored')).toBeInTheDocument()
  })
  /**
   * ⛔ ESTE PANEL EXISTE PARA EXPLICAR EL VEREDICTO DE ARRIBA. La pestaña ya decía «no se puede
   * certificar — kappa indefinida» y no ofrecía forma de ver POR QUÉ. La razón es esta
   * distribución: `calibration.go:34-36` — un conjunto cuyas etiquetas humanas son **todas
   * iguales** no puede medir acuerdo corregido por azar.
   *
   * EL MUTANTE: enseñar la lista sin la distribución. El veredicto queda como un callejón sin
   * salida; con ella es una instrucción concreta — etiquetar casos del otro signo.
   */
  it('una distribución degenerada explica por qué no se puede certificar', async () => {
    itemsMock.mockResolvedValue({
      items: [
        { case_key: 'c1', human_passed: true },
        { case_key: 'c2', human_passed: true },
      ],
    })
    const user = userEvent.setup()
    await abrirCalibracion(user)
    expect(
      await screen.findByText(/kappa CANNOT be computed/i),
    ).toBeInTheDocument()
  })

  /**
   * LA DIRECCIÓN QUE NO DEBE DISPARAR: con etiquetas de los dos signos NO se avisa. Un aviso
   * permanente convertiría en alarma el estado normal de un conjunto sano.
   */
  it('una distribución mixta no avisa', async () => {
    const user = userEvent.setup()
    await abrirCalibracion(user)
    expect(await screen.findByText(/1 labelled pass/i)).toBeInTheDocument()
    expect(screen.queryByText(/kappa CANNOT be computed/i)).toBeNull()
  })

  /**
   * ⛔ `human_score` es un PUNTERO en el motor (`*float64`, `omitempty`): ausente significa **sin
   * puntuar**, que no es un cero. Un 0,00 ahí afirma que un humano evaluó el caso y le dio la nota
   * mínima — una afirmación sobre el trabajo de una persona que nadie hizo.
   */
  it('un caso sin puntuar no se pinta como 0,00', async () => {
    itemsMock.mockResolvedValue({
      items: [{ case_key: 'c1', human_passed: true }],
    })
    const user = userEvent.setup()
    await abrirCalibracion(user)
    expect(await screen.findByText(/unscored/i)).toBeInTheDocument()
    expect(screen.queryByText('0.00')).toBeNull()
  })

  /**
   * ⛔ Y el 412: sin juez cableado no hay a quién medir. El motor lo razona — «an honest 412: a
   * calibration cannot be simulated». Dejarlo caer al manejador genérico lo convierte en avería.
   */
  it('un 412 se dice «no hay juez», no un error', async () => {
    runMock.mockRejectedValue(
      new ApiError(412, 'no_judge', 'Precondition Failed'),
    )
    const user = userEvent.setup()
    await abrirCalibracion(user)
    await user.click(
      await screen.findByRole('button', { name: /Run calibration/i }),
    )
    expect(await screen.findByText(/cannot be simulated/i)).toBeInTheDocument()
  })
})
