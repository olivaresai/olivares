// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// C07-04 — LA MITAD DE PANTALLA de la pestaña del gate de CI.
//
// La otra mitad —qué bytes salen al cable— está en `gate-contract.test.ts`, y van separadas a
// propósito: ese fichero corre el cliente REAL contra un `fetch` sustituido; éste mockea `./api`
// y afirma que **la pantalla llama a lo que dice llamar**. Un fichero solo no puede hacer las dos
// cosas, y quedarse con una deja el hueco que este repositorio ya pagó: doce funciones de cliente
// perfectas **sin ningún llamante**, con todas las celdas verdes.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen, userEvent, waitFor } from '@/test/intel'
import '@/features/_intel'
import './i18n'

let permisos = new Set<string>()
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    can: (p: string) => permisos.has(p),
  }),
}))

const gatesMock = vi.fn()
const overrideMock = vi.fn()
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return {
    ...actual,
    evalsApi: {
      ...actual.evalsApi,
      gates: (...a: unknown[]) => gatesMock(...a),
      overrideGate: (...a: unknown[]) => overrideMock(...a),
      scorecards: () => Promise.resolve({ items: [], has_more: false }),
      runs: () => Promise.resolve({ items: [], has_more: false }),
      suites: () => Promise.resolve({ items: [], has_more: false }),
    },
  }
})

const { EvalsView } = await import('./evals-view')

const GATE_FALLADO = {
  id: 'g-1',
  suite_ref: 'suite-tono',
  subject_ref: 'agent-a',
  verdict: 'fail' as const,
  effective_verdict: 'fail' as const,
  reasons: ['pass rate 0.71 below threshold 0.90'],
  sampled: 10,
  total_cases: 40,
  judge_model: 'claude-opus-5',
  overridden: false,
  occurred_at: '2026-08-17T10:00:00Z',
}

const GATE_ANULADO = {
  ...GATE_FALLADO,
  id: 'g-2',
  effective_verdict: 'pass' as const,
  overridden: true,
  override_by: 'ciso@example.com',
  override_reason: 'el fallo era del runner, no del modelo',
}

async function abrirGate(user: ReturnType<typeof userEvent.setup>) {
  renderIntel(<EvalsView />)
  await user.click(await screen.findByRole('tab', { name: /CI gate/i }))
}

beforeEach(() => {
  permisos = new Set(['evals:run:read'])
  gatesMock
    .mockReset()
    .mockResolvedValue({ items: [GATE_FALLADO], has_more: false })
  overrideMock.mockReset().mockResolvedValue(GATE_ANULADO)
})

