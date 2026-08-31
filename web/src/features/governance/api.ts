// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Endpoint helpers + query keys for the governance module (VI). Thin wrappers over
// the core HTTP client against /v1/m/governance (ARCHITECTURE.md — no logic here). The
// active tenant header is attached automatically; tenant-scoped keys cache-isolate
// per tenant (query.ts contract).
//
// Pagination note (spec gap #5): /identities, /groups, /policies, /approvals return
// real cursor+has_more; but /bindings, /groups/{ref}/members and
// /approvals/{id}/decisions drain internally (has_more:false, no cursor) — callers
// must NOT render "load more" for those three.
import { http } from '@/lib/api'
import { ApiError } from '@/lib/api/errors'
import type { ListResponse } from '@/lib/api/types'
import type {
  AgentRegistrationInput,
  AgentRegistrationOutcome,
  EmergingStandardsResponse,
  AgentCoreApplyOutcome,
  AgentCoreExportResultDTO,
  AgentCoreEnforcementMode,
  AgentCoreExportApplyDTO,
  AgentCoreExportApplyInput,
  AgentCoreExportPendingDTO,
  AgentCoreExportPlan,
  AgentRiskProfileDTO,
  ApprovalDTO,
  ActivateBreakGlassInput,
  BindRequest,
  BindResponse,
  BindingDTO,
  BreakGlassDTO,
  BreakGlassUseDTO,
  ClassifyAgentRiskInput,
  CreateApprovalRequest,
  DecisionDTO,
  DecisionInput,
  GroupDTO,
  IdentityDTO,
  MemberDTO,
  PolicyDTO,
  PolicyInput,
  RosterReport,
  ReviewBreakGlassInput,
  RoutinePolicyDTO,
  RoutinePostureDTO,
  CreateRoutinePolicyInput,
  UpdateRoutinePolicyInput,
  SetAgentRiskTierInput,
  SweepReport,
} from './types'

const BASE = '/v1/m/governance'

export interface ListParams {
  cursor?: string
  limit?: number
}

