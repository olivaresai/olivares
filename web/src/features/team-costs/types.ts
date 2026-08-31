// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Types for team cost attribution. Shapes mirror the backend DTOs from
// GET /v1/m/finops/analytics/team-summary (modules/finops/analytics.go). The
// engine speaks integer micro-USD; these types preserve that — no floats.

/** One project's spend within a team during the requested period. */
export interface ProjectSummaryDTO {
  project: string
  sessions: number
  input_tokens: number
  output_tokens: number
  cost_micro_usd: number
}

/** One model's spend within a team during the requested period. */
export interface ModelSummaryDTO {
  model: string
  cost_micro_usd: number
}

/**
 * Team-level cost aggregation with per-project and per-model breakdowns and a
 * zero-filled per-calendar-day trend series (micro-USD per day).
 * Teams with no `team` label on their cost samples appear as "(untagged)".
 */
export interface TeamSummaryDTO {
  /** Team name from the `team` label, or `"(untagged)"` when absent. */
  team: string
  /** Distinct session IDs that contributed to this team's spend. */
  sessions: number
  input_tokens: number
  output_tokens: number
  /** Total estimated cost in integer micro-USD (millionths of a dollar). */
  cost_micro_usd: number
  /** Zero-filled daily cost in micro-USD, one entry per calendar day in the period. */
  trend: number[]
  projects: ProjectSummaryDTO[]
  models: ModelSummaryDTO[]
}

/**
 * Response from GET /v1/m/finops/analytics/team-summary.
 * `period` is `YYYY-MM-DD/YYYY-MM-DD` (inclusive start / exclusive end).
 */
export interface TeamSummaryResponse {
  period: string
  teams: TeamSummaryDTO[]
}

/** Accepted period query values. The backend defaults to `30d` when omitted. */
export type SummaryPeriod = '7d' | '30d' | '90d'
