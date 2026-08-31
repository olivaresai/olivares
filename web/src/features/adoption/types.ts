// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Adoption (gap #12) DTOs — hand-authored to match modules/claudeadoption/dto.go 1:1.
// Counts are plain integers (an internal design note (not shipped)); active time is integer
// milliseconds; an acceptance rate is a 0..1 fraction or null (no decisions → honest
// null, never a fabricated 0%). The two LENSES — `analytics` (admin per-developer feed)
// and `telemetry` (OTLP per-session plane) — describe the SAME activity and are never
// summed; the UI presents them side by side.

export interface BoundaryNote {
  claude_api_only: boolean
  excludes: string[]
}

export interface AdoptionTotals {
  sessions: number
  lines_added: number
  lines_removed: number
  lines_net: number
  commits: number
  pull_requests: number
  active_time_ms: number
  tools_accepted: number
  tools_rejected: number
  acceptance_rate: number | null
  input_tokens: number
  output_tokens: number
  tokens: number
}

export interface ModelMix {
  model: string
  tokens: number
}

export interface ToolBreakdown {
  tool: string
  accepted: number
  rejected: number
  acceptance_rate: number | null
}

export interface Lens {
  totals: AdoptionTotals
  by_model: ModelMix[]
  by_tool: ToolBreakdown[]
}

export type LensId = 'analytics' | 'telemetry'

export interface SummaryResponse {
  since?: string
  until?: string
  analytics: Lens
  telemetry: Lens
  developers: number
  teams: number
  boundary: BoundaryNote
  truncated?: boolean
}

export interface TrendDay {
  day: string
  totals: AdoptionTotals
}

export interface TrendResponse {
  lens: string
  days: TrendDay[]
  boundary: BoundaryNote
  truncated?: boolean
}

export interface TeamRow {
  team: string
  totals: AdoptionTotals
}

export interface TeamsResponse {
  teams: TeamRow[]
  boundary: BoundaryNote
  truncated?: boolean
}

export interface DeveloperRow {
  developer: string
  totals: AdoptionTotals
}

export interface DevelopersResponse {
  developers: DeveloperRow[]
  boundary: BoundaryNote
  truncated?: boolean
}

export type DiscrepancyDirection =
  | 'aligned'
  | 'telemetry_exceeds_official'
  | 'official_exceeds_telemetry'
  | 'official_plane_absent'
  | 'telemetry_plane_absent'

export interface DiscrepancyMetric {
  name: string
  analytics: number
  telemetry: number
  ratio: number
  direction: DiscrepancyDirection
  material: boolean
}

export interface DiscrepancyDay {
  day: string
  metrics: DiscrepancyMetric[]
  material: boolean
}

export interface DiscrepancyThresholds {
  ratio: number
  floors: Record<string, number>
}

export interface DiscrepancyResponse {
  since?: string
  until?: string
  days: DiscrepancyDay[]
  thresholds: DiscrepancyThresholds
  boundary: BoundaryNote
  truncated?: boolean
}