export const governanceApi = {
  // --- identities & groups ---------------------------------------------------
  listIdentities: (params?: ListParams & { source?: string; kind?: string }) =>
    http.get<ListResponse<IdentityDTO>>(`${BASE}/identities`, {
      query: { ...params },
    }),
  listGroups: (params?: ListParams & { source?: string; kind?: string }) =>
    http.get<ListResponse<GroupDTO>>(`${BASE}/groups`, {
      query: { ...params },
    }),
  listGroupMembers: (ref: string, params?: { transitive?: boolean }) =>
    http.get<ListResponse<MemberDTO>>(
      `${BASE}/groups/${encodeURIComponent(ref)}/members`,
      { query: { ...params } },
    ),
  syncRoster: () => http.post<RosterReport>(`${BASE}/roster/sync`),

  /** El registro «design-toward» de estándares emergentes. Lectura pura. */
  emergingStandards: () =>
    http.get<EmergingStandardsResponse>(`${BASE}/emerging-identity-standards`),

  // --- agent ↔ identity bindings (NHI) ---------------------------------------
  listBindings: () => http.get<ListResponse<BindingDTO>>(`${BASE}/bindings`),
  /** Registra (o PROMUEVE) una identidad de agente. `governance:nhi:admin`.
   *
   *  Usa `postWithMeta` a propósito: el éxito NO trae cuerpo y el único dato es el código
   *  —201 creado, 200 promovido—. Leer el cuerpo daría `undefined`, y decir «creado» ante una
   *  promoción sería mentir sobre lo que pasó. */
  registerAgent: async (
    body: AgentRegistrationInput,
  ): Promise<AgentRegistrationOutcome> => {
    const res = await http.postWithMeta<unknown>(`${BASE}/agents`, body)
    return { promoted: res.status === 200 }
  },

  bindAgentIdentity: (agentID: string, input: BindRequest) =>
    http.post<BindResponse>(
      `${BASE}/agents/${encodeURIComponent(agentID)}/identity`,
      input,
    ),
  unbindAgentIdentity: (agentID: string) =>
    http.delete<void>(`${BASE}/agents/${encodeURIComponent(agentID)}/identity`),

  // --- policies (ABAC + approval) --------------------------------------------
  listPolicies: (params?: ListParams & { kind?: string; enabled?: boolean }) =>
    http.get<ListResponse<PolicyDTO>>(`${BASE}/policies`, {
      query: { ...params },
    }),
  getPolicy: (id: string) =>
    http.get<PolicyDTO>(`${BASE}/policies/${encodeURIComponent(id)}`),
  createPolicy: (input: PolicyInput) =>
    http.post<PolicyDTO>(`${BASE}/policies`, input),
  updatePolicy: (id: string, input: PolicyInput) =>
    http.put<PolicyDTO>(`${BASE}/policies/${encodeURIComponent(id)}`, input),
  deletePolicy: (id: string) =>
    http.delete<void>(`${BASE}/policies/${encodeURIComponent(id)}`),

  // --- approval queue (HITL) -------------------------------------------------
  listApprovals: (params?: ListParams & { status?: string; action?: string }) =>
    http.get<ListResponse<ApprovalDTO>>(`${BASE}/approvals`, {
      query: { ...params },
    }),
  createApproval: (input: CreateApprovalRequest) =>
    http.post<ApprovalDTO>(`${BASE}/approvals`, input),
  getApproval: (id: string) =>
    http.get<ApprovalDTO>(`${BASE}/approvals/${encodeURIComponent(id)}`),
  listDecisions: (id: string) =>
    http.get<ListResponse<DecisionDTO>>(
      `${BASE}/approvals/${encodeURIComponent(id)}/decisions`,
    ),
  decide: (id: string, input: DecisionInput) =>
    http.post<ApprovalDTO>(
      `${BASE}/approvals/${encodeURIComponent(id)}/decisions`,
      input,
    ),
  cancelApproval: (id: string) =>
    http.post<ApprovalDTO>(
      `${BASE}/approvals/${encodeURIComponent(id)}/cancel`,
    ),
  sweepApprovals: () => http.post<SweepReport>(`${BASE}/approvals/sweep`),

  // --- break-glass emergency access ----------------------------------------
  listBreakGlass: (params?: ListParams & { status?: string }) =>
    http.get<ListResponse<BreakGlassDTO>>(`${BASE}/breakglass`, {
      query: { ...params },
    }),
  activateBreakGlass: (input: ActivateBreakGlassInput) =>
    http.post<BreakGlassDTO>(`${BASE}/breakglass`, input),
  getBreakGlass: (id: string) =>
    http.get<BreakGlassDTO>(`${BASE}/breakglass/${encodeURIComponent(id)}`),
  listBreakGlassUses: (id: string) =>
    http.get<ListResponse<BreakGlassUseDTO>>(
      `${BASE}/breakglass/${encodeURIComponent(id)}/uses`,
    ),
  revokeBreakGlass: (id: string) =>
    http.post<BreakGlassDTO>(
      `${BASE}/breakglass/${encodeURIComponent(id)}/revoke`,
    ),
  reviewBreakGlass: (id: string, input: ReviewBreakGlassInput) =>
    http.post<BreakGlassDTO>(
      `${BASE}/breakglass/${encodeURIComponent(id)}/review`,
      input,
    ),

  // --- per-agent risk profiles ---------------------------------------
  // Heuristic classification from observed signals, operator override, human
  // review. effective_tier (operator||suggested) is what enforcement reads.
  listAgentRiskProfiles: (
    params?: ListParams & { effective_tier?: string; state?: string },
  ) =>
    http.get<ListResponse<AgentRiskProfileDTO>>(`${BASE}/agent-risk-profiles`, {
      query: { ...params },
    }),
  getAgentRiskProfile: (id: string) =>
    http.get<AgentRiskProfileDTO>(
      `${BASE}/agent-risk-profiles/${encodeURIComponent(id)}`,
    ),
  classifyAgentRisk: (input: ClassifyAgentRiskInput) =>
    http.post<AgentRiskProfileDTO>(
      `${BASE}/agent-risk-profiles/classify`,
      input,
    ),
  setAgentRiskTier: (id: string, input: SetAgentRiskTierInput) =>
    http.put<AgentRiskProfileDTO>(
      `${BASE}/agent-risk-profiles/${encodeURIComponent(id)}/tier`,
      input,
    ),
  // Review takes NO request body — it marks state=reviewed and accepts the
  // current effective tier (agentrisk.go handleReviewAgentRisk).
  reviewAgentRisk: (id: string) =>
    http.post<AgentRiskProfileDTO>(
      `${BASE}/agent-risk-profiles/${encodeURIComponent(id)}/review`,
    ),

  // --- routine governance policies (· enforcement) -----------------
  // Six routes, read/write split: list, posture and get need
  // governance:routine:read; create, update and delete need
  // governance:routine:admin (governance.go:528-533). `/posture` is registered
  // BEFORE `/{id}`, so it is a sibling endpoint and never an id.
  listRoutinePolicies: (
    params?: ListParams & { scope_kind?: string; enabled?: boolean },
  ) =>
    http.get<ListResponse<RoutinePolicyDTO>>(`${BASE}/routine-policies`, {
      query: { ...params },
    }),
  // The composed posture answers for a SCOPE. Omitting both refs asks the
  // baseline question — what governs a routine with no owning user in the
  // tenant's default workspace — which is what a token-declared routine
  // actually resolves under.
  routinePosture: (params?: {
    workspace_ref?: string
    user_ref?: string
    /** 'false' reproduces the unanswerable user axis orchestration hits for a
     * routine whose owner it cannot recognise. The engine 400s if it is
     * combined with a user_ref — an unanswerable axis has no owner. */
    user_known?: string
  }) =>
    http.get<RoutinePostureDTO>(`${BASE}/routine-policies/posture`, {
      query: { ...params },
    }),
  getRoutinePolicy: (id: string) =>
    http.get<RoutinePolicyDTO>(
      `${BASE}/routine-policies/${encodeURIComponent(id)}`,
    ),
  createRoutinePolicy: (input: CreateRoutinePolicyInput) =>
    http.post<RoutinePolicyDTO>(`${BASE}/routine-policies`, input),
  updateRoutinePolicy: (id: string, input: UpdateRoutinePolicyInput) =>
    http.put<RoutinePolicyDTO>(
      `${BASE}/routine-policies/${encodeURIComponent(id)}`,
      input,
    ),
  deleteRoutinePolicy: (id: string) =>
    http.delete<void>(`${BASE}/routine-policies/${encodeURIComponent(id)}`),

  // --- AgentCore Cedar export (· console) --------------------------
  // Two routes, BOTH governance:agentcore-export:admin (governance.go:563-564):
  // planning reads remote policy metadata and apply mutates the remote engine,
  // so there is no read tier to gate a nav entry on.
  //
  // `plan` is a POST that WRITES NOTHING — it is the dry-run diff. It is modelled
  // as a mutation rather than a query on purpose: a useQuery would let react-query
  // refetch on focus/reconnect and swap the plan under an operator who is reading
  // it, which is exactly the "the plan changed beneath you" hazard the plan_hash
  // exists to catch. The plan the operator sees is held in explicit state, and
  // nothing but an explicit click replaces it.
  planAgentCoreExport: (mode?: AgentCoreEnforcementMode) =>
    http.post<AgentCoreExportPlan>(
      `${BASE}/agentcore-export/plan`,
      // Omit the field entirely for "keep the tenant's configured mode": the
      // engine overrides only on a non-blank string (agentcoreexport.go:225-227).
      mode ? { enforcement_mode: mode } : {},
    ),

  /**
   * Apply the plan the operator reviewed. `input.plan_hash` MUST be the hash of
   * the plan currently on screen — the engine re-plans server-side and 409s when
   * the hashes differ (agentcoreexport.go:177-188), which is the seam that makes
   * "apply a plan nobody reviewed" impossible rather than merely discouraged.
   *
   * `enforcement_mode` must be the one the DISPLAYED plan was computed with, not
   * whatever the selector reads now: the engine re-plans with the mode in THIS
   * body, so a different mode produces a different hash and a spurious 409.
   *
   * postWithMeta because the status IS part of the contract here — 202 means the
   * write has not happened yet.
   */
  applyAgentCoreExport: async (
    input: AgentCoreExportApplyInput,
  ): Promise<AgentCoreApplyOutcome> => {
    const { status, data } = await http.postWithMeta<
      AgentCoreExportApplyDTO | AgentCoreExportPendingDTO
    >(`${BASE}/agentcore-export/apply`, input)
    return agentCoreApplyOutcome(status, data)
  },
}

