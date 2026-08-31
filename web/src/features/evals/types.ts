// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// DTOs for module XII (evals), mirroring the contract (docs/contracts/
//-sandbox.md §2 + §5 — the UI data contract). The engine measures quality;
// the web presents. INVARIANT: a candidate output is NEVER on the wire — a case result
// carries only a one-way `detail_hash` fingerprint + a short clamped `label`. A
// `skipped` outcome (e.g. llm_judge with no Judge wired) is NEVER a pass. Scores are
// floats 0..1; timestamps are RFC3339 UTC strings. No prompts/outputs/PII/secrets.

/** What a suite/run/scorecard is measuring. */
export type SubjectKind =
  | 'suite'
  | 'agent'
  | 'model'
  | 'prompt_variant'
  | 'prompt'
  | 'session'
  | 'sandbox_run'

/** Lifecycle status of an eval run. `degraded` = ran but a scorer (e.g. judge) was
 *  not wired and emitted `skipped`; honest, not a silent pass. */
export type RunStatus = 'running' | 'completed' | 'degraded' | 'error'

/** Per-case scoring outcome. `skipped` is NEUTRAL — never styled as a pass. */
export type CaseOutcome = 'pass' | 'fail' | 'error' | 'skipped'

/** Built-in deterministic scorers + the pluggable llm_judge. */
export type ScorerId =
  | 'exact'
  | 'contains'
  | 'not_contains'
  | 'regex'
  | 'json_valid'
  | 'json_equal'
  | 'numeric_range'
  | 'llm_judge'
  | string

/** One point on a scorecard's quality trend (ascending by time). */
export interface TrendPoint {
  at: string
  score: number
  pass_rate: number
}

/** GET /scorecards — an on-read aggregate per (suite|agent|model|prompt_variant). */
export interface Scorecard {
  key: string
  subject_kind: SubjectKind
  /**
   * ⛔ `pass_rate` ES LA MEDIA DE LAS CORRIDAS, NO LA TASA DE APROBACIÓN.
   * El motor lo dice en su propio comentario (`modules/evals/scorecards.go:45-49`):
   * se pairea con `pass_rate_ci`, que es un intervalo t sobre la serie POR CORRIDA.
   * Una corrida de 1 caso pesa lo mismo que una de 200. NO es #aprobados/#total.
   */
  pass_rate: number
  /**
   * La tasa de verdad: aprobados/puntuados sumados sobre TODAS las corridas, con
   * su denominador y su intervalo de Wilson (`scorecards.go:48-49,157-161`). El
   * motor la manda desde y la consola no la declaraba, así que la tarjeta
   * rotulaba «Pass-rate» sobre `pass_rate`. AUSENTE significa «no se puntuó nada»
   * (`a.pooledN > 0`), jamás «tasa 0».
   */
  pooled_pass_rate?: { rate: number; n: number; ci: { lo: number; hi: number } }
  mean_score: number
  runs: number
  last_score: number
  trend: TrendPoint[]
  regressed: boolean
}

/** GET /suites · /suites/{id} — a versioned golden-dataset definition. */
export interface Suite {
  id: string
  name: string
  description: string
  subject_kind: SubjectKind
  scorer: ScorerId
  /** The rubric/criterion — bounded, scrubbed of PII/secrets by the handler. */
  criterion: string
  pass_threshold: number
  regression_threshold: number
  /** Model used for `llm_judge`; absent ⇒ no judge wired ⇒ skipped. */
  judge_model?: string | null
  suite_version: number
  status: 'active' | 'archived'
}

/** GET /runs · /runs/{id} — a run lifecycle aggregate (created already terminal). */
export interface EvalRun {
  id: string
  suite_ref: string
  suite_version: number
  subject_kind: SubjectKind
  subject_ref: string
  model_ref?: string | null
  prompt_variant?: string | null
  scorer: ScorerId
  status: RunStatus
  total: number
  passed: number
  failed: number
  errors: number
  skipped: number
  score: number
  pass_rate: number
  /**
   * ⛔ EL DENOMINADOR DETRÁS DE `score` Y `pass_rate`, y el motor explica por qué lo manda
   * (`modules/evals/runs.go:400-402`): «reported so a reader can weigh the aggregate — **n=2 and
   * n=200 are different claims**».
   *
   * La consola lo ignoraba: pintaba «100 %» sin decir sobre cuántos casos. Un 100 % sobre dos
   * casos y otro sobre doscientos se leen igual en la tabla y no significan lo mismo, y quien
   * decide una release con eso decide sobre una cifra sin método.
   */
  /**
   * La línea base contra la que se resolvió la comparación (`runs.go:409`, `omitempty`).
   * ⛔ VACÍO significa que NO hubo comparación — la suite no comprueba regresiones, o no había
   * base que resolver. Lo converso NO vale: `baselineScore` devuelve la ref explícita aunque no
   * la encuentre (`runs.go:288-296`), así que presente ≠ comparada.
   */
  baseline_ref?: string
  n_scored?: number
  /** El intervalo de Wilson al 95 % para `pass_rate`. NULO cuando no se puntuó nada — la
   *  ausencia es «no hay intervalo», no «intervalo cero». */
  pass_rate_ci?: { lo: number; hi: number } | null
  /** A regression vs baseline emits a Finding (Kind=eval_regression) handled by the
   *  security view — here we only mark the run. */
  regressed: boolean
  /** Δ vs baseline (baseline.score − run.score). */
  drift: number
  started_at: string
  finished_at?: string | null
  launched_by: string
}

