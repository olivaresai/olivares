// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// DTOs for the read-only Rate Limits view (ANT2-05). Two provenance classes
//:
//  • REAL — the governance Info Finding the connector already emits, read over the
//    live security-findings endpoint (connectors/claude-api/governance.go:230-242,
//    subjectRateLimit = "anthropic.rate_limit"). It carries only the COUNT of limits
//    a gateway/proxy must mirror + the documented Managed-Agents caveat (a redacted
//    detail_hash, never a payload).
//  • LIVE — the per-group inventory, now served by `GET /v1/m/models/rate-limits`
//    (modules/models/ratelimits.go depth-fill; flipped from a declared seam in
//). It mirrors modelprovider.RateLimitRef 1:1 (connectors/modelprovider/
//    catalog.go). The route ALWAYS answers 200 (never a 404 seam): it returns
//    `available=false` + a `reason` when the read-only Admin connector is not wired, so
//    the view shows an honest "unavailable" notice, never a fabricated empty inventory.
//
// The web PRESENTS, it does not recompute (ARCHITECTURE.md): `group_type` is OPEN
// vocabulary (widened with `| string`), `models` is present only for model_group rows,
// and workspace rows are OVERRIDES ONLY: an absent group/limiter means inherit the org
// value, not unlimited.

// --- REAL: the governance Finding (security module findingDTO) -------------------

/** A security/governance Finding as emitted by the read-only Claude connectors.
 *  Mirrors core/api/dto.go findingDTO (the same shape the security view consumes);
 *  `detail_hash` is a one-way fingerprint, never a payload (docs/SECURITY-HARDENING.md). The
 *  rate-limit summary arrives as an `Info` finding with subject_kind
 *  `anthropic.rate_limit` (governance.go:236-241). */
export interface RateLimitFinding {
  id: string
  /** governance | posture | … (open vocabulary; widened). */
  kind: string
  /** info | low | medium | high | critical (the Info summary is `info`). */
  severity: string
  status: string
  /** "anthropic.rate_limit" for the count summary; "anthropic.surface" when ingest
   *  is degraded on a no-Admin-API surface. */
  subject_kind?: string
  subject_ref?: string
  /** e.g. "%d rate-limit group(s) a gateway/proxy must keep in sync" — the count is
   *  in the title; the COUNT itself is the only quantitative value the finding carries. */
  title?: string
  /** Redacted fingerprint — render as a hash, never expand to content. */
  detail_hash?: string
  occurred_at?: string
  metadata?: Record<string, unknown>
}

// --- LIVE: the per-group inventory (mirrors RateLimitRef) -------------------------

/** Known `group_type` partitions the Rate Limits API reports. OPEN vocabulary — the
 *  view renders unknown values gracefully and NEVER validates against this as a
 *  closed enum (catalog.go:322-324; `| string` keeps it provider-driven). */
export type RateLimitGroupType =
  | 'model_group'
  | 'batch'
  | 'token_count'
  | 'files'
  | 'skills'
  | 'web_search'
  | string

/** One limiter inside a rate-limit group. `org_limit` is the workspace endpoint's
 *  org-level echo; absence means not reported/not applicable, never a hard zero. */
export interface RateLimitValue {
  /** Provider limiter vocabulary, e.g. requests_per_minute. */
  type: string
  /** Numeric ceiling for this limiter. */
  value: number
  /** Org-level value for the same limiter on workspace override rows. */
  org_limit?: number
}

/** One organization- or workspace-scoped rate-limit group (LIVE; mirrors
 *  modelprovider.RateLimitRef, connectors/modelprovider/catalog.go). It is
 *  read-only inventory a gateway/proxy operator must keep in sync — NOT a control the
 *  product mutates (the view has no edit/create affordance). */
export interface RateLimit {
  /** The workspace the group is scoped to; empty/absent for an organization-wide
   *  group (the view groups org vs per-workspace on this). */
  workspace_ref?: string
  /** Partitions the group (model_group|batch|token_count|files|skills|web_search …);
   *  carried as provider vocabulary, not a sealed enum. */
  group_type: RateLimitGroupType
  /** Model ids and aliases for model_group rows; absent otherwise. */
  models?: string[] | null
  /** The concrete limiters reported inside this group. */
  limits: RateLimitValue[]
}

/** GET /v1/m/models/rate-limits — the inventory response (modules/models/ratelimits.go
 *  rateLimitsResponseDTO). The route ALWAYS returns 200: `available=false` with a
 *  `reason` distinguishes an UNWIRED Admin connector (or a transient fetch failure) from
 *  a genuinely empty inventory — the view shows the reason, never an invented empty list.
 *  `caveat` is the verbatim ANT2-05 coverage limit a mirroring gateway/proxy must honor. */
export interface RateLimitInventory {
  /** true = the read-only Admin connector is wired and the list is authoritative;
   *  false = unavailable (unwired or a transient fetch failure) — see `reason`. */
  available: boolean
  /** Why the inventory is unavailable (operator-facing); present only when
   *  `available` is false. */
  reason?: string
  /** The org- and workspace-scoped limits a gateway/proxy must keep in sync. */
  rate_limits: RateLimit[]
  /** The documented Managed-Agents coverage caveat (always present). */
  caveat: string
}
