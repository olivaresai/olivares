// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// API for the Claude Code governance console. ARCHITECTURE.md — presentation over
// the SAME API, no logic here.
//
// CONTRACT PROVENANCE (honest):
//  • DRIFT reads hit the REAL security findings endpoint (connectors emit them).
//  • AUTHORING (validate/dry-run/publish/version) + PDP (validate/explain/dry-run)
//    are the DECLARED contract this session EXPOSES (docs/contracts-*) for
//    To implement. They are wired to their declared routes; today they
//    will 404/501 and `isContractPending()` lets the UI render an honest "backend
//    pending" seam instead of a fake success or a red error.
//  • Schema VALIDATION is done LOCALLY against the verified schemas (real value,
//    no backend needed) — see ./schema.
import { http } from '@/lib/api'
import { ApiError } from '@/lib/api/errors'
import type { ListResponse } from '@/lib/api/types'
import type { PolicySurface } from './schema'
import type {
  DryRunResult,
  PdpActivePolicy,
  PdpDecision,
  PdpEngine,
  PdpExampleRequest,
  PdpPublishResult,
  PdpRevision,
  PdpRollbackResult,
  PdpTestStatus,
  PdpValidateResult,
  PolicyDistributionView,
  PolicyDriftFinding,
  PolicyVersion,
  PublishResult,
  ThreadEvent,
  ToolConfirmationInput,
} from './types'

/** Declared authoring namespace (contract (a)).*/
const BASE = '/v1/m/claude-policy'
/** Declared policy-as-code namespace under governance (contract (b) PDP).*/
const PDP = '/v1/m/governance/pdp'

/**
 * ⛔ EL TECHO SE PIDE, NO SE HEREDA — PERO SÓLO DONDE HAY ALGO QUE RECORTAR.
 *
 * Empecé poniéndolo en las cinco listas de este cliente. El contraste lo refutó para tres, y lo
 * comprobé en el motor: `handleListVersions` y `pdpVersions` **no miran la query** y llaman a
 * `listRevisions`, que *«drains every page so the version history is complete»* —su propio
 * comentario— y reordena por revisión descendente; `handleThreadEvents` tampoco mira la query.
 * Ahí no hay página que elevar ni recorte que declarar: pedir `limit` habría sido decorativo y el
 * aviso, **inalcanzable**. Un aviso que no puede aparecer no protege: sólo afirma que protege.
 *
 * Quedan las dos que sí paginan, y las dos van al mismo sitio —`/v1/m/security/findings`—, donde
 * el store aplica 100 por defecto, acepta hasta 1000 y ordena por `id ASC` al no haber `Sort`.
 * En una pantalla de política ese recorte es lo peor que puede pasar: un hallazgo de deriva que no
 * sale se lee como que **no hay deriva**. La ausencia aquí es la afirmación.
 */
const POLICY_PAGE = 1000
/** REAL findings namespace (connectors → security module).*/
const SECURITY = '/v1/m/security'
/** Managed-Agents namespace (contract (c)); thread events are served LIVE
 *  through the reader when a claude-managed-agents source is wired.*/
const CLAUDE_AGENTS = '/v1/m/claude-agents'

export interface DriftParams {
  status?: string
  kind?: string
  cursor?: string
  limit?: number
}

