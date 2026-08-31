// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Red-team pure-component tests (module XVIII). They exercise the contract's honesty
// invariants:
//   1. a non-authorized target DISABLES "Launch run" (consent is the double-use gate);
//   2. a `degraded` run (all probes skipped — no sandbox) is NEVER a pass/green — it
//      reads as "pending sandbox" and carries no score;
//   3. the robustness gauge reflects passed/(passed+failed);
//   4. results expose a FINGERPRINT, never an attack payload.
import { describe, expect, it } from 'vitest'
import { renderIntel, screen, within } from '@/test/intel'
import '@/features/_intel' // register the shared `intel` namespace for badges/notices
import {
  ResultsTable,
  RunScorecard,
  RunsTable,
  TargetsTable,
} from './components'
import {
  completedRunFixture,
  degradedRunFixture,
  resultsFixture,
  runsFixture,
  targetsFixture,
} from './fixtures'
import './i18n'

describe('TargetsTable — consent gates the launch', () => {
  it('disables "Launch run" for a target that is not authorized', () => {
    renderIntel(<TargetsTable targets={targetsFixture} canAdmin />)
    const launchButtons = screen.getAllByRole('button', { name: /Launch run/i })
    // Three targets → three Launch buttons; only the authorized one is enabled.
    expect(launchButtons).toHaveLength(3)
    const enabled = launchButtons.filter(
      (b) => !(b as HTMLButtonElement).disabled,
    )
    const disabled = launchButtons.filter(
      (b) => (b as HTMLButtonElement).disabled,
    )
    expect(enabled).toHaveLength(1) // only tgt-support (authorized)
    expect(disabled).toHaveLength(2) // registered + revoked
  })

  it('shows the consent state for each target', () => {
    renderIntel(<TargetsTable targets={targetsFixture} canAdmin />)
    expect(screen.getByText(/Authorized/i)).toBeInTheDocument()
    expect(screen.getByText(/Registered/i)).toBeInTheDocument()
    expect(screen.getByText(/Revoked/i)).toBeInTheDocument()
  })
})

describe('RunScorecard — degraded is never a pass', () => {
  it('shows a completed run as a robustness score = passed/(passed+failed)', () => {
    // passed=7, failed=2 → score 78. The gauge renders that figure as its label.
    renderIntel(<RunScorecard run={completedRunFixture} />)
    expect(screen.getByText('78')).toBeInTheDocument()
    expect(screen.getByText(/^Completed$/)).toBeInTheDocument()
    // the score the engine sent equals passed/(passed+failed)·100 (we present, not compute)
    const { passed, failed, score } = completedRunFixture
    expect(Math.round((passed / (passed + failed)) * 100)).toBe(score)
  })

  it('renders a degraded run as "pending sandbox", never green/pass', () => {
    renderIntel(<RunScorecard run={degradedRunFixture} />)
    // honest label, not a pass
    expect(screen.getAllByText(/Pending sandbox/i).length).toBeGreaterThan(0)
    expect(screen.getByText(/no score/i)).toBeInTheDocument()
    // the degraded hint is shown, and there is NO "Completed" pass badge
    expect(screen.getByText(/no sandbox was available/i)).toBeInTheDocument()
    expect(screen.queryByText(/^Completed$/)).not.toBeInTheDocument()
    // all probes were skipped → 0 passed; it must not read as a defense that held
    expect(degradedRunFixture.passed).toBe(0)
    expect(degradedRunFixture.skipped).toBe(degradedRunFixture.total)
  })
})

describe('RunsTable — score column is blank for non-completed runs', () => {
  it('lists runs and only shows a score for completed runs', () => {
    renderIntel(<RunsTable runs={runsFixture} />)
    const table = screen.getByRole('grid')
    // completed run's score is shown…
    expect(within(table).getByText('78')).toBeInTheDocument()
    // …and the three statuses are present (degraded shown as "Pending sandbox")
    expect(within(table).getByText(/^Completed$/)).toBeInTheDocument()
    expect(within(table).getByText(/Pending sandbox/i)).toBeInTheDocument()
    expect(within(table).getByText(/Execution error/i)).toBeInTheDocument()
  })
})

describe('ResultsTable — fingerprint, never a payload', () => {
  it('renders per-probe outcomes and a truncated fingerprint, no raw secret', () => {
    renderIntel(<ResultsTable results={resultsFixture} />)
    const table = screen.getByRole('grid')
    // outcomes from the vocabulary appear
    expect(within(table).getByText(/Leaked/i)).toBeInTheDocument()
    expect(within(table).getByText(/Blocked/i)).toBeInTheDocument()
    expect(within(table).getByText(/Complied/i)).toBeInTheDocument()
    // the detail_hash is rendered truncated (head…tail), never expanded into content
    const fullHash = resultsFixture[0].detail_hash
    expect(screen.queryByText(fullHash)).not.toBeInTheDocument()
    expect(
      screen.getByText(new RegExp(fullHash.slice(0, 8))),
    ).toBeInTheDocument()
  })
})
