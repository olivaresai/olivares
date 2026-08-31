// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen, userEvent, waitFor, within } from '@/test/intel'
import '@/features/_intel' // register the shared `intel` namespace for badges/notices
import {
  AbComparison,
  CaseResultsTable,
  RunsTable,
  ScorecardCard,
} from './components'
import {
  abResultFixture,
  caseResultsFixture,
  runsFixture,
  scorecardsFixture,
  suitesFixture,
} from './fixtures'
import './i18n'

const api = vi.hoisted(() => ({
  scorecards: vi.fn(),
  suites: vi.fn(),
  runs: vi.fn(),
  run: vi.fn(),
  runResults: vi.fn(),
  ab: vi.fn(),
  launchRun: vi.fn(),
}))

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))
vi.mock('./api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('./api')>()),
  evalsApi: api,
}))

import { EvalsView } from './evals-view'

beforeEach(() => {
  vi.clearAllMocks()
  api.scorecards.mockResolvedValue({
    items: scorecardsFixture,
    has_more: false,
  })
  api.suites.mockResolvedValue({ items: suitesFixture, has_more: false })
  api.runs.mockResolvedValue({ items: runsFixture, has_more: false })
  api.runResults.mockResolvedValue({ items: [], has_more: false })
  // The fixture is test-only here: it is the mocked POST /ab response payload.
  api.ab.mockResolvedValue(abResultFixture)
})

describe('ScorecardCard', () => {
  it('shows the mean score, the pass-rate and a trend sparkline', () => {
    const sc = scorecardsFixture[0] // support-triage: healthy
    renderIntel(<ScorecardCard scorecard={sc} />)
    // mean_score 0.91 → "0.91". La cifra rotulada «Pass-rate» sale de
    // pooled_pass_rate (0.89 → "89%"), NO de pass_rate (0.94), que es la media
    // por corrida y llevaba ese rótulo prestado.
    expect(screen.getByText('0.91')).toBeInTheDocument()
    expect(screen.getByText('89%')).toBeInTheDocument()
    expect(screen.queryByText('94%')).not.toBeInTheDocument()
    expect(screen.getByText('support-triage')).toBeInTheDocument()
    // the trend renders via a themed Sparkline (Recharts renders no geometry under
    // jsdom — the visual e2e covers pixels); the healthy card carries no regression
    expect(screen.queryByText(/Regressed/i)).not.toBeInTheDocument()
  })

  // El rótulo «Pass-rate» debe cubrir #aprobados/#total, no la media de las tasas
  // por corrida: una corrida de 1 caso pesaba lo mismo que una de 200. El motor
  // manda las dos cifras desde (scorecards.go:43 y :52) y la consola sólo
  // declaraba la primera, así que la tarjeta enseñaba la media bajo el nombre de
  // la tasa. La fixture ahora lleva las dos y DIFIEREN, que es lo que hace que
  // este test pueda distinguirlas.
  it('shows the case-weighted pass-rate with its denominator, not the per-run mean', () => {
    const sc = scorecardsFixture[0]
    renderIntel(<ScorecardCard scorecard={sc} />)
    expect(screen.getByText('89%')).toBeInTheDocument()
    expect(screen.getByText(/over 213/)).toBeInTheDocument()
    expect(screen.queryByText('94%')).not.toBeInTheDocument()
  })

  // Ausencia es "no se puntuó nada", nunca "0 %": el motor omite el campo cuando
  // pooledN es 0 (scorecards.go:157), y un 0 % inventado acusaría al sujeto de
  // fallarlo todo cuando no se midió nada.
  it('renders a dash, never 0%, when nothing was scored', () => {
    const sc = { ...scorecardsFixture[0], pooled_pass_rate: undefined }
    renderIntel(<ScorecardCard scorecard={sc} />)
    expect(screen.queryByText('0%')).not.toBeInTheDocument()
    expect(screen.queryByText(/over /)).not.toBeInTheDocument()
  })

  it('flags a regressed subject with a danger badge', () => {
    const sc = scorecardsFixture[1] // code-reviewer: regressed
    renderIntel(<ScorecardCard scorecard={sc} />)
    expect(screen.getByText(/Regressed/i)).toBeInTheDocument()
  })
})

describe('RunsTable — regression flow', () => {
  it('marks the regressed run with the regressed badge and a drift', () => {
    renderIntel(<RunsTable runs={runsFixture} />)
    const table = screen.getByRole('grid')
    // run-2042 regressed with drift 0.27 → "Regressed · drift 27%"
    expect(within(table).getByText(/Regressed/i)).toBeInTheDocument()
    expect(within(table).getByText(/27%/)).toBeInTheDocument()
  })

  it('does not mark the healthy completed run as regressed', () => {
    const healthy = runsFixture.filter((r) => !r.regressed)
    renderIntel(<RunsTable runs={healthy} />)
    expect(screen.queryByText(/Regressed/i)).not.toBeInTheDocument()
  })
})