/**
 * Map (status, body) onto the three distinct 2xx outcomes. Mirrors the shape
 * console/api.ts uses for the sourcescope 200-vs-202 pair (bindingApplyResult).
 *
 * The `partial` arm is the one with no HTTP signal at all: a run where some
 * policy writes failed answers 200 with the failures inside `results[].error`
 * (agentcoreexport.go:204-208). `results` is `[]` from the DTO constructor but
 * is typed nullable because the field has no `omitempty` guarantee.
 */
export function agentCoreApplyOutcome(
  status: number,
  data: AgentCoreExportApplyDTO | AgentCoreExportPendingDTO,
): AgentCoreApplyOutcome {
  if (status === 202) {
    const pending = data as AgentCoreExportPendingDTO
    return {
      kind: 'pending',
      planHash: pending.plan_hash,
      approvalRef: pending.approval_ref,
      status: pending.status,
    }
  }
  const applied = data as AgentCoreExportApplyDTO
  const results = applied.results ?? []
  return {
    kind: results.some(agentCoreResultFailed) ? 'partial' : 'applied',
    planHash: applied.plan_hash,
    results,
  }
}

/**
 * Whether one policy write failed. `error` is NOT the only signal, and assuming
 * it was is the defect the contrast found (C2.1).
 *
 * The exporter copies AWS's own `Status` onto the result and sets `Err` only when
 * the CALL failed (exporter.go:427-437). A write that AWS accepted and then could
 * not complete comes back with `Status: CREATE_FAILED | UPDATE_FAILED |
 * DELETE_FAILED` and its reasons in `status_reasons`, with no `Err` at all — so
 * the engine's failure counter does not move, the route answers a clean 200, and
 * a console keyed only on `error` reports "Export applied" over a policy that
 * never reached the engine.
 *
 * The status vocabulary is the ratified contract's, not a guess:
 * The agentcore-export contract lists
 * `CREATING|ACTIVE|UPDATING|DELETING|CREATE_FAILED|UPDATE_FAILED|DELETE_FAILED`,
 * and :55 already treats `*_FAILED` as the drift-worthy state. `ERROR` is the
 * value the exporter itself writes when the call errored (exporter.go:435).
 */
