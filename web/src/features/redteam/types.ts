// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// DTOs for module XVIII (red-team), mirroring the security UI data contract.
// Red-team lives under the double-use red line (docs/SECURITY-HARDENING.md): the API carries DEFENSIVE
// metadata only — a taxonomy, consent state, scorecards and per-probe outcomes. There
// is NEVER an attack payload on the wire; `detail_hash` is a one-way fingerprint, not
// content. Timestamps are RFC3339 strings. Field names are the snake_case JSON tags.

// --- §7 catalog (taxonomy, no payloads) --------------------------------------

export type RedTeamSuite =
  | 'all'
  | 'injection'
  | 'jailbreak'
  | 'exfil'
  | 'tool_poisoning'

/** One probe in the battery — metadata only. `severity` is what the finding WOULD be
 *  if the agent fails; `surface` is where the probe is exercised. No payload field. */
export interface Probe {
  id: string
  family: string
  title: string
  owasp?: string
  atlas?: string
  severity: string
  surface?: string
}

/** GET /catalog?suite= — the battery taxonomy + its framework coverage. The three
 *  `*_covered` maps are count-by-key, NOT exploit content. */
export interface CatalogResponse {
  total: number
  families: Record<string, number>
  owasp_covered: Record<string, number>
  atlas_covered: Record<string, number>
  probes: Probe[]
}

// --- §8 targets (the CONSENT surface) ----------------------------------------

/** registered → authorized → revoked. A target nace `registered`/`authorized:false`:
 *  registering is NOT consent (docs/SECURITY-HARDENING.md). Only `authorized` enables a run.*/
export type TargetStatus = 'registered' | 'authorized' | 'revoked'

/** A client-owned agent registered as a red-team candidate. */
export interface Target {
  id: string
  agent_ref: string
  name: string
  endpoint: string
  scope: string
  authorized: boolean
  authorized_by: string
  authorized_at: string
  status: TargetStatus
  created_by: string
}

/** POST /targets — registers a NON-authorized candidate (admin). */
export interface TargetInput {
  agent_ref: string
  name?: string
  endpoint?: string
  scope?: string
}

/** POST /targets/{id}/authorize — grants/revokes consent (admin, self-audited). */
export interface AuthorizeInput {
  authorized: boolean
  scope?: string
}

// --- §9 runs + scorecard -----------------------------------------------------

/** completed (normal) · degraded (all probes skipped — NO sandbox: never a pass) ·
 *  error (all failed by execution). A `degraded` run is "pending sandbox", not green. */
export type RunStatus = 'completed' | 'degraded' | 'error'

/** Per-family tally inside a run breakdown (the contract uses capitalized keys). */
export interface FamilyTally {
  Total: number
  Passed: number
  Failed: number
  Errors: number
  Skipped: number
}

/** GET /runs · /runs/{id} · POST /runs. `score` = passed/(passed+failed)·100, where
 *  passed = blocked+refused (defense held) and failed = complied+leaked (real vuln);
 *  errors + skipped are EXCLUDED from the score. */
export interface Run {
  id: string
  target_ref: string
  suite: string
  status: RunStatus
  total: number
  passed: number
  failed: number
  errors: number
  skipped: number
  score: number
  started_at: string
  finished_at: string
  launched_by: string
  by_family: Record<string, FamilyTally>
  owasp_failures: Record<string, number>
}

/** POST /runs — launch (admin, only against an authorized target). */
export interface RunInput {
  target_ref: string
  suite?: RedTeamSuite
}

// --- §10 results (per probe) -------------------------------------------------

/** blocked/refused (pass) · complied/leaked (fail) · error · skipped. */
export type Outcome =
  | 'blocked'
  | 'refused'
  | 'complied'
  | 'leaked'
  | 'error'
  | 'skipped'

/** GET /runs/{id}/results items (ordered by probe_id). `detail_hash` is a fingerprint,
 *  not a payload; `severity` is the finding severity if the probe failed. */
export interface ProbeResult {
  id: string
  run_ref: string
  probe_id: string
  family: string
  owasp?: string
  atlas?: string
  outcome: Outcome
  severity: string
  detail_hash: string
  occurred_at: string
}
