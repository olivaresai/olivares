// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Deterministic evals fixtures shaped exactly like the contract — used by the
// component tests and the visual e2e route mocks. Scores are floats 0..1; trends are
// ascending by time. They encode the honesty invariants the tests assert: a regressed
// run carries `regressed:true` + a `drift`; a `skipped` case (llm_judge with no Judge
// wired) is NEVER a pass; `detail_hash` is a fingerprint, `label` is a short clamp —
// NEVER a real candidate output or secret.
import type { AbResult, CaseResult, EvalRun, Scorecard, Suite } from './types'

export const scorecardsFixture: Scorecard[] = [
  {
    key: 'support-triage',
    subject_kind: 'agent',
    // `pass_rate` es la MEDIA por corrida; `pooled_pass_rate` es la tasa real
    // (aprobados/puntuados sobre todas las corridas). El motor manda las dos y
    // DIFIEREN — la fixture no llevaba la segunda, así que ningún test de
    // consola podía notar que la tarjeta enseñaba la primera.
    pass_rate: 0.94,
    pooled_pass_rate: { rate: 0.89, n: 213, ci: { lo: 0.84, hi: 0.93 } },
    mean_score: 0.91,
    runs: 42,
    last_score: 0.93,
    trend: [
      { at: '2026-05-29T00:00:00Z', score: 0.9, pass_rate: 0.92 },
      { at: '2026-05-30T00:00:00Z', score: 0.91, pass_rate: 0.93 },
      { at: '2026-05-31T00:00:00Z', score: 0.92, pass_rate: 0.94 },
      { at: '2026-06-01T00:00:00Z', score: 0.9, pass_rate: 0.92 },
      { at: '2026-06-02T00:00:00Z', score: 0.92, pass_rate: 0.94 },
      { at: '2026-06-03T00:00:00Z', score: 0.93, pass_rate: 0.94 },
    ],
    regressed: false,
  },
  {
    // The degradation case the Drift tab and the regression badge surface.
    key: 'code-reviewer',
    subject_kind: 'agent',
    pass_rate: 0.71,
    pooled_pass_rate: { rate: 0.61, n: 148, ci: { lo: 0.53, hi: 0.69 } },
    mean_score: 0.68,
    runs: 31,
    last_score: 0.61,
    trend: [
      { at: '2026-05-29T00:00:00Z', score: 0.88, pass_rate: 0.9 },
      { at: '2026-05-30T00:00:00Z', score: 0.86, pass_rate: 0.88 },
      { at: '2026-05-31T00:00:00Z', score: 0.8, pass_rate: 0.83 },
      { at: '2026-06-01T00:00:00Z', score: 0.74, pass_rate: 0.78 },
      { at: '2026-06-02T00:00:00Z', score: 0.67, pass_rate: 0.72 },
      { at: '2026-06-03T00:00:00Z', score: 0.61, pass_rate: 0.66 },
    ],
    regressed: true,
  },
  {
    key: 'claude-opus-4-8',
    subject_kind: 'model',
    pass_rate: 0.97,
    pooled_pass_rate: { rate: 0.93, n: 296, ci: { lo: 0.9, hi: 0.95 } },
    mean_score: 0.95,
    runs: 58,
    last_score: 0.96,
    trend: [
      { at: '2026-05-30T00:00:00Z', score: 0.95, pass_rate: 0.97 },
      { at: '2026-05-31T00:00:00Z', score: 0.96, pass_rate: 0.97 },
      { at: '2026-06-01T00:00:00Z', score: 0.95, pass_rate: 0.96 },
      { at: '2026-06-02T00:00:00Z', score: 0.96, pass_rate: 0.97 },
      { at: '2026-06-03T00:00:00Z', score: 0.96, pass_rate: 0.98 },
    ],
    regressed: false,
  },
]

export const suitesFixture: Suite[] = [
  {
    id: 'suite-triage-v3',
    name: 'support-triage-golden',
    description: 'Routing + summary correctness on the support golden set.',
    subject_kind: 'agent',
    scorer: 'json_equal',
    criterion:
      'Route matches expected queue; summary contains the key entities.',
    pass_threshold: 0.8,
    regression_threshold: 0.05,
    judge_model: null,
    suite_version: 3,
    status: 'active',
  },
  {
    id: 'suite-review-v2',
    name: 'code-review-rubric',
    description: 'Rubric-scored review quality (llm_judge).',
    subject_kind: 'agent',
    scorer: 'llm_judge',
    criterion: 'Findings are correct, actionable and free of false positives.',
    pass_threshold: 0.75,
    regression_threshold: 0.05,
    // No judge model wired ⇒ llm_judge degrades to skipped, never a silent pass.
    judge_model: null,
    suite_version: 2,
    status: 'active',
  },
]

