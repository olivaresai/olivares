// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Deterministic sandbox fixtures shaped EXACTLY like the contract — used by the
// component tests and the visual e2e route mocks. They cover the honesty cases the
// view must render truthfully:
//   - an isolated in-proc-mock run (isolated + destroyed) that WAS scored,
//   - a degraded run ("executed, not scored": status=degraded, score/passed null) —
//     NEVER shown as a pass,
//   - a container (OS-level) run, isolated, scored,
//   - per-step synthetic outputs INCLUDING a deterministic mock-miss marker,
//   - the four comparison verdicts (improved/regressed/unchanged/inconclusive)
//     with signed deltas.
// No output is a real resource or secret; every `output` is bounded synthetic text.
import type { Comparison, Output, Run, Scenario } from './types'

// Scenarios carry the steps + mocks the engine actually projects. They used
// to carry `steps_count` and `created_at`, which `toScenarioDTO` emits nowhere
// (modules/sandbox/scenarios.go:44-51) — so the fixtures agreed with the console's
// type while the console disagreed with the engine, and the table's step count was
// `undefined` for every real row. Step counts here are the LENGTHS below, not a claim.
export const scenariosFixture: Scenario[] = [
  {
    id: 'scn-checkout-flow',
    name: 'Checkout agent — happy path',
    description:
      'Drives the checkout agent through cart → quote → confirm against mocked payment and catalog MCPs.',
    subject_kind: 'agent',
    steps: [
      { key: 'step-1-add-to-cart', input: 'Add SKU SYNTH-1 and SKU SYNTH-2 to the cart.' },
      { key: 'step-2-quote', input: 'Ask for the order total.' },
      { key: 'step-3-confirm', input: 'Confirm the order with the synthetic card.' },
    ],
    mocks: [
      { resource: 'catalog.lookup', response: '{"sku":"SYNTH-1","price":"SYNTH"}' },
      { resource: 'payment.authorize', response: '{"status":"authorized"}' },
    ],
    spec_hash:
      'a91f4c0e7b2d3f5a8c10e2b7d4f6091c3e5a7b9d1f0c2e4a6b8d0f2c4e6a8b0d',
    status: 'active',
  },
  {
    id: 'scn-refund-policy',
    name: 'Refund policy — edge cases',
    description:
      'Exercises refund eligibility prompts with a mocked policy resource and a deliberately-absent ledger mock.',
    subject_kind: 'prompt',
    steps: [
      { key: 'step-1-classify', input: 'Classify this refund request.' },
      { key: 'step-2-lookup-order', input: 'Look up order #SYNTH-1042.' },
      { key: 'step-3-ledger-balance', input: 'Read the customer ledger balance.' },
      { key: 'step-4-draft-reply', input: 'Draft the refund reply.' },
    ],
    // The ledger mock is deliberately absent — step 3 produces a mock-miss marker.
    mocks: [
      { resource: 'policy.refunds', response: 'Refunds within 30 days are eligible.' },
    ],
    spec_hash:
      'd0c2e4a6b8d0f2c4e6a8b0d1f3a5c7e9b1d3f5a7c9e1b3d5f7a9c1e3b5d7f9a1',
    status: 'active',
  },
  {
    id: 'scn-legacy-router',
    name: 'Legacy router regression',
    description: 'Archived scenario kept for historical comparison only.',
    subject_kind: 'agent',
    steps: [{ key: 'step-1-route', input: 'Route a synthetic request through the legacy path.' }],
    mocks: [],
    spec_hash:
      'b5d7f9a1c3e5b7d9f1a3c5e7b9d1f3a5c7e9b1d3f5a7c9e1b3d5f7a9c1e3b5d7',
    status: 'archived',
  },
]

