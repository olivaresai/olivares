// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Evals (module XII) endpoint wrappers + query keys. Thin `http.*` calls against the
// engine's `/v1/m/evals/…` routes (the web presents, never recomputes the
// score). Tenant-scoped keys include the active tenant so switching org refetches
// cleanly. Reads are RBAC-gated server-side; the UI mirrors that to hide the
// privileged write actions (launch run / A/B / monitor / pin baseline).
import { http, type TenantRequestOptions } from '@/lib/api/client'
import type { ListResponse } from '@/lib/api/types'
import type {
  AbRequest,
  AbResult,
  CaseResult,
  EvalRun,
  RunInput,
  Scorecard,
  Suite,
  AddCalibrationItemsRequest,
  Baseline,
  CalibrationItem,
  GateEvaluation,
  GateRequest,
  GateVerdict,
  PinBaselineRequest,
} from './types'

const BASE = '/v1/m/evals'
const LIST_CEILING = 1000
const CALIBRATION_ITEMS_CEILING = 25

/** GET /scorecards groups by one of these dimensions. */
export type ScorecardGroupBy = 'suite' | 'agent' | 'model' | 'prompt_variant'

export const evalsApi = {
  scorecards: (groupBy: ScorecardGroupBy) =>
    http.get<ListResponse<Scorecard>>(`${BASE}/scorecards`, {
      query: { by: groupBy },
    }),
  suites: () =>
    http.get<ListResponse<Suite>>(`${BASE}/suites`, {
      query: { limit: LIST_CEILING },
    }),
  runs: (params?: { suite_ref?: string; limit?: number }) =>
    http.get<ListResponse<EvalRun>>(`${BASE}/runs`, {
      query: { limit: LIST_CEILING, ...params },
    }),
  run: (id: string) => http.get<EvalRun>(`${BASE}/runs/${id}`),
  runResults: (id: string) =>
    http.get<ListResponse<CaseResult>>(`${BASE}/runs/${id}/results`),
  // The body is sent WHOLE, not assembled here: /ab takes suite_ref at its top
  // level and a bare {label, outputs} per variant. Wrapping two RunInputs as
  // {a, b} produced a body the engine could not decode at all.
  ab: (body: AbRequest) => http.post<AbResult>(`${BASE}/ab`, body),
  launchRun: (body: RunInput) => http.post<EvalRun>(`${BASE}/runs`, body),

  // ── C07-04 · las dieciséis rutas que la consola nunca llamaba ──────────────────────
  //
  // Medido contra `origin/main`: 23 rutas en `modules/evals/evals.go:182-221`, 7 llamadas
  // aquí. Los permisos van tal como el motor los registra, y NO son uniformes: los GET son
  // `read`, `POST /gate` y la calibración son `write`, y **`/baselines` y el override del
  // gate son `admin`** (`evals.go:202,221`). Fundirlos en un solo `canWrite` ofrecería a un
  // escritor la puerta de decisión, y el 403 llegaría del motor —que es la autoridad— pero
  // después de haber enseñado el botón.

  /** El suite entero, no sólo su fila de la lista. */
  suite: (id: string) =>
    http.get<Suite>(`${BASE}/suites/${encodeURIComponent(id)}`),
  createSuite: (body: Partial<Suite>) =>
    http.post<Suite>(`${BASE}/suites`, body),
  /** Los casos dorados contra los que se puntúa. Sin esto no hay forma de ver el dataset. */
  suiteCases: (id: string) =>
    http.get<ListResponse<CaseResult>>(
      `${BASE}/suites/${encodeURIComponent(id)}/cases`,
    ),
  /** Append-only: corregir un caso es una versión nueva del suite, no una edición. */
  addSuiteCase: (id: string, body: unknown) =>
    http.post<Suite>(`${BASE}/suites/${encodeURIComponent(id)}/cases`, body),
  archiveSuite: (id: string) =>
    http.post<Suite>(`${BASE}/suites/${encodeURIComponent(id)}/archive`, {}),

  /** Muestrea sesiones REALES de producción y las puntúa: la única vía de medir calidad
   *  sobre tráfico real. */
  monitor: (body: unknown) => http.post<EvalRun>(`${BASE}/monitor`, body),

  /** ADMIN. Fija el baseline de un par (suite, sujeto). Sin él, la detección de regresión
   *  no tiene contra qué comparar. */
  pinBaseline: (body: PinBaselineRequest) =>
    http.post<Baseline>(`${BASE}/baselines`, body),

  /** La verdad humana con la que se califica al juez LLM. */
  calibrationItems: (set: string | undefined, options: TenantRequestOptions) =>
    http.get<ListResponse<CalibrationItem>>(`${BASE}/calibration/items`, {
      ...options,
      query: { set, limit: CALIBRATION_ITEMS_CEILING },
    }),
  addCalibrationItems: (body: AddCalibrationItemsRequest) =>
    http.post<ListResponse<CalibrationItem>>(`${BASE}/calibration/items`, body),
  calibrationReports: (params?: { set?: string; judge_model?: string }) =>
    http.get<ListResponse<unknown>>(`${BASE}/calibration/reports`, {
      query: { limit: LIST_CEILING, ...params },
    }),
  /** Mide al juez contra el conjunto etiquetado (acuerdo, kappa, error medio). Sin juez
   *  cableado el motor devuelve 412 honesto en vez de simular un número. */
  runCalibration: (body: unknown, options: TenantRequestOptions) =>
    http.post<unknown>(`${BASE}/calibration/run`, body, options),

  /** El gate de regresión de CI. */
  gates: (params?: { suite_ref?: string; verdict?: GateVerdict }) =>
    http.get<ListResponse<GateEvaluation>>(`${BASE}/gate`, {
      query: { limit: LIST_CEILING, ...params },
    }),
  runGate: (body: GateRequest) =>
    http.post<GateEvaluation>(`${BASE}/gate`, body),
  /** La que CI vuelve a consultar tras una anulación para ver el veredicto EFECTIVO. */
  gate: (id: string) =>
    http.get<GateEvaluation>(`${BASE}/gate/${encodeURIComponent(id)}`),
  /** ADMIN. La válvula de escape GOBERNADA: anula un gate en fail/warn con motivo escrito,
   *  que queda en la fila y en el ledger junto al veredicto original. El motor rechaza con
   *  400 un motivo vacío (`gate.go:588`), con 409 un gate ya anulado y con 409 uno que ya
   *  pasa — «nothing to override». Una anulación no se deshace: se re-ejecuta el gate. */
  overrideGate: (id: string, reason: string) =>
    http.post<GateEvaluation>(
      `${BASE}/gate/${encodeURIComponent(id)}/override`,
      { reason },
    ),
}

export const evalsKeys = {
  gates: (tenant: string | null) => ['evals', tenant, 'gate'] as const,
  all: (tenant: string | null) => ['evals', tenant] as const,
  scorecards: (tenant: string | null, groupBy: string) =>
    ['evals', tenant, 'scorecards', groupBy] as const,
  suites: (tenant: string | null) => ['evals', tenant, 'suites'] as const,
  runs: (tenant: string | null, params?: unknown) =>
    params === undefined
      ? (['evals', tenant, 'runs'] as const)
      : (['evals', tenant, 'runs', params] as const),
  run: (tenant: string | null, id: string) =>
    ['evals', tenant, 'runs', id] as const,
  runResults: (tenant: string | null, id: string) =>
    ['evals', tenant, 'runs', id, 'results'] as const,
  calibrationItems: (tenant: string | null) =>
    ['evals', tenant, 'calibration', 'items'] as const,
}