describe('CaseResultsTable — honesty: skipped is never a pass', () => {
  it('renders a skipped case as a neutral Skipped, never styled as Pass', () => {
    renderIntel(<CaseResultsTable results={caseResultsFixture} />)
    const skippedBadge = screen.getByText('Skipped')
    expect(skippedBadge).toBeInTheDocument()
    // the skipped outcome carries the `outline` variant, NOT the success variant a
    // pass uses — assert it does not wear the success surface class.
    const badgeEl = skippedBadge.closest('span') ?? skippedBadge
    expect(badgeEl.className).not.toMatch(/bg-success-soft/)
    // a Pass exists in the same table and DOES read as success — proving the contrast
    const passBadge = screen.getByText('Pass')
    const passEl = passBadge.closest('span') ?? passBadge
    expect(passEl.className).toMatch(/success/)
  })

  it('shows a hash fingerprint per case and never a raw candidate output', () => {
    renderIntel(<CaseResultsTable results={caseResultsFixture} />)
    // the detail_hash is shown truncated (head…tail), proving it is a fingerprint
    expect(screen.getAllByText(/a91f3c7e…/).length).toBeGreaterThan(0)
    // the skipped case is not scored — its score cell is an em-dash, not "0.00"
    const skippedRow = screen.getByText('judges-tone').closest('tr')!
    expect(within(skippedRow).queryByText('0.00')).not.toBeInTheDocument()
    expect(within(skippedRow).getByText('Skipped')).toBeInTheDocument()
  })
})

describe('A/B comparison flow', () => {
  // This test used to drive two RUN pickers and assert `api.ab` was called with
  // two RunInputs and `outputs: {}`. It was green because `vi.mock('./api')`
  // accepts anything — the real engine answered 400 "invalid JSON body" for that
  // body, every time (modules/evals/ab_console_contract_test.go). The shape is now
  // pinned against the engine's own fixture in ab-contract.test.ts; what this
  // test owns is the VIEW: what the operator must supply before the write is
  // offered at all.
  async function openAbTab() {
    const user = userEvent.setup()
    renderIntel(<EvalsView />)
    await user.click(screen.getByRole('tab', { name: 'A/B' }))
    return user
  }

  it('starts honestly empty and renders only the POST /ab response', async () => {
    const user = await openAbTab()

    expect(
      await screen.findByText(
        'No A/B comparison has been run in this session.',
      ),
    ).toBeInTheDocument()
    expect(screen.queryByText('v6-concise')).not.toBeInTheDocument()
    expect(screen.queryByText('v5-verbose')).not.toBeInTheDocument()

    const runComparison = screen.getByRole('button', {
      name: 'Run comparison',
    })
    expect(runComparison).toBeDisabled()

    await user.click(screen.getByRole('combobox', { name: /Suite/ }))
    await user.click(screen.getByRole('option', { name: 'code-review-rubric' }))
    // A suite alone is not a comparison: the endpoint scores OUTPUT SETS.
    expect(runComparison).toBeDisabled()

    await user.type(
      screen.getByRole('textbox', { name: /Variant A outputs/ }),
      '{{"greeting": "hello"}',
    )
    expect(runComparison).toBeDisabled()
    await user.type(
      screen.getByRole('textbox', { name: /Variant B outputs/ }),
      '{{"greeting": "hi there"}',
    )
    expect(runComparison).toBeEnabled()

    await user.click(runComparison)

    await waitFor(() =>
      expect(api.ab).toHaveBeenCalledWith({
        suite_ref: 'suite-review-v2',
        a: { label: '', outputs: { greeting: 'hello' } },
        b: { label: '', outputs: { greeting: 'hi there' } },
      }),
    )

    expect(await screen.findByText('v5-verbose')).toBeInTheDocument()
    expect(screen.getByText(/Winner: v6-concise/)).toBeInTheDocument()
    expect(screen.getByText(/0\.09/)).toBeInTheDocument()
  })

  // The engine ACCEPTS an empty output set: it scores nothing, answers a 0-vs-0
  // tie and still persists two runs into the tenant's history. So the console
  // refuses it here, and says why rather than disabling the button in silence.
  it('refuses an empty output set instead of writing a fabricated tie', async () => {
    const user = await openAbTab()
    await user.click(await screen.findByRole('combobox', { name: /Suite/ }))
    await user.click(screen.getByRole('option', { name: 'code-review-rubric' }))
    await user.type(
      screen.getByRole('textbox', { name: /Variant A outputs/ }),
      '{{}',
    )
    await user.type(
      screen.getByRole('textbox', { name: /Variant B outputs/ }),
      '{{"greeting": "hi"}',
    )

    expect(
      await screen.findByText(/Provide at least one case output/),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: 'Run comparison' }),
    ).toBeDisabled()
    expect(api.ab).not.toHaveBeenCalled()
  })

  it('opts into the judged head-to-head only when asked, and never invents one', async () => {
    const user = await openAbTab()
    await user.click(await screen.findByRole('combobox', { name: /Suite/ }))
    await user.click(screen.getByRole('option', { name: 'code-review-rubric' }))
    await user.type(
      screen.getByRole('textbox', { name: /Variant A outputs/ }),
      '{{"greeting": "hello"}',
    )
    await user.type(
      screen.getByRole('textbox', { name: /Variant B outputs/ }),
      '{{"greeting": "hi there"}',
    )
    await user.click(screen.getByRole('checkbox', { name: /head-to-head/ }))
    await user.click(screen.getByRole('button', { name: 'Run comparison' }))

    await waitFor(() =>
      expect(api.ab).toHaveBeenCalledWith(
        expect.objectContaining({ pairwise: true }),
      ),
    )
  })
})

describe('AbComparison', () => {
  it('reports an explicit tie when there is no winner', () => {
    renderIntel(
      <AbComparison
        variants={[
          { label: 'a', score: 0.8, pass_rate: 0.8 },
          { label: 'b', score: 0.8, pass_rate: 0.8 },
        ]}
        winner=""
        delta={0}
      />,
    )
    expect(screen.getAllByText(/Tie/i).length).toBeGreaterThan(0)
  })
})