/** GET /runs/{id}/results — a per-case result. The candidate output is NOT stored;
 *  only the `detail_hash` fingerprint + a short clamped `label`. */
export interface CaseResult {
  id: string
  run_ref: string
  case_key: string
  scorer: ScorerId
  outcome: CaseOutcome
  score: number
  passed: boolean
  /** One-way hash of `output|expected|reason` — a fingerprint, never a payload. */
  detail_hash: string
  /** Short, clamped+scrubbed label for the UI. Never raw candidate text. */
  label: string
  occurred_at: string
}

/** One variant in an A/B comparison (modules/evals/ab.go abVariantResult). */
export interface AbVariant {
  label: string
  /** The run the engine persisted for this variant — an A/B scores AND stores. */
  run_ref: string
  score: number
  pass_rate: number
}

/** The order-swapped judged comparison (modules/evals/ab.go abPairwiseDTO). Present
 *  only when the request opted in with `pairwise`. `mode` is "judged" or "skipped"
 *  — a skip is DECLARED with its reason, never dressed up as a winner. */
export interface AbPairwise {
  mode: 'judged' | 'skipped'
  skip_reason?: string
  /** Variant label with more order-consistent wins; empty on a tie. */
  winner?: string
  compared: number
  a_wins: number
  b_wins: number
  ties: number
  /** Duals where the two presentation orders disagreed — the measured position bias. */
  inconsistent: number
  errors: number
  /** Fraction of judged duals where both orders agreed, WITH its denominator and
   *  95% Wilson interval (a rate without an n is not a defensible claim). */
  position_consistency?: {
    rate: number
    n: number
    ci: { lo: number; hi: number }
  }
}

/** POST /ab response — two variants scored against the SAME suite. */
export interface AbResult {
  variants: AbVariant[]
  /** The winning variant label; empty when scores are equal (see `tie`). */
  winner: string
  /** score(winner) − score(loser); 0 on an explicit tie. */
  delta: number
  /** The engine's EXPLICIT tie flag. Inferring a tie from `delta === 0` would
   *  read a 0-vs-0 comparison of two unscored variants as a real draw. */
  tie: boolean
  pairwise?: AbPairwise
}

/** One variant of a POST /ab request — the MIRROR of modules/evals/ab.go
 *  abVariantInput, which carries a label and an output set and NOTHING else. */
export interface AbVariantInput {
  /** Shown in the result and stored as the run's variant; the engine defaults it
   *  to "A"/"B" when empty. */
  label: string
  /** case_key → candidate output. The engine scores these and keeps only a hash. */
  outputs: Record<string, string>
}

/**
 * POST /ab body — the MIRROR of modules/evals/ab.go abRequest, and deliberately
 * NOT `RunInput`.
 *
 * RunInput is the POST /runs body: it carries suite_ref, subject_kind, subject_ref,
 * model_ref and prompt_variant at its top level. The console used to send it as
 * BOTH `a` and `b`, and the engine decodes with DisallowUnknownFields
 * (modules/evals/helpers.go:85) — so the first unknown key inside `a` killed the
 * whole decode and answered 400 "invalid JSON body" (measured against the real
 * handler, ab_console_contract_test.go). The A/B comparison had never worked.
 *
 * Moving suite_ref up would not have fixed it: subject_kind, subject_ref and
 * model_ref would still be inside `a`, and the decoder rejects them just the same.
 * The shapes are different contracts, so they get different types.
 */
export interface AbRequest {
  suite_ref: string
  subject_kind?: SubjectKind
  subject_ref?: string
  a: AbVariantInput
  b: AbVariantInput
  /** Opt into the order-swapped judged comparison: each shared case is judged
   *  TWICE with the candidates' order swapped, and a win counts only when both
   *  orders agree. Billable (two judge calls per case) and the suite must carry a
   *  criterion, so it is off unless the operator asks. */
  pairwise?: boolean
}

