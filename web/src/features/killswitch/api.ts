// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Endpoint helpers + query keys for the estate kill switch (governance module
// routes). Thin wrappers over the core HTTP client against /v1/m/governance
// (ARCHITECTURE.md — no logic here). The active tenant header is attached automatically;
// tenant-scoped keys cache-isolate per tenant (query.ts contract).
//
// Status-shape note: POST /killswitch/{id}/reenable answers 202 with the
// pending_approval envelope while the dual-control approval collects decisions and
// 200 with the flipped stop once two distinct humans approved — both are 2xx, so
// the client discriminates on the body shape (isReenablePending), never the code.
import { http } from '@/lib/api'
import type { ListResponse } from '@/lib/api/types'
import type {
  CreateGuardianRuleRequest,
  EngageKillSwitchRequest,
  EvidencePack,
  GuardianActionDTO,
  GuardianRuleDTO,
  KillSwitchDTO,
  KillSwitchStateDTO,
  ReenableKillSwitchRequest,
  ReenableResponse,
  ReviewKillSwitchRequest,
  UpdateGuardianRuleRequest,
} from './types'

const BASE = '/v1/m/governance'

export interface ListParams {
  cursor?: string
  limit?: number
}

export const killswitchApi = {
  // --- stop rows + live posture ----------------------------------------------
  list: (params?: ListParams & { status?: string }) =>
    http.get<ListResponse<KillSwitchDTO>>(`${BASE}/killswitch`, {
      query: { ...params },
    }),
  state: () => http.get<KillSwitchStateDTO>(`${BASE}/killswitch/state`),
  get: (id: string) =>
    http.get<KillSwitchDTO>(`${BASE}/killswitch/${encodeURIComponent(id)}`),

  // --- the lifecycle: engage → dual-control re-enable → forced post-review ----
  engage: (input: EngageKillSwitchRequest) =>
    http.post<KillSwitchDTO>(`${BASE}/killswitch`, input),
  reenable: (id: string, input: ReenableKillSwitchRequest) =>
    http.post<ReenableResponse>(
      `${BASE}/killswitch/${encodeURIComponent(id)}/reenable`,
      input,
    ),
  review: (id: string, input: ReviewKillSwitchRequest) =>
    http.post<KillSwitchDTO>(
      `${BASE}/killswitch/${encodeURIComponent(id)}/review`,
      input,
    ),
  evidence: (id: string) =>
    http.get<EvidencePack>(
      `${BASE}/killswitch/${encodeURIComponent(id)}/evidence`,
    ),

  // --- guardian rules + containment trail -------------------------------------
  listGuardianRules: (params?: ListParams) =>
    http.get<ListResponse<GuardianRuleDTO>>(`${BASE}/guardian/rules`, {
      query: { ...params },
    }),
  createGuardianRule: (input: CreateGuardianRuleRequest) =>
    http.post<GuardianRuleDTO>(`${BASE}/guardian/rules`, input),
  updateGuardianRule: (id: string, input: UpdateGuardianRuleRequest) =>
    http.put<GuardianRuleDTO>(
      `${BASE}/guardian/rules/${encodeURIComponent(id)}`,
      input,
    ),
  deleteGuardianRule: (id: string) =>
    http.delete<void>(`${BASE}/guardian/rules/${encodeURIComponent(id)}`),
  listGuardianActions: (params?: ListParams & { status?: string }) =>
    http.get<ListResponse<GuardianActionDTO>>(`${BASE}/guardian/actions`, {
      query: { ...params },
    }),
}

/** Tenant-scoped query keys (query.ts contract: tenant id FIRST). */
export const killswitchKeys = {
  all: (t: string | null) => ['killswitch', t] as const,
  state: (t: string | null) => ['killswitch', t, 'state'] as const,
  stops: (t: string | null, params?: unknown) =>
    params === undefined
      ? (['killswitch', t, 'stops'] as const)
      : (['killswitch', t, 'stops', params] as const),
  stop: (t: string | null, id: string) =>
    ['killswitch', t, 'stop', id] as const,
  guardianRules: (t: string | null) =>
    ['killswitch', t, 'guardianRules'] as const,
  guardianActions: (t: string | null, params?: unknown) =>
    params === undefined
      ? (['killswitch', t, 'guardianActions'] as const)
      : (['killswitch', t, 'guardianActions', params] as const),
}
