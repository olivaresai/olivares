// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The console twin of the engine's regression interval (modules/evals/
// regression_interval_test.go): the same 10-point pass-rate drop is noise at n=10
// and a regression at n=100, because only the 95% Newcombe interval of the
// difference tells a real drop from sampling noise at that n. The engine settles
// the verdict in the comparison receipt it attaches to EVERY run read — the list
// included (runs.go handleListRuns) — so the console must read it instead of
// flattening every `regressed: false` into "no regression".
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen, userEvent, within } from '@/test/intel'
import '@/features/_intel' // register the shared `intel` namespace for badges/notices
import { RunsTable, veredictoRegresion } from './components'
import {
  caseResultsFixture,
  scorecardsFixture,
  suitesFixture,
} from './fixtures'
import type { EvalRun } from './types'
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
  api.runResults.mockResolvedValue({
    items: caseResultsFixture,
    has_more: false,
  })
})

// The acceptance pair. Counts and intervals are the engine's own measured values
// (modules/evals/stats_test.go TestNewcombeDifference): 9/10 against a 10/10
// baseline drops 10 points with a CI of [-0.1894, 0.4042] — includes zero, so
// reason inconclusive_noise and regressed stays false; 90/100 against 100/100
// keeps the same drop with a CI of [0.0419, 0.1744] — excludes zero, so
// regressed. Both runs drift 0.1 over a 0.05 regression threshold: the n is the
// only difference between them.
const noiseRun: EvalRun = {
  id: 'run-2044',
  suite_ref: 'suite-triage-v3',
  suite_version: 3,
  subject_kind: 'agent',
  subject_ref: 'refund-router',
  model_ref: 'claude-opus-4-8',
  prompt_variant: 'v1',
  scorer: 'json_equal',
  status: 'completed',
  total: 10,
  passed: 9,
  failed: 1,
  errors: 0,
  skipped: 0,
  score: 0.9,
  pass_rate: 0.9,
  n_scored: 10,
  baseline_ref: 'run-2039',
  regressed: false,
  drift: 0.1,
  comparison: {
    reason: 'inconclusive_noise',
    pass_rate_diff: {
      baseline_passed: 10,
      baseline_n: 10,
      passed: 9,
      n: 10,
      drop: 0.1,
      drop_ci: { lo: -0.1894, hi: 0.4042 },
    },
  },
  started_at: '2026-06-03T11:00:00Z',
  finished_at: '2026-06-03T11:00:12Z',
  launched_by: 'ci-bot',
}

const regressedLargeNRun: EvalRun = {
  ...noiseRun,
  id: 'run-2045',
  total: 100,
  passed: 90,
  failed: 10,
  n_scored: 100,
  finished_at: '2026-06-03T11:05:00Z',
  regressed: true,
  comparison: {
    reason: 'comparison_valid',
    pass_rate_diff: {
      baseline_passed: 100,
      baseline_n: 100,
      passed: 90,
      n: 100,
      drop: 0.1,
      drop_ci: { lo: 0.0419, hi: 0.1744 },
    },
  },
}

describe('veredictoRegresion — the receipt decides, not the flag alone', () => {
  it('reads inconclusive_noise off the comparison receipt', () => {
    // regressed:false, scored, baseline present — the exact shape that used to
    // collapse into noRegression next to a drift over the threshold.
    expect(veredictoRegresion(noiseRun)).toBe('withinNoise')
  })

  it('keeps the flag-only verdicts when no receipt rides the run', () => {
    expect(
      veredictoRegresion({ ...regressedLargeNRun, comparison: undefined }),
    ).toBe('regressed')
    // An engine that never sends a receipt (or a run read before it existed)
    // must keep the old verdict, not guess: absent and null alike.
    expect(veredictoRegresion({ ...noiseRun, comparison: undefined })).toBe(
      'noRegression',
    )
    expect(veredictoRegresion({ ...noiseRun, comparison: null })).toBe(
      'noRegression',
    )
  })

  it('only the noise reason is a non-pass: other receipts on a scored, baselined run stay "no regression"', () => {
    // `withinNoise` must key on the reason, not on "a receipt is present": the
    // comparison_valid receipt of a clean run and the no_baseline stub both ride
    // regressed:false runs that really are fine / not compared.
    expect(
      veredictoRegresion({
        ...noiseRun,
        comparison: { reason: 'comparison_valid' },
      }),
    ).toBe('noRegression')
    expect(
      veredictoRegresion({
        ...noiseRun,
        comparison: { reason: 'no_baseline' },
      }),
    ).toBe('noRegression')
  })

  it('a regression is never softened: regressed outranks a noise receipt', () => {
    // The engine cannot emit this pair; the ordering is the documented contract.
    expect(veredictoRegresion({ ...noiseRun, regressed: true })).toBe(
      'regressed',
    )
  })

  it('a run that scored nothing is "not scored" even with a noise receipt', () => {
    expect(veredictoRegresion({ ...noiseRun, n_scored: 0 })).toBe('notScored')
  })

  it('a run with no baseline ref is "no baseline" even with a noise receipt', () => {
    expect(veredictoRegresion({ ...noiseRun, baseline_ref: undefined })).toBe(
      'noBaseline',
    )
  })
})