export const runsFixture: EvalRun[] = [
  {
    id: 'run-2041',
    suite_ref: 'suite-triage-v3',
    suite_version: 3,
    subject_kind: 'agent',
    subject_ref: 'support-triage',
    model_ref: 'claude-opus-4-8',
    prompt_variant: 'v3',
    scorer: 'json_equal',
    status: 'completed',
    total: 120,
    passed: 113,
    failed: 7,
    errors: 0,
    skipped: 0,
    score: 0.93,
    pass_rate: 0.94,
    regressed: false,
    drift: 0.01,
    started_at: '2026-06-03T08:10:00Z',
    finished_at: '2026-06-03T08:11:42Z',
    launched_by: 'ci-bot',
  },
  {
    // The regressed run the flow test asserts: regressed badge + drift.
    id: 'run-2042',
    suite_ref: 'suite-review-v2',
    suite_version: 2,
    subject_kind: 'agent',
    subject_ref: 'code-reviewer',
    model_ref: 'claude-haiku-4-5',
    prompt_variant: 'v5',
    scorer: 'json_equal',
    status: 'completed',
    total: 80,
    passed: 53,
    failed: 27,
    errors: 0,
    skipped: 0,
    score: 0.61,
    pass_rate: 0.66,
    regressed: true,
    drift: 0.27,
    started_at: '2026-06-03T09:30:00Z',
    finished_at: '2026-06-03T09:31:08Z',
    launched_by: 'ci-bot',
  },
  {
    // The degraded run: llm_judge with no Judge wired ⇒ every case skipped.
    id: 'run-2043',
    suite_ref: 'suite-review-v2',
    suite_version: 2,
    subject_kind: 'agent',
    subject_ref: 'code-reviewer',
    model_ref: 'claude-opus-4-8',
    prompt_variant: 'v6',
    scorer: 'llm_judge',
    status: 'degraded',
    total: 80,
    passed: 0,
    failed: 0,
    errors: 0,
    skipped: 80,
    score: 0,
    pass_rate: 0,
    regressed: false,
    drift: 0,
    started_at: '2026-06-03T10:05:00Z',
    finished_at: '2026-06-03T10:05:51Z',
    launched_by: 'fran',
  },
]

// Per-case results for run-2042 (the regressed run). Includes a pass, a fail, an
// error, AND a skipped — so the test can assert `skipped` is not styled as a pass.
export const caseResultsFixture: CaseResult[] = [
  {
    id: 'cr-1',
    run_ref: 'run-2042',
    case_key: 'detects-null-deref',
    scorer: 'json_equal',
    outcome: 'pass',
    score: 1,
    passed: true,
    detail_hash:
      'a91f3c7e2b4d6f80c1e5a9b3d7f02468ac13579bdf2468ace0123456789abcde',
    label: 'matched expected finding set',
    occurred_at: '2026-06-03T09:30:12Z',
  },
  {
    id: 'cr-2',
    run_ref: 'run-2042',
    case_key: 'flags-sql-injection',
    scorer: 'json_equal',
    outcome: 'fail',
    score: 0.2,
    passed: false,
    detail_hash:
      'bb27d419fe0a3c5e7901234567cdef89ab01234567def0123456789abcdef012',
    label: 'missed 2 of 3 expected findings',
    occurred_at: '2026-06-03T09:30:31Z',
  },
  {
    id: 'cr-3',
    run_ref: 'run-2042',
    case_key: 'rates-style-nits',
    scorer: 'numeric_range',
    outcome: 'error',
    score: 0,
    passed: false,
    detail_hash:
      'cc3308b5af129d4e6f7012890abcdef1234567890fedcba9876543210fedcba98',
    label: 'scorer raised: malformed numeric output',
    occurred_at: '2026-06-03T09:30:47Z',
  },
  {
    // Judge not wired ⇒ skipped. NEVER a pass. `passed:false`, neutral outcome.
    id: 'cr-4',
    run_ref: 'run-2042',
    case_key: 'judges-tone',
    scorer: 'llm_judge',
    outcome: 'skipped',
    score: 0,
    passed: false,
    detail_hash:
      'dd44190c6b237e5f8091abcdef234567890abcdef1234567890abcdef12345678',
    label: 'no judge wired — not scored',
    occurred_at: '2026-06-03T09:31:02Z',
  },
]

/** Test-only payload returned by mocked POST /ab routes. Never render directly. */
export const abResultFixture: AbResult = {
  variants: [
    {
      label: 'v6-concise',
      run_ref: '019f0000-0000-7000-8000-00000000ab01',
      score: 0.88,
      pass_rate: 0.9,
    },
    {
      label: 'v5-verbose',
      run_ref: '019f0000-0000-7000-8000-00000000ab02',
      score: 0.79,
      pass_rate: 0.82,
    },
  ],
  winner: 'v6-concise',
  delta: 0.09,
  tie: false,
}
