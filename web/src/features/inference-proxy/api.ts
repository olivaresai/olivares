// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Inference-proxy administration. Tenant-scoped module routes under
// /v1/m/inferenceproxy/ (modules/inferenceproxy/inferenceproxy.go APIRoutes):
//   GET/PUT  /config            — the per-tenant proxy gates + ceilings
//   GET/PUT  /dlp/rules         — egress DLP allow/deny rules by classification
//   DELETE   /dlp/rules/{id}
//   POST     /device/approve    — approve/deny a device-authorization user code
// Reads are viewer-tier; config writes editor-tier; DLP writes admin-tier. The
// console mirrors those tiers and adds an AAL3 step-up on the mutating actions.
import { http } from '@/lib/api/client'
import type { ListResponse } from '@/lib/api/types'
import { EVIDENCE_PAGE } from '@/features/models/api'

const BASE = '/v1/m/inferenceproxy'

/** Egress-DLP response handling: off | flag (annotate) | buffer (hold for scan). */
export type ResponseDLPMode = 'off' | 'flag' | 'buffer'

/**
 * The per-tenant proxy configuration (modules/inferenceproxy configDTO). The `gate_*`
 * and `ceilings_enforce` fields are pointers server-side: omitting one keeps the
 * documented default (`gate_model_access` defaults ON; `ceilings_enforce` defaults
 * OFF). The console always reads then re-sends explicit booleans, so no gate flips by
 * omission. `ceiling_task_budget_tokens` must be 0 (unset) or ≥ 20000.
 *
 * `record_mandatory` IS THE EXCEPTION TO THAT RULE, and it is deliberate. Server-side
 * it is backed by two columns — the value, and a nullable `record_mandatory_chosen`
 * whose NULL means nobody decided — and the handler infers the second from the field
 * being PRESENT in the request. Re-sending it the way the gates are re-sent therefore
 * records an evidence decision the operator never made, and a tenant marked as having
 * chosen stops yielding to the audit spool's declared `degrade`. On an UPDATE the
 * server keeps whatever the row says when the field is omitted, so leaving it out is
 * both safe and the only honest way to say "this save was not about evidence".
 */
export interface ProxyConfig {
  fail_open: boolean
  response_dlp_mode?: ResponseDLPMode
  record_mandatory?: boolean
  gate_model_access?: boolean
  gate_budget?: boolean
  gate_residency?: boolean
  gate_context_window?: boolean
  gate_dlp_request?: boolean
  gate_dlp_response?: boolean
  ceilings_enforce?: boolean
  ceiling_max_tokens?: number
  ceiling_max_tool_uses?: number
  ceiling_task_budget_tokens?: number
}

/** One egress-DLP rule: a classification `class` mapped to allow/deny (dlpRuleDTO). */
export interface DLPRule {
  id?: string
  class: string
  action: 'allow' | 'deny'
  note?: string
  created_by?: string
}

/** The outcome of a device-authorization decision (devicegrant.go). */
export interface DeviceGrantResult {
  id: string
  user_code: string
  state: string
}

/** El techo de la lista de reglas DLP. Mismo valor que el `maxLimit` del store
 *  (`core/internal/store/sqlstore/generic.go:26-29`): pedir mas seria pedir algo que el motor
 *  recorta igual, y pedir menos seria recortar dos veces. */
// ⛔ AQUI VIVIA `const DLP_PAGE = 1000`. Retirado al integrar: su uso pasa al EVIDENCE_PAGE central.

export const inferenceProxyApi = {
  getConfig: () => http.get<ProxyConfig>(`${BASE}/config`),
  putConfig: (body: ProxyConfig) =>
    http.put<ProxyConfig>(`${BASE}/config`, body),

  /**
   * ⛔ EL TECHO SE PIDE, NO SE HEREDA. Sin `limit`, el motor sirve su pagina por defecto —100
   * filas (`core/internal/store/sqlstore/generic.go:26-29`, `defaultLimit`)— y esta llamada no
   * mandaba ninguno: `http.get(...)` a secas. Una consola que ensena 100 reglas DLP de N y no lo
   * dice deja creer que el egreso esta gobernado por esas 100.
   *
   * Verificado en el motor antes de poner el techo, que es lo que decide el remedio: el handler
   * PAGINA (no drena), asi que hay recorte real que declarar y el techo no es decorativo.
   *
   * No lleva `...params` a proposito: esta llamada no acepta ninguno, asi que no hay orden de
   * spread que discutir — el techo no se puede borrar desde el llamante porque no hay llamante
   * que pueda pasar `limit`.
   *
   * ⛔ TECHO CENTRAL al integrar el 2026-08-29: la rama declaraba `DLP_PAGE` local; se usa
   * `EVIDENCE_PAGE`, que es el que la campana centralizo. Un techo por fichero vuelve a
   * fragmentar lo que se acababa de unificar, y con otro nombre nadie lo ve.
   */
  listDLPRules: () =>
    http.get<ListResponse<DLPRule>>(`${BASE}/dlp/rules`, {
      query: { limit: EVIDENCE_PAGE },
    }),
  // PUT is upsert by class: 201 when created, 200 when updated (both return the rule).
  putDLPRule: (body: DLPRule) => http.put<DLPRule>(`${BASE}/dlp/rules`, body),
  deleteDLPRule: (id: string) =>
    http.delete<void>(`${BASE}/dlp/rules/${encodeURIComponent(id)}`),

  // Approve or deny a device-authorization user code. 404 = unknown code; 410 = the
  // grant expired or was already consumed — both surfaced honestly to the operator.
  approveDevice: (input: { user_code: string; deny?: boolean }) =>
    http.post<DeviceGrantResult>(`${BASE}/device/approve`, input),
}

export const inferenceProxyKeys = {
  config: (t: string | null) => ['inferenceproxy', t, 'config'] as const,
  dlpRules: (t: string | null) => ['inferenceproxy', t, 'dlpRules'] as const,
}