export const claudePolicyApi = {
  // --- REAL: drift / posture Findings ----------------------------------------
  /** PERMITTED-policy-vs-OBSERVED-config drift Findings for the Claude surfaces. */
  listDrift: (params?: DriftParams) =>
    http.get<ListResponse<PolicyDriftFinding>>(`${SECURITY}/findings`, {
      query: { kind: 'policy_drift', limit: POLICY_PAGE, ...params },
    }),
  getFinding: (id: string) =>
    http.get<PolicyDriftFinding>(
      `${SECURITY}/findings/${encodeURIComponent(id)}`,
    ),

  // --- DECLARED: managed-* authoring -----------------------------------------
  /** Preview the resolved precedence/effect of a change WITHOUT publishing. */
  dryRun: (surface: PolicySurface, content: string) =>
    http.post<DryRunResult>(`${BASE}/${surface}/dry-run`, { content }),
  /** Publish = privileged + audited: backend distributes + runs drift verification.
   *  The UI never writes host files. */
  publish: (surface: PolicySurface, content: string) =>
    http.post<PublishResult>(`${BASE}/${surface}/publish`, { content }),
  // Sin techo A PROPÓSITO: el handler drena todas las páginas y devuelve el historial completo
  // (`modules/governance/revision.go`, `listRevisions`). No hay recorte que declarar.
  listVersions: (surface: PolicySurface) =>
    http.get<ListResponse<PolicyVersion>>(`${BASE}/${surface}/versions`),
  getVersion: (surface: PolicySurface, revision: number) =>
    http.get<PolicyVersion>(`${BASE}/${surface}/versions/${revision}`),
  /** truth view: published vs signed-for-distribution vs what every scope's
   *  attested check-in reports. Real state only; absences are named in notes. */
  getDistribution: (surface: PolicySurface) =>
    http.get<PolicyDistributionView>(`${BASE}/${surface}/distribution`),

  // --- DECLARED: Cedar/OPA policy-as-code (PDP) --------------------------
  pdpValidate: (engine: PdpEngine, source: string) =>
    http.post<PdpValidateResult>(`${PDP}/validate`, { engine, source }),
  /** decision-explain: why a request was permitted/denied (deny-only semantics). */
  pdpExplain: (engine: PdpEngine, source: string, request: PdpExampleRequest) =>
    http.post<PdpDecision>(`${PDP}/explain`, { engine, source, request }),
  /** dry-run a proposed policy against an example request WITHOUT publishing. */
  pdpDryRun: (engine: PdpEngine, source: string, request: PdpExampleRequest) =>
    http.post<PdpDecision>(`${PDP}/dry-run`, { engine, source, request }),
  /** ONE flat list carrying BOTH engines' revisions, distinguished only by
   *  `surface`, and NOT globally sorted (all cedar desc, then all opa desc).
   *  Group by surface before rendering; never scan it for "the" active row. */
  // Sin techo A PROPÓSITO, y por lo mismo: reutiliza `listRevisions` para Cedar y luego OPA.
  pdpVersions: () => http.get<ListResponse<PdpRevision>>(`${PDP}/versions`),
  /** ONE stored revision WITH its content. `engine` is required: revision numbers
   *  are per-surface, so cedar r1 and opa r1 are different documents. */
  pdpGetVersion: (engine: PdpEngine, revision: number) =>
    http.get<PdpRevision>(`${PDP}/versions/${revision}`, { query: { engine } }),
  /** What the STORE selects, plus (for cedar) the managed/adopted surfaces that
   *  are unioned into the enforced policy — presence/revision/digest only. */
  pdpActive: (engine: PdpEngine) =>
    http.get<PdpActivePolicy>(`${PDP}/active`, { query: { engine } }),
  /** Stores a new immutable revision and, for cedar, activates it on the live
   *  engine. Read `live_activation` — `active` alone does not prove enforcement. */
  pdpPublish: (engine: PdpEngine, source: string, note?: string) =>
    http.post<PdpPublishResult>(`${PDP}/publish`, { engine, source, note }),
  /** Appends an activation record selecting an EXISTING revision. It creates no
   *  new revision and deletes none: the immutable history is preserved. */
  pdpRollback: (engine: PdpEngine, revision: number) =>
    http.post<PdpRollbackResult>(`${PDP}/rollback`, { engine, revision }),
  /** The stored compile/validate-gate artifact for ONE revision. `revision` is
   *  passed explicitly ALWAYS: omitting it makes the engine pick the NEWEST
   *  revision, which diverges from the active one after a rollback. */
  pdpTestStatus: (engine: PdpEngine, revision?: number) =>
    http.get<PdpTestStatus>(`${PDP}/tests`, { query: { engine, revision } }),

  // --- Managed-Agents HITL (ANT2-14) -----------------------------------------
  /** REAL: pending user.tool_confirmation findings (connector → security).
   *  Filtered to the managed-agent subject; redacted (detail_hash only). */
  listManagedAgentHitl: (params?: DriftParams) =>
    http.get<ListResponse<PolicyDriftFinding>>(`${SECURITY}/findings`, {
      query: {
        kind: 'governance',
        subject_kind: 'anthropic.managed_agent',
        limit: POLICY_PAGE,
        ...params,
      },
    }),
  /** DECLARED: the thread events that carry the concrete tool to confirm. */
  // Sin techo A PROPÓSITO: `handleThreadEvents` no mira la query. ⚠ Y aquí hay un recorte REAL
  // que NO se puede declarar desde el cliente: el conector corta en `max_pages` y devuelve la
  // lista parcial con `has_more:false` (`connectors/claude-managed-agents/threads.go`). Reportado.
  threadEvents: (sessionId: string) =>
    http.get<ListResponse<ThreadEvent>>(
      `${CLAUDE_AGENTS}/sessions/${encodeURIComponent(sessionId)}/events`,
    ),
  /** DECLARED: emit user.tool_confirmation (allow/deny) — privileged + audited. */
  confirmTool: (sessionId: string, input: ToolConfirmationInput) =>
    http.post<void>(
      `${CLAUDE_AGENTS}/sessions/${encodeURIComponent(sessionId)}/tool-confirmation`,
      input,
    ),
}