describe('la pestaña del gate de CI', () => {
  /**
   * EL CONTROL: la pestaña lee el gate. Antes de esto, `GET /v1/m/evals/gate` no lo llamaba
   * nadie: las evaluaciones existían en el motor y no había forma de verlas.
   */
  it('lee las evaluaciones del gate al abrirse', async () => {
    const user = userEvent.setup()
    await abrirGate(user)
    await waitFor(() => expect(gatesMock).toHaveBeenCalled())
    expect(await screen.findByText('suite-tono')).toBeInTheDocument()
  })

  /**
   * EL CONTROL, y es la propiedad que esta pantalla existe para no romper: cuando el veredicto
   * MEDIDO y el EFECTIVO difieren, se ven LOS DOS, con quién anuló y por qué.
   *
   * EL MUTANTE: pintar sólo `effective_verdict`. La fila diría «pass» y una release desbloqueada
   * por una persona se leería como una que pasó sola — que es exactamente la afirmación que un
   * registro de evidencias existe para impedir.
   */
  it('cuando los dos veredictos difieren, enseña los dos y quién anuló', async () => {
    gatesMock.mockResolvedValue({ items: [GATE_ANULADO], has_more: false })
    const user = userEvent.setup()
    await abrirGate(user)

    expect(await screen.findByText(/measured fail/i)).toBeInTheDocument()
    expect(await screen.findByText(/CI acts on pass/i)).toBeInTheDocument()
    expect(await screen.findByText(/ciso@example\.com/)).toBeInTheDocument()
    expect(
      await screen.findByText(/el fallo era del runner/),
    ).toBeInTheDocument()
  })

  /**
   * LA DIRECCIÓN QUE NO DEBE DISPARAR: si los dos veredictos COINCIDEN, no se pinta la flecha ni
   * el segundo badge. Repetirlo sugeriría que hubo una decisión donde no la hubo.
   */
  it('no habla de veredicto efectivo cuando no hubo decisión', async () => {
    const user = userEvent.setup()
    await abrirGate(user)
    expect(await screen.findByText(/measured fail/i)).toBeInTheDocument()
    expect(screen.queryByText(/CI acts on/i)).toBeNull()
  })

  /**
   * EL CONTROL: anular es ADMIN (`modules/evals/evals.go:221`, permRunAdmin). Un lector no ve
   * el botón.
   *
   * EL MUTANTE: gatear con `evals:run:write`, o no gatear. Un escritor vería la puerta de
   * decisión y el 403 llegaría del motor DESPUÉS de haberla ofrecido.
   */
  it('un lector no ve el botón de anular', async () => {
    const user = userEvent.setup()
    await abrirGate(user)
    await screen.findByText('suite-tono')
    expect(screen.queryByRole('button', { name: /^Override$/i })).toBeNull()
  })

  it('un admin sí lo ve', async () => {
    permisos = new Set(['evals:run:read', 'evals:run:admin'])
    const user = userEvent.setup()
    await abrirGate(user)
    expect(
      await screen.findByRole('button', { name: /^Override$/i }),
    ).toBeInTheDocument()
  })

  /**
   * EL CONTROL: el motor contesta 409 a anular un gate que YA PASA — «nothing to override»
   * (`gate.go:612`). La consola no ofrece el botón ahí. No es adelantarse a la autoridad: es no
   * mandar una petición que ya se sabe rechazada.
   */
  it('no ofrece anular un gate que ya pasa', async () => {
    permisos = new Set(['evals:run:read', 'evals:run:admin'])
    gatesMock.mockResolvedValue({
      items: [{ ...GATE_FALLADO, verdict: 'pass', effective_verdict: 'pass' }],
      has_more: false,
    })
    const user = userEvent.setup()
    await abrirGate(user)
    await screen.findByText('suite-tono')
    expect(screen.queryByRole('button', { name: /^Override$/i })).toBeNull()
  })

  /**
   * EL CONTROL: la anulación manda el id y el MOTIVO ESCRITO. El motor rechaza con 400 un motivo
   * vacío (`gate.go:588`), así que el botón de confirmar está deshabilitado hasta que hay texto.
   *
   * EL MUTANTE: habilitar el confirmar con el campo vacío. La consola mandaría una petición que
   * el motor rechaza, y el operador vería un error genérico en vez de saber qué le falta.
   */
  it('anula con el motivo escrito, y no antes de tenerlo', async () => {
    permisos = new Set(['evals:run:read', 'evals:run:admin'])
    const user = userEvent.setup()
    await abrirGate(user)

    await user.click(await screen.findByRole('button', { name: /^Override$/i }))
    const confirmar = await screen.findByRole('button', {
      name: /Override gate/i,
    })
    expect(confirmar).toBeDisabled()

    await user.type(screen.getByLabelText(/Reason/i), 'el fallo era del runner')
    expect(confirmar).toBeEnabled()
    await user.click(confirmar)

    await waitFor(() =>
      expect(overrideMock).toHaveBeenCalledWith(
        'g-1',
        'el fallo era del runner',
      ),
    )
  })

  /**
   * ⛔ EL MOTOR EXIGE LAS DOS TASAS JUNTAS, con estas palabras (`modules/evals/gate.go:89-92`):
   * el estimador corregido de Rogan–Gladen se muestra **«alongside — never instead of — the raw
   * rate»**. Es la tasa que midió el juez, ajustada por la sensibilidad y especificidad REALES de
   * ese juez.
   *
   * EL MUTANTE: enseñar sólo la cruda. Con un juez cuya sensibilidad no es 1, la cifra en la que
   * CI se apoya para bloquear un merge está sesgada y nadie lo ve — y el motor calcula la
   * corrección precisamente para eso.
   */
  it('enseña la tasa corregida junto a la cruda', async () => {
    gatesMock.mockResolvedValue({
      has_more: false,
      items: [
        {
          ...GATE_FALLADO,
          corrected_pass_rate: {
            pass_rate: 0.62,
            sensitivity: 0.9,
            specificity: 0.85,
          },
        },
      ],
    })
    const user = userEvent.setup()
    await abrirGate(user)
    expect(
      await screen.findByText(/Bias-corrected pass rate 62%/i),
    ).toBeInTheDocument()
  })

  /**
   * ⛔ Y SU AUSENCIA NO ES «coincide con la cruda»: el campo es un puntero con `omitempty` y no
   * llega cuando no hay calibración de confianza con la que corregir. Callarlo deja la tasa cruda
   * pareciendo ajustada.
   */
  it('sin calibración de confianza lo dice, no lo calla', async () => {
    gatesMock.mockResolvedValue({ items: [GATE_FALLADO], has_more: false })
    const user = userEvent.setup()
    await abrirGate(user)
    expect(
      await screen.findByText(/no trusted calibration to correct with/i),
    ).toBeInTheDocument()
  })

  /** Y la muestra: el gate juzga un subconjunto sembrado, no todos los casos. */
  /**
   * Y la muestra: el gate juzga un subconjunto SEMBRADO, no todos los casos. «Pasó» significa
   * «pasó la muestra», y la fixture de este fichero es 10 de 40 — un cuarto.
   */
  it('dice que juzgó una MUESTRA, no todos los casos', async () => {
    const user = userEvent.setup()
    await abrirGate(user)
    expect(
      await screen.findByText(/10 of 40|10 \/ 40|10.*40/),
    ).toBeInTheDocument()
  })
})