describe('RunsTable — the noise verdict', () => {
  it('shows the drop-within-noise verdict, never "None", for the 10-case run', () => {
    renderIntel(<RunsTable runs={[noiseRun]} />)
    const table = screen.getByRole('grid')
    expect(
      within(table).getByText('Drop within noise · drift 10%'),
    ).toBeInTheDocument()
    const row = within(table).getByText('refund-router').closest('tr')!
    expect(within(row).queryByText('None')).not.toBeInTheDocument()
  })

  it('tells the pair apart: the same drop regresses at n=100 and is noise at n=10', () => {
    renderIntel(<RunsTable runs={[regressedLargeNRun, noiseRun]} />)
    const table = screen.getByRole('grid')
    expect(within(table).getByText('Regressed · drift 10%')).toBeInTheDocument()
    expect(
      within(table).getByText('Drop within noise · drift 10%'),
    ).toBeInTheDocument()
  })
})

describe('run detail — the interval with both denominators', () => {
  // Absence is "no comparison was made", never a zero drop: a receipt without an
  // interval (no_baseline, an unusable baseline) renders no line at all.
  it('renders no interval line when the receipt carries no pass_rate_diff', async () => {
    api.runs.mockResolvedValue({
      items: [{ ...noiseRun, comparison: { reason: 'no_baseline' } }],
      has_more: false,
    })
    const user = userEvent.setup()
    renderIntel(<EvalsView />)
    await user.click(screen.getByRole('tab', { name: 'Runs' }))
    const row = (await screen.findByText('refund-router')).closest('tr')!
    await user.click(within(row).getByText('90%'))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).queryByText(/Pass-rate drop/)).not.toBeInTheDocument()
    // the dialog itself still carries the run's case mix
    expect(within(dialog).getByText('Passed')).toBeInTheDocument()
  })

  it('shows the 95% interval and both denominators for the noise run', async () => {
    api.runs.mockResolvedValue({ items: [noiseRun], has_more: false })
    const user = userEvent.setup()
    renderIntel(<EvalsView />)
    await user.click(screen.getByRole('tab', { name: 'Runs' }))
    const row = (await screen.findByText('refund-router')).closest('tr')!
    await user.click(within(row).getByText('90%'))
    // One decimal like the engine's gate line: 0.1 → "10.0%", -0.1894 →
    // "-18.9%", 0.4042 → "40.4%" — the lower bound's sign never rounds away.
    const dialog = await screen.findByRole('dialog')
    expect(
      within(dialog).getByText(
        'Pass-rate drop 10.0% (95% CI -18.9%–40.4%) — 9/10 scored vs 10/10 baseline',
      ),
    ).toBeInTheDocument()
  })

  it('shows the same line for a regressed run — the interval is evidence, not only the excuse', async () => {
    api.runs.mockResolvedValue({
      items: [regressedLargeNRun],
      has_more: false,
    })
    const user = userEvent.setup()
    renderIntel(<EvalsView />)
    await user.click(screen.getByRole('tab', { name: 'Runs' }))
    const row = (await screen.findByText('refund-router')).closest('tr')!
    await user.click(within(row).getByText('90%'))
    const dialog = await screen.findByRole('dialog')
    expect(
      within(dialog).getByText(
        'Pass-rate drop 10.0% (95% CI 4.2%–17.4%) — 90/100 scored vs 100/100 baseline',
      ),
    ).toBeInTheDocument()
  })

  it('keeps a lower bound under half a point visible next to a Regressed badge', async () => {
    // Synthetic bound (the engine's measured pair has no bound under 0.5 pt): the
    // default whole-percent rounding would print this regressed run's lower bound
    // as "0%", reading like an interval that includes zero.
    api.runs.mockResolvedValue({
      items: [
        {
          ...regressedLargeNRun,
          comparison: {
            reason: 'comparison_valid',
            pass_rate_diff: {
              baseline_passed: 100,
              baseline_n: 100,
              passed: 90,
              n: 100,
              drop: 0.1,
              drop_ci: { lo: 0.003, hi: 0.2 },
            },
          },
        },
      ],
      has_more: false,
    })
    const user = userEvent.setup()
    renderIntel(<EvalsView />)
    await user.click(screen.getByRole('tab', { name: 'Runs' }))
    const row = (await screen.findByText('refund-router')).closest('tr')!
    await user.click(within(row).getByText('90%'))
    const dialog = await screen.findByRole('dialog')
    expect(
      within(dialog).getByText(
        'Pass-rate drop 10.0% (95% CI 0.3%–20.0%) — 90/100 scored vs 100/100 baseline',
      ),
    ).toBeInTheDocument()
  })
})