/**
 * True when a DECLARED endpoint is not yet implemented by the backend (404 / 501 /
 * 405). The UI uses this to show an honest "authoring API pending" seam
 * — never a fake success, never a red error.
 */
export function isContractPending(error: unknown): boolean {
  if (!(error instanceof ApiError)) return false
  return error.status === 404 || error.status === 501 || error.status === 405
}

/** Tenant-scoped query keys (query.ts contract: tenant id first). */
export const claudePolicyKeys = {
  all: (t: string | null) => ['claude-policy', t] as const,
  drift: (t: string | null, params?: unknown) =>
    params === undefined
      ? (['claude-policy', t, 'drift'] as const)
      : (['claude-policy', t, 'drift', params] as const),
  finding: (t: string | null, id: string) =>
    ['claude-policy', t, 'finding', id] as const,
  versions: (t: string | null, surface: PolicySurface) =>
    ['claude-policy', t, 'versions', surface] as const,
  distribution: (t: string | null, surface: PolicySurface) =>
    ['claude-policy', t, 'distribution', surface] as const,
  /** Prefix for every Cedar/OPA PDP read — a publish/activation invalidates the
   *  whole lifecycle at once (versions, active, per-revision gate). */
  pdp: (t: string | null) => ['claude-policy', t, 'pdp'] as const,
  pdpVersions: (t: string | null) =>
    ['claude-policy', t, 'pdp', 'versions'] as const,
  pdpActive: (t: string | null, engine: PdpEngine) =>
    ['claude-policy', t, 'pdp', 'active', engine] as const,
  pdpVersion: (t: string | null, engine: PdpEngine, revision: number) =>
    ['claude-policy', t, 'pdp', 'version', engine, revision] as const,
  /** Keyed by revision: the gate artifact is per-revision, and the active
   *  revision changes under a rollback without the engine changing. */
  pdpTests: (t: string | null, engine: PdpEngine, revision?: number) =>
    ['claude-policy', t, 'pdp', 'tests', engine, revision ?? null] as const,
  hitl: (t: string | null) =>
    ['claude-policy', t, 'managed-agent-hitl'] as const,
  threadEvents: (t: string | null, sessionId: string) =>
    ['claude-policy', t, 'thread-events', sessionId] as const,
}
