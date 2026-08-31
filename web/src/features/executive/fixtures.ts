// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Deterministic fixtures for the executive dashboard (module XXI). They reuse the
// SOURCE modules' own fixtures wherever those exist (so the rollups are tested against
// the same shapes the technical views render — no drift), and synthesize the few
// Inputs that ship their demo data via the visual-e2e set (health, inventory,
// sessions, access-map). Shared by the component tests and the visual-e2e route mocks.
import { governedModelsFixture } from '@/features/models/fixtures'
import { findingsFixture } from '@/features/security/fixtures'
import { runsFixture } from '@/features/redteam/fixtures'
import {
  riskFixture,
  summaryFixture as complianceSummaryFixture,
} from '@/features/compliance/fixtures'
import type { SpendResponse } from '@/features/finops/types'
import type { AccessEdge, DiffResponse } from '@/features/access-map/types'
import type { IncidentDTO, StatusDTO } from '@/features/health/types'
import type { InventorySummary } from '@/features/inventory/types'
import type { LiveDTO } from '@/features/sessions/types'
import type { ListResponse } from '@/lib/api/types'

function list<T>(items: T[]): ListResponse<T> {
  return { items, has_more: false }
}

// Re-export the directly reused source fixtures under stable names.
export {
  summaryFixture as finopsSummaryFixture,
  trendFixture as finopsTrendFixture,
  forecastFixture as finopsForecastFixture,
} from '@/features/finops/fixtures'

export const modelsFixture = list(governedModelsFixture)
export const securityFindingsFixture = list(findingsFixture)
export const redteamRunsFixture = list(runsFixture)
export const complianceSummary = complianceSummaryFixture
export const complianceRiskFixture = list(riskFixture)

// ---- access-map drift — firm + pending + partial-coverage edges --------
const driftEdge = (o: Partial<AccessEdge>): AccessEdge => ({
  id: 'e0',
  origin_kind: 'agent',
  origin_id: 'A1',
  origin_ref: 'orchestrator',
  resource_id: 'R2',
  resource_kind: 'postgres.table',
  resource_ref: 'appdb.public.payments',
  mode: 'readwrite',
  signal_source: 'pg_audit',
  signal_sources: 'otel,pg_audit',
  confidence: 'attributed',
  bridged: true,
  coverage_tier: 'clean',
  observed: true,
  permitted: false,
  occurrence_count: 7,
  first_seen: '2026-06-03T09:00:00Z',
  last_seen: '2026-06-04T07:30:00Z',
  ...o,
})

export const accessDriftFixture: DiffResponse = {
  unexpected_accesses: [
    {
      kind: 'unexpected_access',
      reconciliation_pending: false,
      edge: driftEdge({ id: 'e2' }),
    },
    {
      kind: 'unexpected_access',
      reconciliation_pending: true,
      edge: driftEdge({
        id: 'e6',
        resource_kind: 'http.api',
        resource_ref: 'api.stripe.com',
        signal_source: 'ebpf',
        signal_sources: 'ebpf',
        confidence: 'approximate',
        coverage_tier: 'opaque',
      }),
    },
  ],
  unused_grants: [
    {
      kind: 'unused_grant',
      edge: driftEdge({
        id: 'g1',
        mode: 'read',
        observed: false,
        permitted: true,
      }),
    },
  ],
  unexpected_count: 2,
  unused_count: 1,
}

// ---- health ------------------------------------------------------------
export const healthStatusFixture: ListResponse<StatusDTO> = list([
  {
    id: 'h1',
    name: 'orchestrator',
    subject_kind: 'agent',
    subject_ref: 'orchestrator',
    state: 'healthy',
    desired_status: 'active',
    expected_interval_seconds: 300,
    grace_factor: 2,
    sla_target_ppm: 999000,
    sla_breach_open: false,
    last_checked_at: '2026-06-04T07:30:00Z',
    last_seen_at: '2026-06-04T07:30:00Z',
    last_latency_ms: 42,
    last_detail_hash: 'sha256:ab12cd',
    created_at: '2026-06-01T08:00:00Z',
  },
  {
    id: 'h2',
    name: 'github mcp',
    subject_kind: 'mcp',
    subject_ref: 'github',
    state: 'degraded',
    desired_status: 'active',
    expected_interval_seconds: 300,
    grace_factor: 2,
    sla_target_ppm: 999000,
    sla_breach_open: false,
    last_checked_at: '2026-06-04T07:28:00Z',
    last_seen_at: '2026-06-04T07:28:00Z',
    last_latency_ms: 880,
    last_detail_hash: 'sha256:ef34gh',
    created_at: '2026-06-01T08:00:00Z',
  },
  {
    id: 'h3',
    name: 'nightly-reporter',
    subject_kind: 'agent',
    subject_ref: 'nightly-reporter',
    state: 'down',
    desired_status: 'active',
    expected_interval_seconds: 300,
    grace_factor: 2,
    sla_target_ppm: 999000,
    sla_breach_open: true,
    last_checked_at: '2026-06-04T02:10:00Z',
    last_seen_at: '2026-06-04T02:10:00Z',
    last_latency_ms: -1,
    last_detail_hash: '',
    created_at: '2026-05-20T02:00:00Z',
  },
])

