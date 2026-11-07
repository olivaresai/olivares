// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// PAGES DO NOT EXPLAIN THE ENGINE. These sentences were written for the engine's authors:
// environment variables, permission names, protocol names, "N reported no cost", "Showing
// first N". Each one was rewritten as one product sentence or deleted with its control; the
// docs keep the engineering detail. A key listed here may not carry its old English text again.
import { describe, expect, it } from 'vitest'

const catalogs = import.meta.glob<Record<string, unknown>>(
  '@/features/*/i18n/en.json',
  { eager: true, import: 'default' },
)

/** `<feature dir>:<key path>` -> the retired English text. */
const RETIRED: [id: string, text: string][] = [
  [
    'capabilities:wiring.notAccessMap',
    'This is the capability-connection graph, not the R/RW resource access map (see Access map).',
  ],
  [
    'capabilities:wiring.partial',
    'Showing a partial graph — refine the filters to see specific connections.',
  ],
  [
    'logs:capture.debugUnavailable',
    "DEBUG entries are not captured at the engine's current log level — set OLIVARES_LOG_LEVEL=debug.",
  ],
  [
    'observability:description',
    'Standards-based ingestion health and ledger-correlated trace drill-down. Figures are engine-wide (process-global), not per-tenant; standards are pinned to the versions and maturities the upstream bodies declare.',
  ],
  [
    'observability:traces.emptyHint',
    'No audit-ledger event in the walk window (last 20,000 events) carries a W3C trace id — callers are not propagating traceparent, or the window has rolled past them.',
  ],
  [
    'governance:agentcoreExport.notWiredBody',
    'No AgentCore exporter is configured for this tenant, so the engine declines the request. An operator enables it on the host by pointing this variable at the export configuration file:',
  ],
  ['governance:approvals.runSweep', 'Expire and escalate overdue requests'],
  [
    'adoption:boundary.note',
    'Covers only Claude API traffic — the admin Analytics feed and the OTLP telemetry plane. Usage served by {{providers}} is not included, so absence of adoption here is not proof of absence.',
  ],
  ['adoption:lenses.analytics.title', 'Per developer (admin Analytics)'],
  [
    'adoption:lenses.analytics.description_other',
    'Authoritative per-developer/day totals from the admin Analytics feed across {{count}} developers.',
  ],
  ['adoption:lenses.telemetry.title', 'Per session (live telemetry)'],
  [
    'adoption:lenses.telemetry.description_other',
    'Real-time per-session activity from the OTLP plane across {{count}} teams; includes active time.',
  ],
  [
    'claude-policy:drift.subtitle',
    'PERMITTED policy vs OBSERVED host configuration, as reported by the read-only connectors.',
  ],
  ['attestation:disclaimer.tag', 'Beta'],
  [
    'attestation:disclaimer.body',
    'The project is beta: no releases exist yet and the release pipeline is written but not activated. The running-binary panel is MEASURED from this process; the contract tabs are the declared contract, not a published release or a live cryptographic verification — this console never re-verifies signatures in the browser.',
  ],
  [
    'attestation:live.description',
    'What THIS engine process can prove about itself — read live from the binary (ldflags, debug.ReadBuildInfo, crypto/fips140, a self-hash of the executable) — and the honest release state: nothing is published or verified until a v* tag fires the pipeline.',
  ],
  [
    'platforms:description',
    'The multi-surface deployment matrix and per-platform model lifecycle for the Claude estate — compliance posture by surface and divergent retirement deadlines. Reference served by the engine, never recomputed.',
  ],
  [
    'rate-limits:description',
    'Read-only inventory of the organization- and workspace-scoped rate limits a gateway or proxy must keep in sync. Sourced from the Anthropic Rate Limits API (read-only); this view never changes a limit.',
  ],
  [
    'model-ops:subtitle',
    'Govern owned models, their signed-model admission and local inference deployments.',
  ],
  [
    'sandbox:actions.replayHint',
    'Re-executes a historical session deterministically against mocks. If the timeline cannot be reconstructed the replay comes back DEGRADED with zero steps — that is not a session that did nothing, it is a timeline that could not be rebuilt.',
  ],
  ['sandbox:seam.synthetic', 'Local data generation'],
  ['source-diff:requires', 'Requires'],
  ['source-diff:permission', 'system:admin'],
  [
    'communications:handoff.inbox.emptyBody',
    'This is an empty authorized result, not a refusal and not a failure to look.',
  ],
  [
    'protocol-bindings:subtitle',
    'Compose, inspect, and reconcile governed A2A and MCP bindings without treating unavailable evidence as success.',
  ],
  ['workspace-dashboard:truncation.label', 'Loaded {{n}} rows; there are more'],
  [
    'inventory:scope.note',
    'Tenant-wide inventory. The workspace selector does not filter this view.',
  ],
  ['usage:today.unpriced_other', '{{count}} sessions reported no cost.'],
  ['usage:today.atLeast', 'At least'],
  ['models:usage.withoutTokens', '{{n}} without tokens'],
  ['models:usage.withoutCost', '{{n}} without cost'],
  ['models:usage.notReported', 'not reported'],
  [
    'audit:scan.incomplete',
    'Scanned up to seq {{seq}} — not the full ledger; Load more continues the scan.',
  ],
  ['health:deps.truncated', 'Showing first {{n}}'],
  [
    'capabilities:subtitle',
    'The live capability catalog — MCP servers, tools, skills and how they are connected.',
  ],
  [
    'console:mcpGateway.storeOwned',
    'The organization’s settings govern MCP on this installation’s HTTP listener. No configuration file is required.',
  ],
  ['console:mcpGateway.sessionOn', 'On: turn it off to stop it.'],
  ['audit:internal.hidden', 'Internal records hidden: {{count}}'],
  ['audit:ledger.placeholder', 'person:  action:  since:7d  or words'],
  ['audit:ledger.histogram.loaded_other', '{{count}} entries loaded'],
  ['audit:ledger.histogram.more', 'more to load'],
]

/** Engineering words no English string of these features may carry. */
const WORDS = /OLIVARES_[A-Z_]+|system:admin/

function lookup(id: string): unknown {
  const [dir, path] = id.split(':')
  const catalog = catalogs[`/src/features/${dir}/i18n/en.json`]
  if (!catalog) throw new Error(`no English catalog for ${dir}`)
  let value: unknown = catalog
  for (const part of path.split('.'))
    value = (value as Record<string, unknown> | undefined)?.[part]
  return value
}

function strings(value: unknown): string[] {
  if (typeof value === 'string') return [value]
  if (value && typeof value === 'object')
    return Object.values(value).flatMap(strings)
  return []
}

describe('pages do not explain the engine', () => {
  it.each(RETIRED)('%s no longer carries its engineering text', (id, text) => {
    expect(lookup(id)).not.toBe(text)
  })

  it.each(['logs', 'source-diff'])(
    '%s names no environment variable or permission',
    (dir) => {
      const catalog = catalogs[`/src/features/${dir}/i18n/en.json`]
      expect(strings(catalog).filter((s) => WORDS.test(s))).toEqual([])
    },
  )
})
