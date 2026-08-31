// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Agent-artifact registry and dedicated agent AIBOM endpoint wrappers. This family
// rides the models module base path and models:registry:* permissions.
import { http } from '@/lib/api/client'
import type { ListResponse } from '@/lib/api/types'
import type {
  AgentArtifact,
  AgentArtifactClass,
  AgentArtifactInput,
  AibomSeal,
  AibomSealReceipt,
} from './types'

// Single-quoted literal, NOT `${BASE}/agent-artifacts` off a separate BASE constant.
// Same reason as web/src/features/work/api.ts:44-52, and this file is the second
// measured instance of it: the route census in cmd/olivares/consoleroutes_test.go
// resolves a template literal, a single-quoted literal or a path constant, but a
// constant whose VALUE is a template built from ANOTHER constant is one level too
// indirect — the constant itself AND every call site derived from it become
// "unresolvable": counted, never checked against a registered route. Measured
// 2026-08-11: this one constant contributed 6 of the 34 sites that held the ratchet
// over its budget of 32 and kept control-plane red on main for every lane. The BASE
// constant it used to interpolate had no other reader here and is gone with it; if a
// second models-module path appears, spell it out the same way rather than deriving it.
const ARTIFACTS = '/v1/m/models/agent-artifacts'

/** El techo REAL del repositorio genérico. Se pide entero en el registro y en el ledger de
 *  precintos por la misma razón: sin `limit` el motor pagina a 100 y publica un `has_more` que
 *  esta pantalla tiraba, así que una lista recortada se leía como completa. Pedir mil no elimina
 *  el recorte — lo vuelve DECLARABLE. */
export const ARTIFACT_PAGE = 1000

export const agentArtifactsApi = {
  artifacts: (query?: {
    artifact_class?: AgentArtifactClass
    limit?: number
  }) => http.get<ListResponse<AgentArtifact>>(ARTIFACTS, { query }),
  createArtifact: (body: AgentArtifactInput) =>
    http.post<AgentArtifact>(ARTIFACTS, body),
  deleteArtifact: (id: string) => http.delete<void>(`${ARTIFACTS}/${id}`),
  liveAibom: () => http.get<unknown>(`${ARTIFACTS}/aibom`),
  // No body: the server re-generates the BOM, commits its canonical hash and
  // returns the only copy of that sealed document in the receipt.
  sealAibom: () => http.post<AibomSealReceipt>(`${ARTIFACTS}/aibom`),
  /** ⛔ APPEND-ONLY: cada precinto es una fila nueva, así que este ledger crece y sin `limit`
   *  la consola enseñaba los CIEN PRIMEROS por `id ASC` — y lo que se pierde por ese lado es
   *  lo más RECIENTE, que en un ledger de evidencia es lo que se viene a ver. */
  aibomSeals: (query?: { limit?: number }) =>
    http.get<ListResponse<AibomSeal>>(`${ARTIFACTS}/aiboms`, { query }),
}

export const agentArtifactsKeys = {
  all: (tenant: string | null) =>
    ['models', tenant, 'agent-artifacts'] as const,
  artifacts: (
    tenant: string | null,
    artifactClass?: AgentArtifactClass,
    params?: unknown,
  ) =>
    params === undefined
      ? ([
          'models',
          tenant,
          'agent-artifacts',
          'registry',
          artifactClass ?? '*',
        ] as const)
      : ([
          'models',
          tenant,
          'agent-artifacts',
          'registry',
          artifactClass ?? '*',
          params,
        ] as const),
  liveAibom: (tenant: string | null) =>
    ['models', tenant, 'agent-artifacts', 'aibom', 'live'] as const,
  aibomSeals: (tenant: string | null, params?: unknown) =>
    params === undefined
      ? (['models', tenant, 'agent-artifacts', 'aiboms'] as const)
      : (['models', tenant, 'agent-artifacts', 'aiboms', params] as const),
}