export const runsFixture: Run[] = [
  // Isolated in-proc-mock run, scored and passed, ephemeral state discarded.
  {
    id: 'run-001',
    scenario_ref: 'scn-checkout-flow',
    kind: 'scenario',
    subject_ref: 'agent:checkout',
    variant: 'candidate',
    runner: 'inproc-mock',
    isolated: true,
    status: 'completed',
    steps_total: 6,
    steps_ok: 6,
    steps_error: 0,
    outputs_hash:
      '7b2d3f5a8c10e2b7d4f6091c3e5a7b9d1f0c2e4a6b8d0f2c4e6a8b0da91f4c0e',
    score: 0.94,
    passed: true,
    suite_ref: 'suite:checkout-golden',
    destroyed: true,
    started_at: '2026-06-03T08:12:00Z',
    finished_at: '2026-06-03T08:12:04Z',
    launched_by: 'user:ops-eng',
  },
  // DEGRADED: executed but NOT scored (no Scorer wired). Must NOT read as a pass.
  {
    id: 'run-002',
    scenario_ref: 'scn-refund-policy',
    kind: 'scenario',
    subject_ref: 'prompt:refund-eligibility',
    variant: null,
    runner: 'inproc-mock',
    isolated: true,
    status: 'degraded',
    steps_total: 4,
    steps_ok: 3,
    steps_error: 0,
    outputs_hash:
      'c3e5a7b9d1f0c2e4a6b8d0f2c4e6a8b0da91f4c0e7b2d3f5a8c10e2b7d4f6091',
    score: null,
    passed: null,
    suite_ref: null,
    destroyed: true,
    started_at: '2026-06-03T09:40:00Z',
    finished_at: '2026-06-03T09:40:02Z',
    launched_by: 'user:qa',
  },
  // Deterministic replay of a historical session, scored.
  {
    id: 'run-003',
    scenario_ref: null,
    kind: 'replay',
    subject_ref: 'session:sess_4f2a',
    variant: null,
    runner: 'inproc-mock',
    isolated: true,
    status: 'completed',
    steps_total: 5,
    steps_ok: 5,
    steps_error: 0,
    outputs_hash:
      'f0c2e4a6b8d0f2c4e6a8b0da91f4c0e7b2d3f5a8c10e2b7d4f6091c3e5a7b9d1',
    score: 0.81,
    passed: true,
    suite_ref: 'suite:checkout-golden',
    destroyed: true,
    started_at: '2026-06-02T16:05:00Z',
    finished_at: '2026-06-02T16:05:03Z',
    launched_by: 'user:ops-eng',
  },
  // OS-level container backend (pluggable Runner), isolated, scored, one step errored.
  {
    id: 'run-004',
    scenario_ref: 'scn-checkout-flow',
    kind: 'scenario',
    subject_ref: 'agent:checkout',
    variant: 'baseline',
    runner: 'container',
    isolated: true,
    status: 'completed',
    steps_total: 6,
    steps_ok: 5,
    steps_error: 1,
    outputs_hash:
      'a6b8d0f2c4e6a8b0da91f4c0e7b2d3f5a8c10e2b7d4f6091c3e5a7b9d1f0c2e4',
    score: 0.88,
    passed: true,
    suite_ref: 'suite:checkout-golden',
    destroyed: true,
    started_at: '2026-06-03T08:10:00Z',
    finished_at: '2026-06-03T08:10:11Z',
    launched_by: 'user:ops-eng',
  },
]

/** Per-step synthetic outputs for run-002 — includes a deterministic mock-miss.
 *  Every row carries its `id`, which the engine always sends
 *  (modules/sandbox/runs.go:409) and which is the row's only unique identity: the
 *  stream dedupes replays on it, and step keys are not required to be unique. */