export function agentCoreResultFailed(r: AgentCoreExportResultDTO): boolean {
  if (r.error) return true
  const status = (r.status ?? '').toUpperCase()
  return status === 'ERROR' || status.endsWith('_FAILED')
}

/**
 * 501 = this deployment has not wired the exporter. It is the engine's honest
 * frontier, not a failure and not a permission problem: without
 * OLIVARES_AGENTCORE_EXPORT_CONFIG the composition root builds no exporters and
 * governance answers 501 (agentcoreexportwiring.go:16-21).
 *
 * Detected by STATUS, never by matching the message — the same rule
 * capabilities/api.ts:isEnterprisePending follows. A message is prose: it gets
 * reworded and translated, and a console that greps it silently starts calling a
 * missing capability a crash.
 */
export function isAgentCoreExportNotWired(error: unknown): boolean {
  return error instanceof ApiError && error.status === 501
}

/**
 * 409 = the plan changed between review and apply ("plan changed; re-plan",
 * agentcoreexport.go:185-188). It must be reported as itself and MUST NOT
 * trigger an automatic re-plan: silently re-planning and applying is precisely
 * the "apply a plan the operator never saw" that the hash forbids.
 */
export function isAgentCoreExportPlanChanged(error: unknown): boolean {
  return error instanceof ApiError && error.status === 409
}