export const healthIncidentsFixture: ListResponse<IncidentDTO> = list([
  {
    id: 'i1',
    subject_kind: 'agent',
    subject_ref: 'nightly-reporter',
    check_ref: 'h3',
    kind: 'down',
    severity: 'high',
    state: 'open',
    opened_at: '2026-06-04T02:15:00Z',
    summary: 'agent nightly-reporter is DOWN',
  },
  {
    id: 'i2',
    subject_kind: 'mcp',
    subject_ref: 'github',
    kind: 'degraded',
    severity: 'medium',
    state: 'resolved',
    opened_at: '2026-06-03T18:00:00Z',
    resolved_at: '2026-06-03T19:30:00Z',
    summary: 'github mcp degraded',
  },
])

// ---- inventory ---------------------------------------------------------
export const inventorySummaryFixture: InventorySummary = {
  by_kind: {
    agent: { active: 3, stale: 1, total: 4 },
    session: { active: 5, stale: 2, total: 7 },
    mcp_server: { active: 2, stale: 0, total: 2 },
    model: { active: 3, stale: 0, total: 3 },
    identity: { active: 2, stale: 0, total: 2 },
    resource: { active: 6, stale: 1, total: 7 },
  },
  by_source: { otel: 18, pg_audit: 6, ebpf: 3, cloudtrail: 2 },
  total: 25,
}

// ---- sessions ----------------------------------------------------------
export const sessionsLiveFixture: ListResponse<LiveDTO> = list([
  {
    session_ref: 'sess-9f2a',
    agent_ref: 'orchestrator',
    cc_state: 'active',
    current_action: 'create_pr',
    current_resource: 'github/create_pr',
    current_mode: 'readwrite',
    model_ref: 'claude-opus-4-8',
    input_tokens: 18420,
    output_tokens: 5310,
    cost_micro_usd: 184200,
    event_count: 64,
    tool_call_count: 22,
    first_event_at: '2026-06-04T07:00:00Z',
    last_event_at: '2026-06-04T07:31:00Z',
    duration_seconds: 1860,
    goal: 'Open a PR fixing the payments rounding bug',
    summary: '',
  },
  {
    session_ref: 'sess-7c10',
    agent_ref: 'ingest-worker',
    cc_state: 'idle',
    current_action: 'INSERT',
    current_resource: 'appdb.public.customers',
    current_mode: 'readwrite',
    model_ref: 'claude-sonnet-4-6',
    input_tokens: 8200,
    output_tokens: 1200,
    cost_micro_usd: 41000,
    event_count: 30,
    tool_call_count: 12,
    first_event_at: '2026-06-04T06:30:00Z',
    last_event_at: '2026-06-04T07:05:00Z',
    duration_seconds: 2100,
    goal: '',
    summary: '',
  },
  {
    session_ref: 'sess-3b88',
    agent_ref: 'nightly-reporter',
    cc_state: 'silent_evasion',
    current_action: 'web.search',
    current_mode: 'read',
    model_ref: 'claude-haiku-4-5',
    input_tokens: 1200,
    output_tokens: 300,
    cost_micro_usd: 1500,
    event_count: 6,
    tool_call_count: 2,
    first_event_at: '2026-06-04T02:00:00Z',
    last_event_at: '2026-06-04T02:03:00Z',
    duration_seconds: 180,
    goal: '',
    summary: '',
  },
])

// ---- spend by dimension (org/team/project summaries) -------------------------
const spend = (
  dimension: SpendResponse['dimension'],
  buckets: [string, number][],
): SpendResponse => ({
  dimension,
  since: '2026-05-05T00:00:00Z',
  until: '2026-06-04T00:00:00Z',
  total_micro_usd: buckets.reduce((s, [, v]) => s + v, 0),
  buckets: buckets.map(([key, cost_micro_usd]) => ({
    key,
    cost_micro_usd,
    input_tokens: Math.round(cost_micro_usd / 220),
    output_tokens: Math.round(cost_micro_usd / 540),
    samples: Math.round(cost_micro_usd / 3_600_000),
  })),
  truncated: false,
})

export const spendByDimensionFixtures: Record<string, SpendResponse> = {
  team: spend('team', [
    ['platform', 21_400_000_000],
    ['support', 14_800_000_000],
    ['growth', 8_300_000_000],
    ['research', 3_780_000_000],
  ]),
  project: spend('project', [
    ['payments-copilot', 18_900_000_000],
    ['support-triage', 13_200_000_000],
    ['data-pipeline', 9_100_000_000],
    ['internal-tools', 7_080_000_000],
  ]),
  agent: spend('agent', [
    ['support-triage', 20_600_000_000],
    ['orchestrator', 16_400_000_000],
    ['ingest-worker', 7_500_000_000],
    ['nightly-reporter', 3_780_000_000],
  ]),
  model: spend('model', [
    ['claude-opus-4-8', 32_050_000_000],
    ['gemini-1.5-pro', 8_200_000_000],
    ['claude-sonnet-4-6', 5_900_000_000],
    ['claude-haiku-4-5', 2_130_000_000],
  ]),
  provider: spend('provider', [
    ['anthropic', 36_150_000_000],
    ['google', 10_270_000_000],
    ['mistral', 1_860_000_000],
  ]),
}
