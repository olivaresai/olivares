// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// DTOs for module XVII (Testing-sandbox), mirroring the contract
// (the evals-sandbox contract, §3 + §5). Field names are the snake_case
// JSON tags. Timestamps are RFC3339 UTC strings. There is no money on this surface.
//
// HONESTY (encoded in the types, enforced in the view): isolation is REAL, not
// faked — `runner` + `isolated` are recorded literally; an unscored run is
// `degraded` ("executed, not scored"), NEVER a pass (`score`/`passed` are
// nullable, never invented). A `sandbox.output` carries BOUNDED SYNTHETIC text from
// mocks — never raw text from a real target; a mock-miss is a deterministic marker
// (e.g. "[[mock-miss:<resource>]]"). Synthetic-data generation is POST-v1 / not
// implemented (a seam, never pretended).

/** What a run did: a scenario sim, a deterministic replay, or a pre/post compare. */
export type RunKind = 'scenario' | 'replay' | 'compare'

/** The backend that executed the run. `inproc-mock` is isolated by construction;
 *  `container`/`microvm` are the pluggable OS-level backends (deferred). */
export type Runner = 'inproc-mock' | 'container' | 'microvm' | string

/** Run lifecycle. `degraded` = executed but NOT scored (no Scorer wired) — it is
 *  explicitly NOT a pass. `error` = the run itself failed. */
export type RunStatus = 'running' | 'completed' | 'degraded' | 'error'

/** A scenario: a sequence of synthetic step inputs + mocked MCP/resource responses. */
export type ScenarioStatus = 'active' | 'archived'

/** One synthetic step of a scenario spec. `key` identifies the step's output row;
 *  the engine assigns `step-<n>` when the operator leaves it blank. */
export interface ScenarioStep {
  key: string
  input: string
}

/** One mocked MCP/resource response. Operator-authored, never a secret (contract). */
export interface ScenarioMock {
  resource: string
  response: string
}

/**
 * GET /scenarios item / GET /scenarios/{id}. No secrets in `mocks` (contract).
 *
 * THIS MIRRORS `scenarioDTO` (modules/sandbox/scenarios.go:33-51), field for field,
 * because it did not. Until it declared `steps_count: number` and
 * `created_at: string`: the engine projects NEITHER — `toScenarioDTO` builds the
 * payload from eight fields and neither is among them — so against a live engine the
 * scenarios table's "Steps" column rendered `undefined`, and every fixture agreed with
 * the type instead of with the wire. What the engine DOES send is the steps and mocks
 * themselves, so the count is derived here (`stepCount`) rather than invented there.
 * Its three `omitempty` tags — description, subject_kind, spec_hash — are optional
 * properties for the same reason: an empty description is ABSENT from the payload,
 * not "".
 */
export interface Scenario {
  id: string
  name: string
  description?: string
  subject_kind?: string
  /** The declared steps, as stored. Always present (the engine encodes `[]`). */
  steps: ScenarioStep[]
  /** The declared mocks, as stored. Always present (the engine encodes `[]`). */
  mocks: ScenarioMock[]
  /** Hash of the scenario spec (steps + mocks) — a fingerprint, never the payload. */
  spec_hash?: string
  status: ScenarioStatus
}

/** GET /runs item / GET /runs/{id}. */
export interface Run {
  id: string
  /** Nullable: a replay/compare may not be tied to a stored scenario. */
  scenario_ref?: string | null
  kind: RunKind
  subject_ref: string
  /** A/B label (e.g. baseline/candidate variant) when relevant. */
  variant?: string | null
  runner: Runner
  /** TRUE if the runner guarantees isolation (no egress / secrets / prod). Literal. */
  isolated: boolean
  status: RunStatus
  steps_total: number
  steps_ok: number
  steps_error: number
  /** Fingerprint of the produced outputs — never the outputs themselves. */
  outputs_hash: string
  /** Only present if the run was scored; absent ⇒ degraded ("executed, not scored"). */
  score?: number | null
  passed?: boolean | null
  /** The evals suite the run was scored against, when scored. */
  suite_ref?: string | null
  /** TRUE = the ephemeral environment's state was discarded (isolation guarantee). */
  destroyed: boolean
  started_at: string
  finished_at?: string | null
  launched_by: string
}

