// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// El registro de decisiones de TODO el estate. Lo que vigilan estos casos no es que la
// lista pinte filas, sino las tres afirmaciones que sólo este modo hace:
//
//   1. trae filas que NINGÚN schedule puede devolver (las de ejecución de workflow), y las
//      identifica — sin sujeto, la lista son veredictos sobre algo que no se nombra;
//   2. declara el recorte, porque «no hay más» es la afirmación más cara de un ledger;
//   3. no cambia el modo por schedule, que sigue sin repetir el sujeto en cada fila.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import { renderIntel, userEvent } from '@/test/intel'
import '@/features/_intel'

// El `can` es mutable a propósito: un caso comprueba qué pasa cuando el principal NO
// tiene el permiso que la RUTA exige, que no es el mismo con el que la vista se registra.
const authz = vi.hoisted(() => ({
  can: (_p: string): boolean => true,
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 'demo', can: authz.can }),
}))

const api = vi.hoisted(() => ({
  graph: vi.fn(),
  flows: vi.fn(),
  schedules: vi.fn(),
  scheduleDecisions: vi.fn(),
  decisions: vi.fn(),
  timeline: vi.fn(),
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return {
    ...actual,
    orchestrationApi: { ...actual.orchestrationApi, ...api },
  }
})

import { DecisionList } from './components'
import {
  decisionsFixture,
  estateDecisionsFixture,
  flowsFixture,
  graphFixture,
} from './fixtures'
import { orchestrationKeys } from './api'
import { OrchestrationView } from './orchestration-view'
import './i18n'

const VACIO = { items: [], has_more: false }
const SCHEDULES = {
  items: [
    {
      id: 'sched-0007',
      name: 'nightly cleanup',
      subject_kind: 'agent' as const,
      subject_ref: 'cleanup-bot',
      trigger_kind: 'cron',
      trigger_expr: '0 0 * * *',
      status: 'active',
    },
  ],
  has_more: false,
}

beforeEach(() => {
  vi.clearAllMocks()
  authz.can = () => true
  // El contenedor lee `graph.coverage.caveats`: un grafo sin `coverage` revienta la
  // vista entera y el caso mediría un árbol vacío.
  api.graph.mockResolvedValue(graphFixture)
  api.flows.mockResolvedValue({ items: flowsFixture, has_more: false })
  api.timeline.mockResolvedValue(VACIO)
  api.schedules.mockResolvedValue(SCHEDULES)
  api.scheduleDecisions.mockResolvedValue({
    items: decisionsFixture,
    has_more: false,
  })
  api.decisions.mockResolvedValue({
    items: estateDecisionsFixture,
    has_more: false,
  })
})

// La tarjeta de decisiones vive dentro de la pestaña «Schedules», que NO es la de por
// defecto: `TabsContent` no monta lo que no está activo. Sin este paso los asertos de
// abajo medirían «no aparece» por una razón que no es la suya — me pasó al escribirlos:
// cinco casos rojos por `Unable to find role="combobox"` con la vista pintando bien.
async function irASchedules() {
  const user = userEvent.setup()
  await user.click(await screen.findByRole('tab', { name: 'Schedules' }))
  return user
}

async function abrirEstate() {
  const user = await irASchedules()
  await user.click(await screen.findByRole('combobox'))
  await user.click(
    await screen.findByRole('option', { name: /All decisions/i }),
  )
}

describe('DecisionList — el sujeto sólo en el modo estate', () => {
  it('NO repite el sujeto en el modo por schedule (la superficie que ya existía)', () => {
    renderIntel(<DecisionList decisions={estateDecisionsFixture} />)
    expect(screen.queryByText('wf-nightly-report')).not.toBeInTheDocument()
    expect(screen.queryByText(/no schedule reference/i)).not.toBeInTheDocument()
  })

  it('con showSubject nombra el sujeto y DICE que la fila no cuelga de un schedule', () => {
    renderIntel(<DecisionList decisions={estateDecisionsFixture} showSubject />)
    expect(screen.getAllByText('wf-nightly-report').length).toBeGreaterThan(0)
    // FIRES IF: alguien deja el hueco vacío — un hueco se lee como dato que falta, no
    // como «esta clase de decisión no tiene referencia de schedule».
    const sinRef = screen.getAllByText(/no schedule reference/i)
    // Control NEGATIVO por CONTEO: son exactamente las cinco filas de workflow del
    // fixture, no todas — la fila con schedule NO lleva ese texto.
    expect(sinRef).toHaveLength(
      estateDecisionsFixture.filter((d) => !d.schedule_ref).length,
    )
    expect(sinRef.length).toBeLessThan(estateDecisionsFixture.length)
    expect(screen.getByText('cleanup-bot')).toBeInTheDocument()
  })
})

describe('DecisionOpStatusBadge — el color no puede desmentir al texto', () => {
  it('una DENEGACIÓN va en peligro y el ÉXITO en verde, y no al revés', () => {
    renderIntel(<DecisionList decisions={estateDecisionsFixture} showSubject />)
    // FIRES IF: `blocked: 'danger'` pasa a `'success'` — el texto seguiría diciendo
    // «Blocked» y el color diría lo contrario, que es lo que el ojo lee primero.
    const bloqueada = screen.getByText('Blocked')
    expect(bloqueada.className).toMatch(/bg-danger-soft/)
    expect(bloqueada.className).not.toMatch(/bg-success-soft/)
    // Control NEGATIVO: el único éxito de la tanda sí va en verde.
    expect(screen.getByText('Completed').className).toMatch(/bg-success-soft/)
  })

  it('los cuatro estados de workflow tienen peso propio, no el neutro del fallback', () => {
    renderIntel(<DecisionList decisions={estateDecisionsFixture} showSubject />)
    // FIRES IF: alguien los quita del mapa — caerían a `neutral` y saldrían en inglés
    // crudo, que es exactamente lo que pasaba antes de tiparlos.
    expect(screen.getByText(/Skipped/).className).toMatch(/bg-warning-soft/)
    expect(screen.getByText('Gate approved').className).toMatch(/bg-info-soft/)
    expect(screen.getByText(/Reconciled/).className).toMatch(/bg-info-soft/)
    // Y ninguno se queda en el literal del motor.
    expect(screen.queryByText('gate_passed')).not.toBeInTheDocument()
    expect(screen.queryByText('reconciled')).not.toBeInTheDocument()
  })
})

describe('OrchestrationView — el registro del estate', () => {
  it('pide el ledger del tenant CON límite y pinta la decisión que ningún schedule tiene', async () => {
    renderIntel(<OrchestrationView />)
    await abrirEstate()
    await waitFor(() => expect(api.decisions).toHaveBeenCalled())
    // FIRES IF: alguien quita el `limit` — el motor pagina a 100 en silencio.
    expect(api.decisions).toHaveBeenCalledWith({ limit: 1000 })
    expect(
      (await screen.findAllByText('wf-nightly-report')).length,
    ).toBeGreaterThan(0)
    // Y NO pide la ruta por schedule: son consultas distintas, no la misma sin filtro.
    expect(api.scheduleDecisions).not.toHaveBeenCalled()
  })

  it('lleva la advertencia que acota la afirmación, no sólo la lista', async () => {
    renderIntel(<OrchestrationView />)
    await abrirEstate()
    // ⛔ SE BUSCA UNA FRASE QUE SÓLO ESTÁ EN EL AVISO. La versión anterior de este caso
    //    buscaba «belong to no schedule», que también aparece en la DESCRIPCIÓN de la
    //    tarjeta: el mutante que borraba el aviso entero ESCAPABA porque la descripción
    //    seguía casando. Un aserto que casa con su propia nota al pie no mide nada.
    expect(
      await screen.findByText(/audit, findings and the run detail/i),
    ).toBeInTheDocument()
  })

  it('DECLARA el recorte cuando el motor dice que hay más, con la cifra', async () => {
    api.decisions.mockResolvedValue({
      items: estateDecisionsFixture,
      has_more: true,
    })
    renderIntel(<OrchestrationView />)
    await abrirEstate()
    const aviso = await screen.findByText(/TRUNCATED/i)
    expect(aviso.textContent).toMatch(/1000/)
    expect(aviso.textContent).not.toMatch(/\b100\b/)
  })

  it('y NO lo declara cuando no lo hay — el contrafactual en la otra dirección', async () => {
    renderIntel(<OrchestrationView />)
    await abrirEstate()
    expect(
      (await screen.findAllByText('wf-nightly-report')).length,
    ).toBeGreaterThan(0)
    expect(screen.queryByText(/TRUNCATED/i)).not.toBeInTheDocument()
  })

  it('elegir un schedule concreto sigue usando SU ruta, no el ledger', async () => {
    renderIntel(<OrchestrationView />)
    const user = await irASchedules()
    await user.click(await screen.findByRole('combobox'))
    await user.click(
      await screen.findByRole('option', { name: 'nightly cleanup' }),
    )
    await waitFor(() =>
      expect(api.scheduleDecisions).toHaveBeenCalledWith('sched-0007'),
    )
    expect(api.decisions).not.toHaveBeenCalled()
  })
})

describe('OrchestrationView — el permiso de la ruta, que NO es el de la vista', () => {
  it('sin `orchestration:schedule:read` no pide el ledger y lo dice', async () => {
    authz.can = (p: string) => p !== 'orchestration:schedule:read'
    renderIntel(<OrchestrationView />)
    await abrirEstate()
    expect(
      await screen.findByText(/cannot read schedule decisions/i),
    ).toBeInTheDocument()
    // FIRES IF: se quita `&& puedeLeerSchedules` del `enabled` — la pantalla pediría
    // una ruta que su principal no puede leer y enseñaría un error de red en su lugar.
    expect(api.decisions).not.toHaveBeenCalled()
  })
})

describe('orchestrationKeys — el ledger no comparte entrada de caché', () => {
  it('no coincide con la de un schedule ni con la de otro tenant', () => {
    expect(orchestrationKeys.ledger('t1')).not.toEqual(
      orchestrationKeys.scheduleDecisions('t1', 'ledger'),
    )
    expect(orchestrationKeys.ledger('t1')).not.toEqual(
      orchestrationKeys.ledger('t2'),
    )
    expect(orchestrationKeys.ledger('t1', { limit: 10 })).not.toEqual(
      orchestrationKeys.ledger('t2', { limit: 10 }),
    )
    // Control POSITIVO de que la comparación discrimina.
    expect(orchestrationKeys.ledger('t1')).toEqual(
      orchestrationKeys.ledger('t1'),
    )
  })
})
