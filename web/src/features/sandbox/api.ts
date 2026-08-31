// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Sandbox (module XVII) endpoint wrappers + query keys. Thin `http.*` calls against
// the engine's `/v1/m/sandbox/…` routes (the web presents, never
// re-executes). Tenant-scoped keys include the active tenant so switching org
// refetches cleanly. Reads are RBAC-gated server-side; the UI mirrors that
// to hide write/admin actions. Launching a run/compare is privileged + audited.
//
// The SSE route `/runs/{id}/stream` is NOT a wrapper here: it cannot go through this
// client (apiFetchWithMeta consumes the whole body with `res.text()`,
// lib/api/client.ts:157 — a stream would never resolve). It is consumed by the
// `useRunStream` hook (stream.ts) over the SHARED SSE transport
// (features/shared/sse.ts), which injects the same bearer + tenant headers; the path
// is built by `runStreamPath` below. No feature-local `fetch` exists.
//
// WHAT THAT STREAM IS, exactly — because the name invites the wrong reading:
//
//  - It REPLAYS a persisted run: one `event: output` per stored step (in step-key
//    order), then `event: summary` (the aggregate), then `event: done`, and closes
//    (modules/sandbox/stream.go:38-43). There is no live broker.
//  - A sandbox run executes SYNCHRONOUSLY inside the launch handler and its row is
//    written already-terminal (modules/sandbox/runs.go:110-115 and :185), so no run is
//    ever observable mid-flight. That is a DECIDED design, not a gap: the contract
//    discarded async+broker explicitly (the evals-sandbox contract).
//    ⇒ the honest label for this surface is a replay of committed evidence arriving
//    progressively, never "an execution in progress".
//  - It reads NO resume cursor: `handleStream` never touches `r.URL.Query()` nor
//    `Last-Event-ID` (stream.go:44-122). A reconnect therefore restarts from the FIRST
//    output — which is loss-free, not lossy, because the whole set is replayed; the
//    hook drops what it already holds by row `id`. Cost of a reconnect is the full
//    replay, bounded by the run's step count.
//  - Opening it is a privileged read and is AUDITED server-side
//    (stream.go:134-148, action `sandbox.run.stream`) ⇒ one ledger row per open. That
//    is why the hook does NOT reconnect by itself: a background retry loop would write
//    audit rows no principal chose.
import { http } from '@/lib/api/client'
import type { ListResponse } from '@/lib/api/types'
import type {
  Comparison,
  CompareInput,
  CreateScenarioInput,
  Output,
  ReplayInput,
  Run,
  RunScenarioInput,
  Scenario,
} from './types'

const BASE = '/v1/m/sandbox'
const LIST_CEILING = 1000

export const sandboxApi = {
  // --- scenarios (sandbox:scenario:read) -------------------------------------
  scenarios: () =>
    http.get<ListResponse<Scenario>>(`${BASE}/scenarios`, {
      query: { limit: LIST_CEILING },
    }),
  scenario: (id: string) => http.get<Scenario>(`${BASE}/scenarios/${id}`),

  // --- runs (sandbox:run:read) -----------------------------------------------
  runs: () =>
    http.get<ListResponse<Run>>(`${BASE}/runs`, {
      query: { limit: LIST_CEILING },
    }),
  run: (id: string) => http.get<Run>(`${BASE}/runs/${id}`),
  outputs: (id: string) =>
    http.get<ListResponse<Output>>(`${BASE}/runs/${id}/outputs`),

  // --- comparisons (sandbox:run:read) ----------------------------------------
  comparisons: () =>
    http.get<ListResponse<Comparison>>(`${BASE}/comparisons`, {
      query: { limit: LIST_CEILING },
    }),
  comparison: (id: string) => http.get<Comparison>(`${BASE}/comparisons/${id}`),

  // --- writes ----------------------------------------------------------------
  // Authoring a scenario is write-tier; ARCHIVING one is admin-tier (a fixture other
  // runs were compared against stops being offered), and both self-audit server-side.
  // The engine has served them since the module was written — the console had never
  // called either, so an operator could run and compare scenarios but not create one.
  createScenario: (body: CreateScenarioInput) =>
    http.post<Scenario>(`${BASE}/scenarios`, body), // sandbox:scenario:write
  archiveScenario: (id: string) =>
    http.post<Scenario>(`${BASE}/scenarios/${id}/archive`, {}), // sandbox:scenario:admin
  runScenario: (id: string, body?: RunScenarioInput) =>
    http.post<Run>(`${BASE}/scenarios/${id}/run`, body ?? {}), // sandbox:run:write
  replay: (body: ReplayInput) => http.post<Run>(`${BASE}/replay`, body), // sandbox:run:write
  compare: (body: CompareInput) =>
    http.post<Comparison>(`${BASE}/compare`, body), // sandbox:run:admin
}

/** The SSE replay path for one run (consumed by `useRunStream`, stream.ts). Same
 *  permission as every other run read: `sandbox:run:read`
 *  (modules/sandbox/sandbox.go:177). */
export function runStreamPath(id: string): string {
  return `${BASE}/runs/${encodeURIComponent(id)}/stream`
}

export const sandboxKeys = {
  all: (tenant: string | null) => ['sandbox', tenant] as const,
  scenarios: (tenant: string | null) =>
    ['sandbox', tenant, 'scenarios'] as const,
  scenario: (tenant: string | null, id: string) =>
    ['sandbox', tenant, 'scenarios', id] as const,
  runs: (tenant: string | null) => ['sandbox', tenant, 'runs'] as const,
  run: (tenant: string | null, id: string) =>
    ['sandbox', tenant, 'runs', id] as const,
  outputs: (tenant: string | null, id: string) =>
    ['sandbox', tenant, 'runs', id, 'outputs'] as const,
  comparisons: (tenant: string | null) =>
    ['sandbox', tenant, 'comparisons'] as const,
  comparison: (tenant: string | null, id: string) =>
    ['sandbox', tenant, 'comparisons', id] as const,
}