/** POST /runs body — outputs are provided by the caller (sandbox/CI/inline) so the
 *  run is hermetic. The candidate output is hashed before storage by the engine. */
export interface RunInput {
  suite_ref: string
  subject_kind: SubjectKind
  subject_ref: string
  model_ref?: string
  prompt_variant?: string
  /** case_key → candidate output (scored, then discarded — only the hash persists). */
  outputs: Record<string, string>
}

// ── C07-04 · las dieciséis rutas de evals que la consola nunca llamaba ────────────────
//
// Medido el 2026-08-17 contra `origin/main`: el motor registra 23 rutas
// (`modules/evals/evals.go:182-221`) y el cliente escrito a mano llamaba 7. Entre las
// dieciséis que faltaban están las que DECIDEN: fijar el baseline contra el que se mide una
// regresión, etiquetar la verdad humana con la que se califica al juez, y anular un gate de CI.
// Hasta hoy, desbloquear una release parada por calidad sólo se podía hacer con `curl`.

/** El veredicto del gate de regresión de CI (`modules/evals/gate.go`). */
export type GateVerdict = 'pass' | 'warn' | 'fail'

/**
 * Una evaluación del gate de CI.
 *
 * ⛔ DOS VEREDICTOS, Y NO SE FUNDEN. `verdict` es lo que midió el gate; `effective_verdict` es
 * lo que CI debe obedecer — el mismo, o `pass` después de una anulación GOBERNADA
 * (`gate.go:70-72`). Enseñar sólo el efectivo esconde que una persona desbloqueó la release;
 * enseñar sólo el original esconde que CI recibió luz verde. La pantalla enseña los dos y,
 * cuando difieren, quién lo anuló y con qué motivo escrito.
 */
export interface GateEvaluation {
  id: string
  suite_ref: string
  subject_ref?: string
  verdict: GateVerdict
  reasons?: string[]
  effective_verdict: GateVerdict
  run_ref?: string
  baseline_ref?: string
  sampled: number
  total_cases: number
  /**
   * ⛔ EL ESTIMADOR CORREGIDO DE SESGO (Rogan–Gladen, `modules/evals/gate.go:89-97`): la tasa que
   * midió el juez, corregida por la sensibilidad y especificidad REALES de ese juez según su
   * informe de calibración de confianza. El motor lo dice con todas las letras: **«Surfaced
   * alongside — never instead of — the raw rate»**.
   *
   * ⛔ Y SU AUSENCIA NO ES «igual a la cruda»: es `omitempty` con puntero, así que no llega cuando
   * no hay calibración de confianza con la que corregir. Un juez sin medir no permite estimar su
   * sesgo, y presentar la tasa cruda a secas en ese caso afirma una precisión que nadie ha
   * comprobado — en la pantalla que decide si CI bloquea un merge.
   */
  corrected_pass_rate?: {
    pass_rate: number
    sensitivity: number
    specificity: number
  }
  calibration?: {
    report_ref?: string
    agreement?: number
    kappa?: number
    meets_target: boolean
  }
  seed?: string
  judge_model?: string
  cache_hits?: number
  overridden: boolean
  override_by?: string
  override_reason?: string
  launched_by?: string
  occurred_at: string
}

/** El cuerpo de `POST /gate`: el sujeto y sus salidas, con muestreo determinista. */
export interface GateRequest {
  suite_ref: string
  subject_kind?: string
  subject_ref?: string
  baseline_ref?: string
  outputs: Record<string, string>
  /** Vacío deriva una semilla estable de la identidad del suite: dos corridas del MISMO
   *  suite juzgan el MISMO subconjunto. No es aleatorio. */
  seed?: string
  /** 0 = todos los casos. La muestra son los `sample_size` casos con menor
   *  hash(seed|case_key) — determinista. */
  sample_size?: number
}

/** El baseline fijado para un par (suite, sujeto) — la superficie de decisión de admin. */
export interface Baseline {
  id: string
  suite_ref: string
  subject_ref: string
  run_ref: string
  pinned_by?: string
}

export interface PinBaselineRequest {
  suite_ref: string
  subject_ref: string
  run_ref: string
}

/** Un item de referencia etiquetado por un humano: la verdad contra la que se mide al juez. */
export interface CalibrationItem {
  case_key: string
  input?: string
  output: string
  expected?: string
  criterion?: string
  human_passed: boolean
  human_score?: number
  notes?: string
}

export interface AddCalibrationItemsRequest {
  set_name?: string
  items: CalibrationItem[]
}