/** Tenant-scoped query keys (query.ts contract: tenant id in every key). */
export const governanceKeys = {
  /** El registro es del BINARIO, no del tenant: la clave no lleva tenant a proposito. */
  emergingStandards: () => ['governance', 'emerging-standards'] as const,
  all: (t: string | null) => ['governance', t] as const,
  identities: (t: string | null, params?: unknown) =>
    params === undefined
      ? (['governance', t, 'identities'] as const)
      : (['governance', t, 'identities', params] as const),
  groups: (t: string | null, params?: unknown) =>
    params === undefined
      ? (['governance', t, 'groups'] as const)
      : (['governance', t, 'groups', params] as const),
  groupMembers: (t: string | null, ref: string, params?: unknown) =>
    params === undefined
      ? (['governance', t, 'groupMembers', ref] as const)
      : (['governance', t, 'groupMembers', ref, params] as const),
  bindings: (t: string | null) => ['governance', t, 'bindings'] as const,
  policies: (t: string | null, params?: unknown) =>
    params === undefined
      ? (['governance', t, 'policies'] as const)
      : (['governance', t, 'policies', params] as const),
  policy: (t: string | null, id: string) =>
    ['governance', t, 'policy', id] as const,
  approvals: (t: string | null, params?: unknown) =>
    params === undefined
      ? (['governance', t, 'approvals'] as const)
      : (['governance', t, 'approvals', params] as const),
  approval: (t: string | null, id: string) =>
    ['governance', t, 'approval', id] as const,
  decisions: (t: string | null, id: string) =>
    ['governance', t, 'approval', id, 'decisions'] as const,
  breakGlass: (t: string | null) => ['governance', t, 'breakGlass'] as const,
  breakGlassList: (t: string | null, params?: unknown) =>
    params === undefined
      ? (['governance', t, 'breakGlass', 'list'] as const)
      : (['governance', t, 'breakGlass', 'list', params] as const),
  breakGlassDetail: (t: string | null, id: string) =>
    ['governance', t, 'breakGlass', 'detail', id] as const,
  breakGlassUses: (t: string | null, id: string) =>
    ['governance', t, 'breakGlass', 'detail', id, 'uses'] as const,
  agentRiskProfiles: (t: string | null, params?: unknown) =>
    params === undefined
      ? (['governance', t, 'agentRiskProfiles'] as const)
      : (['governance', t, 'agentRiskProfiles', params] as const),
  agentRiskProfile: (t: string | null, id: string) =>
    ['governance', t, 'agentRiskProfile', id] as const,
  routinePolicies: (t: string | null, params?: unknown) =>
    params === undefined
      ? (['governance', t, 'routinePolicies'] as const)
      : (['governance', t, 'routinePolicies', params] as const),
  routinePolicy: (t: string | null, id: string) =>
    ['governance', t, 'routinePolicy', id] as const,
  // The posture key carries the resolution scope: two scopes are two different
  // answers and must not share a cache entry.
  routinePosture: (t: string | null, params?: unknown) =>
    params === undefined
      ? (['governance', t, 'routinePosture'] as const)
      : (['governance', t, 'routinePosture', params] as const),
}