/**
 * GET /runs/{id}/outputs item, and the payload of an `event: output` frame on
 * GET /runs/{id}/stream — one bounded synthetic output per step.
 *
 * `id` was MISSING here until while the engine had always sent it
 * (modules/sandbox/runs.go:409, no `omitempty`, always populated from ColID). The
 * omission mattered the moment a second consumer arrived: `step_key` is NOT a unique
 * identity — the engine only fills a BLANK key with `step-<n>` and never rejects a
 * duplicate (modules/sandbox/scenarios.go:259-263) — so a scenario declaring the same
 * key twice yields two legitimate output rows. Deduping a stream replay on `step_key`
 * would silently drop the second, and `key={o.step_key}` was already a duplicate React
 * key in that case.
 */
export interface Output {
  /** Row identity — the only unique key on this payload. */
  id: string
  run_ref: string
  step_key: string
  /** BOUNDED SYNTHETIC text from the mock runner. A mock-miss is a deterministic
   *  marker like "[[mock-miss:<resource>]]" — NEVER a real resource. Raw target
   *  text is never stored. */
  output: string
  /** TRUE = the step resolved against a declared mock; FALSE = a mock-miss. */
  mock_hit: boolean
  occurred_at: string
}

/**
 * The `event: summary` frame of GET /runs/{id}/stream — the run aggregate the engine
 * emits AFTER the per-step outputs and before `event: done`
 * (modules/sandbox/stream.go:26-36).
 *
 * It is a SUBSET of `Run`, not a copy of it: the frame carries no scenario/subject
 * refs, no score, no timestamps. It is typed separately rather than reused as
 * `Partial<Run>` because it is a different wire message with its own fields, and
 * widening `Run` to fit would let a view read a field the frame never carries.
 */
export interface RunStreamSummary {
  run_id: string
  kind: RunKind
  runner: Runner
  isolated: boolean
  status: RunStatus
  steps_total: number
  steps_ok: number
  steps_error: number
  destroyed: boolean
}

/** Pre/post-deploy verdict. `improved`=success, `regressed`=danger,
 *  `unchanged`=neutral, `inconclusive`=warning. */
export type ComparisonVerdict =
  | 'improved'
  | 'regressed'
  | 'unchanged'
  | 'inconclusive'

/** GET /comparisons item / GET /comparisons/{id} — append-only deploy evidence. */
export interface Comparison {
  id: string
  scenario_ref?: string | null
  baseline_run_ref: string
  candidate_run_ref: string
  subject_ref: string
  suite_ref?: string | null
  verdict: ComparisonVerdict
  baseline_score: number
  candidate_score: number
  /** Signed delta = candidate_score − baseline_score. */
  delta: number
  decided_by: string
  occurred_at: string
}

/**
 * POST /scenarios body (sandbox:scenario:write) — an operator-authored fixture.
 *
 * Shaped EXACTLY like the engine's `createScenarioRequest`
 * (modules/sandbox/scenarios.go:53-59): the handler decodes with
 * `DisallowUnknownFields`, so one extra property is a 400 for the whole request, and
 * a body is ONE document. Only `name` is required (the handler 400s on a blank one);
 * everything else the engine clamps — name/subject_kind to 200 chars, description to
 * 1024, a step input to 8192, a mock response to 8192 (modules/sandbox/helpers.go:29-32).
 */
export interface CreateScenarioInput {
  name: string
  description?: string
  subject_kind?: string
  steps?: ScenarioStep[]
  mocks?: ScenarioMock[]
}

/** POST /scenarios/{id}/run body (sandbox:run:write) — launch a scenario sim. */
export interface RunScenarioInput {
  /** Optional A/B/variant label to record on the run. */
  variant?: string
  /** Optional evals suite to score the produced outputs against. */
  suite_ref?: string
}

/** POST /replay body (sandbox:run:write) — deterministic replay of a session. */
export interface ReplayInput {
  session_ref: string
  variant?: string
  suite_ref?: string
}

/** POST /compare body (sandbox:run:admin) — pre/post-deploy decision. */
export interface CompareInput {
  scenario_ref?: string
  session_ref?: string
  baseline_variant: string
  candidate_variant: string
  suite_ref?: string
}
