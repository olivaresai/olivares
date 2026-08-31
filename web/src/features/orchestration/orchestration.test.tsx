// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it, vi } from 'vitest'
import { renderIntel, screen, userEvent, within } from '@/test/intel'
import '@/features/_intel' // register the shared `intel` namespace for badges/notices
import {
  DecisionList,
  FlowsTable,
  SchedulesTable,
  edgeStyleForConfidence,
} from './components'
import {
  COVERAGE_CAVEAT,
  decisionsFixture,
  flowsFixture,
  graphFixture,
  schedulesFixture,
} from './fixtures'
import './i18n'

describe('edgeStyleForConfidence (honesty: never fake certainty)', () => {
  it('dashes ONLY an approximate edge, leaving attributed solid', () => {
    const approximate = edgeStyleForConfidence('approximate', '#000')
    const attributed = edgeStyleForConfidence('attributed', '#000')
    expect(approximate.strokeDasharray).toBe('4 3')
    expect(attributed.strokeDasharray).toBeUndefined()
  })

  it('matches the fixture: the inferred delegation edge is dashed', () => {
    const inferred = graphFixture.edges.find(
      (e) => e.confidence === 'approximate',
    )
    expect(inferred).toBeDefined()
    // The fixture's log-heuristic edge must never render as a solid (certain) line.
    expect(
      edgeStyleForConfidence(inferred!.confidence, '#000').strokeDasharray,
    ).toBe('4 3')
  })
})

describe('graph coverage', () => {
  it('carries the honest "absence is not zero" caveat verbatim', () => {
    // The container renders coverage.caveats prominently; assert the contract text is
    // present so an honest absence never reads as "no communication".
    expect(graphFixture.coverage.caveats).toContain(COVERAGE_CAVEAT)
    expect(COVERAGE_CAVEAT).toMatch(/ABSENT, not zero/)
  })
})

describe('FlowsTable', () => {
  it('renders the supervisor clusters with derived state badges', () => {
    renderIntel(<FlowsTable flows={flowsFixture} />)
    const table = screen.getByRole('grid')
    expect(within(table).getByText('sess-orchestrator')).toBeInTheDocument()
    expect(within(table).getByText(/Active/i)).toBeInTheDocument()
    expect(within(table).getByText(/Stalled/i)).toBeInTheDocument()
  })
})

describe('SchedulesTable', () => {
  it('renders cadences with their health and never hides a stalled miss', () => {
    renderIntel(<SchedulesTable schedules={schedulesFixture} />)
    const table = screen.getByRole('grid')
    expect(within(table).getByText('Nightly batch')).toBeInTheDocument()
    expect(within(table).getByText('0 0 * * *')).toBeInTheDocument()
    // The missed nightly batch must surface as stalled, not green.
    expect(within(table).getByText(/Stalled/i)).toBeInTheDocument()
  })

  it('keeps History available with schedule read permission alone', async () => {
    const user = userEvent.setup()
    const onHistory = vi.fn()
    renderIntel(
      <SchedulesTable
        schedules={[schedulesFixture[0]]}
        canRead
        onHistory={onHistory}
      />,
    )

    await user.click(
      screen.getByRole('button', {
        name: /actions for nightly batch/i,
      }),
    )
    await user.click(screen.getByRole('menuitem', { name: /history/i }))
    expect(onHistory).toHaveBeenCalledWith(schedulesFixture[0])
  })
})

describe('DecisionList — declared_not_fired honesty', () => {
  it('labels declared_not_fired as "approved, not actuated", NOT as success', () => {
    renderIntel(<DecisionList decisions={decisionsFixture} />)
    const declared = decisionsFixture.find(
      (d) => d.op_status === 'declared_not_fired',
    )
    expect(declared).toBeDefined()
    // The honest label is present, exactly once (one declared_not_fired decision)…
    const honest = screen.getAllByText(
      /Approved, not actuated \(no dispatcher\)/i,
    )
    expect(honest).toHaveLength(1)
    // …and the badge for that decision does NOT read "Dispatched"/"success".
    expect(honest[0].textContent ?? '').not.toMatch(/dispatched|success/i)
  })

  /**
   * ⛔ EL VEREDICTO DE LA PUERTA ERA TEXTO GRIS CRUDO en esta línea, y esa es la línea que existe
   * para auditar quién aprobó qué. Al lado de un `DecisionOpStatusBadge` que SÍ va coloreado, un
   * `rejected` en gris del mismo peso que un `approved` es **lo más silencioso de la fila**: el
   * ojo va al color y se salta el gris.
   *
   * EL MUTANTE: volver al `<span>` gris con el valor interpolado. La casilla lo mata porque
   * afirma la ETIQUETA localizada («Rejected»), que el texto crudo no producía — pintaba el
   * literal `rejected` precedido de «Gate: ».
   *
   * ⚠ Y las fixtures decían `'denied'` y `'n/a'`, que **el motor no emite**
   * (`modules/orchestration/ports.go`): las casillas ejercitaban estados imposibles y ninguna
   * tocaba los reales. Corregidas a `rejected` y `no_gate`.
   */
  it('pinta el veredicto de la puerta como tal, no como texto gris', () => {
    renderIntel(<DecisionList decisions={decisionsFixture} />)
    expect(screen.getByText(/^Rejected$/i)).toBeInTheDocument()
    expect(screen.queryByText(/Gate: rejected/i)).toBeNull()
  })

  it('shows the genuine dispatched decision as dispatched (the only success)', () => {
    renderIntel(<DecisionList decisions={decisionsFixture} />)
    // Exactly one dispatched in the fixture; declared_not_fired is NOT counted here.
    expect(screen.getAllByText(/^Dispatched$/i)).toHaveLength(1)
  })
})