export const outputsFixture: Record<string, Output[]> = {
  'run-002': [
    {
      id: 'out-002-1',
      run_ref: 'run-002',
      step_key: 'step-1-classify',
      output:
        'Refund request classified as "policy-eligible" (synthetic mock response).',
      mock_hit: true,
      occurred_at: '2026-06-03T09:40:00Z',
    },
    {
      id: 'out-002-2',
      run_ref: 'run-002',
      step_key: 'step-2-lookup-order',
      output: 'Order #SYNTH-1042 resolved from the catalog mock.',
      mock_hit: true,
      occurred_at: '2026-06-03T09:40:01Z',
    },
    {
      id: 'out-002-3',
      run_ref: 'run-002',
      // The ledger mock is deliberately absent → deterministic mock-miss marker.
      step_key: 'step-3-ledger-balance',
      output: '[[mock-miss:ledger.balance]]',
      mock_hit: false,
      occurred_at: '2026-06-03T09:40:01Z',
    },
    {
      id: 'out-002-4',
      run_ref: 'run-002',
      step_key: 'step-4-draft-reply',
      output:
        'Drafted refund confirmation (synthetic) — no live ledger figure available.',
      mock_hit: true,
      occurred_at: '2026-06-03T09:40:02Z',
    },
  ],
  'run-001': [
    {
      id: 'out-001-1',
      run_ref: 'run-001',
      step_key: 'step-1-add-to-cart',
      output: 'Cart updated: 2 items (synthetic mock response).',
      mock_hit: true,
      occurred_at: '2026-06-03T08:12:00Z',
    },
    {
      id: 'out-001-2',
      run_ref: 'run-001',
      step_key: 'step-2-quote',
      output: 'Quote produced from the catalog mock: TOTAL=SYNTH.',
      mock_hit: true,
      occurred_at: '2026-06-03T08:12:01Z',
    },
  ],
}

export const comparisonsFixture: Comparison[] = [
  // improved — candidate beats baseline (positive delta).
  {
    id: 'cmp-improved',
    scenario_ref: 'scn-checkout-flow',
    baseline_run_ref: 'run-004',
    candidate_run_ref: 'run-001',
    subject_ref: 'agent:checkout',
    suite_ref: 'suite:checkout-golden',
    verdict: 'improved',
    baseline_score: 0.88,
    candidate_score: 0.94,
    delta: 0.06,
    decided_by: 'user:ops-eng',
    occurred_at: '2026-06-03T08:13:00Z',
  },
  // regressed — candidate worse than baseline (negative delta).
  {
    id: 'cmp-regressed',
    scenario_ref: 'scn-refund-policy',
    baseline_run_ref: 'run-003',
    candidate_run_ref: 'run-002',
    subject_ref: 'prompt:refund-eligibility',
    suite_ref: 'suite:checkout-golden',
    verdict: 'regressed',
    baseline_score: 0.81,
    candidate_score: 0.67,
    delta: -0.14,
    decided_by: 'user:qa',
    occurred_at: '2026-06-03T10:02:00Z',
  },
  // unchanged — within noise (zero delta).
  {
    id: 'cmp-unchanged',
    scenario_ref: 'scn-checkout-flow',
    baseline_run_ref: 'run-004',
    candidate_run_ref: 'run-003',
    subject_ref: 'agent:checkout',
    suite_ref: 'suite:checkout-golden',
    verdict: 'unchanged',
    baseline_score: 0.81,
    candidate_score: 0.81,
    delta: 0.0,
    decided_by: 'user:ops-eng',
    occurred_at: '2026-06-02T16:06:00Z',
  },
  // inconclusive — a side was not scored, so no verdict can be drawn.
  {
    id: 'cmp-inconclusive',
    scenario_ref: 'scn-refund-policy',
    baseline_run_ref: 'run-003',
    candidate_run_ref: 'run-002',
    subject_ref: 'prompt:refund-eligibility',
    suite_ref: null,
    verdict: 'inconclusive',
    baseline_score: 0.81,
    candidate_score: 0,
    delta: 0,
    decided_by: 'user:qa',
    occurred_at: '2026-06-03T09:41:00Z',
  },
]
