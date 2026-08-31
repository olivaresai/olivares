// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Deterministic Rate Limits fixtures (ANT2-05). Two provenance classes:
//  • `findingFixture` mirrors the REAL governance Info finding the connector emits
//    (governance.go:230-242) — the count summary the live endpoint returns today.
//  • `inventoryFixture` is shaped EXACTLY like the LIVE inventory rows, transcribed from
//    connectors/claude-api/testdata/rate_limits.json. As of the inventory route is
//    live (`GET /v1/m/models/rate-limits`), so `inventoryResponseFixture` wraps these
//    rows in the real `RateLimitInventory` envelope; `inventoryUnavailableFixture` is the
//    honest `available=false` degraded state. The tests exercise the pure pieces + flip.
//
// HONESTY exercised by the data itself: org-wide groups (empty workspace_ref), a
// per-workspace override (set workspace_ref + org_limit echo), an OPEN/unknown
// group_type, and a non-model group with no models — so the components are tested
// against every truth-telling rule.
import type { RateLimit, RateLimitFinding, RateLimitInventory } from './types'

/** The verbatim ANT2-05 coverage caveat the backend always returns (modules/models/
 *  ratelimits.go rateLimitsCaveat). */
const RATE_LIMITS_CAVEAT =
  'Managed Agents are NOT covered by the Anthropic Rate Limits API (ANT2-05); gateways and proxies must keep these limits in sync.'

/** The REAL Info finding (subject_kind anthropic.rate_limit) — its title carries the
 *  count of limits a gateway/proxy must keep in sync; the detail is a redacted hash. */
export const findingFixture: RateLimitFinding = {
  id: 'fnd-rl-org',
  kind: 'governance',
  severity: 'info',
  status: 'open',
  subject_kind: 'anthropic.rate_limit',
  subject_ref: 'organization',
  title: '4 rate-limit group(s) a gateway/proxy must keep in sync',
  detail_hash:
    'a3f1c0d2e4b5968778695a4b3c2d1e0f9a8b7c6d5e4f3a2b1c0d9e8f7a6b5c4d',
  occurred_at: '2026-06-04T09:12:00Z',
}

/** The LIVE per-group inventory rows, transcribed from testdata/rate_limits.json plus
 *  a per-workspace override and the honesty edge cases. The view renders these via the
 *  live AsyncSection (no longer a seam), flattening one display row per limiter. */
export const inventoryFixture: RateLimit[] = [
  // testdata/rate_limits.json[0] — org-wide model_group with model ids + aliases.
  {
    workspace_ref: '',
    group_type: 'model_group',
    models: ['claude-opus-4-5', 'claude-opus-4-8'],
    limits: [
      { type: 'requests_per_minute', value: 4_000 },
      { type: 'input_tokens_per_minute', value: 10_000_000 },
      { type: 'output_tokens_per_minute', value: 800_000 },
    ],
  },
  // testdata/rate_limits.json[1] — org-wide batch group; models omitted.
  {
    workspace_ref: '',
    group_type: 'batch',
    limits: [{ type: 'enqueued_batch_requests', value: 500_000 }],
  },
  // A per-workspace override (set workspace_ref → grouped under "per-workspace").
  {
    workspace_ref: 'ws_marketing',
    group_type: 'model_group',
    models: ['claude-opus-4-5', 'claude-opus-4-8'],
    limits: [
      { type: 'requests_per_minute', value: 1_000, org_limit: 4_000 },
      {
        type: 'input_tokens_per_minute',
        value: 500_000,
        org_limit: 10_000_000,
      },
    ],
  },
  // An OPEN/unknown group_type the view must render gracefully (no closed enum).
  {
    workspace_ref: '',
    group_type: 'priority_tier',
    limits: [{ type: 'requests_per_minute', value: 50 }],
  },
]

/** The LIVE inventory response (available=true) — the Admin connector is wired and the
 *  rows are authoritative. */
export const inventoryResponseFixture: RateLimitInventory = {
  available: true,
  rate_limits: inventoryFixture,
  caveat: RATE_LIMITS_CAVEAT,
}

/** The honest degraded response (200, available=false): the read-only Admin connector
 *  is not wired, so there is NO authoritative inventory — never a fabricated empty list. */
export const inventoryUnavailableFixture: RateLimitInventory = {
  available: false,
  reason:
    'the Claude Admin-API connector is not wired; the rate-limit inventory is unavailable (provision the read-only Admin credential to enable it)',
  rate_limits: [],
  caveat: RATE_LIMITS_CAVEAT,
}
