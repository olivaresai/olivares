// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// DECLARED reference catalog of FUTURE FinOps dimensions / cost breakdowns that the
// Anthropic platform exposes but the engine does NOT yet model as queryable /spend
// slices (ANT2-02 + ANT2-15). This is a STATIC reference dataset — there
// is NO live HTTP endpoint for these. Per the honesty contract: they are
// rendered behind a SeamBadge ("Future dimensions — backend pending"), labelled
// "Declared reference — AsOf <date>", and NEVER presented as a queryable slice or
// faked as live data. Each entry cites its authoritative source. AsOf is the date this
// catalog was verified against the live docs; revisit when the backend lands the dim.
//
// Sources verified 2026-06-06 (deep-sweep):
//  • ANT2-02 — Usage/Cost Report group_by has 9 dims (not 7): the 7 modeled
//    (api_key_id/workspace_id/model/service_tier/context_window/inference_geo + the
//    estate dims) PLUS `speed` and `service_account_id` and `account_id`, plus a
//    `speeds[]` filter (beta fast-mode-2026-02-01).
//  • ANT2-15 — runtime cost/forensic blind-spots: `advisor` (a 2nd hidden server-side
//    inference billed separately in usage.iterations[].advisor_message), extended
//    thinking (usage.output_tokens_details.thinking_tokens), and programmatic tool
//    calling (results outside the context AND outside `usage` → FinOps under-counts).

/** The date this catalog was verified against the live Anthropic docs. */
export const FUTURE_DIMS_AS_OF = '2026-06-06'

const USAGE_COST_REPORT =
  'https://platform.claude.com/docs/en/api/admin-api/usage-cost/get-messages-usage-report'
const ADVISOR_TOOL =
  'https://platform.claude.com/docs/en/build-with-claude/tool-use/advisor-tool'
const PROGRAMMATIC_TOOL =
  'https://platform.claude.com/docs/en/build-with-claude/tool-use/programmatic-tool-calling'
const EXTENDED_THINKING =
  'https://platform.claude.com/docs/en/build-with-claude/extended-thinking'

/** Maturity of a declared-but-unmodeled item, so the UI can mark beta surfaces. */
export type FutureMaturity = 'beta' | 'ga' | 'development'

/** A FUTURE group_by dimension the Usage/Cost Report exposes but /spend cannot yet
 *  slice by. `groupBy` is the verbatim Anthropic group_by token. */
export interface FutureDimension {
  /** Stable id (also the i18n sub-key under `future.dims`). */
  id: string
  /** The verbatim Anthropic group_by / filter token. */
  groupBy: string
  maturity: FutureMaturity
  /** Anthropic doc the dimension was verified against. */
  source: string
  /** ANT gap reference.*/
  ref: string
}

/** The future group_by dimensions (ANT2-02). These are DISTINCT from the conflated
 *  api_key dimension /spend already serves: service_account_id is a separate NHI
 *  principal (svac_, credentials minted on-demand by WIF), not an API key. */
export const FUTURE_DIMENSIONS: readonly FutureDimension[] = [
  {
    id: 'speed',
    groupBy: 'speed',
    maturity: 'beta',
    source: USAGE_COST_REPORT,
    ref: 'ANT2-02',
  },
  {
    id: 'service_account_id',
    groupBy: 'service_account_id',
    maturity: 'ga',
    source: USAGE_COST_REPORT,
    ref: 'ANT2-02',
  },
  {
    id: 'account_id',
    groupBy: 'account_id',
    maturity: 'ga',
    source: USAGE_COST_REPORT,
    ref: 'ANT2-02',
  },
] as const

/** A FUTURE cost breakdown that today escapes the cost stream (ANT2-15). Each is a
 *  blind-spot the estimate under-counts or cannot attribute until the connector
 *  surfaces the runtime field. */
export interface FutureBreakdown {
  /** Stable id (also the i18n sub-key under `future.breakdowns`). */
  id: string
  /** The verbatim runtime field carrying the cost (where one exists). */
  usageField?: string
  maturity: FutureMaturity
  source: string
  ref: string
}

export const FUTURE_BREAKDOWNS: readonly FutureBreakdown[] = [
  {
    id: 'advisor',
    usageField: 'usage.iterations[].advisor_message',
    maturity: 'beta',
    source: ADVISOR_TOOL,
    ref: 'ANT2-15',
  },
  {
    id: 'thinking_tokens',
    usageField: 'usage.output_tokens_details.thinking_tokens',
    maturity: 'ga',
    source: EXTENDED_THINKING,
    ref: 'ANT2-15',
  },
  {
    id: 'programmatic_tool_calling',
    maturity: 'beta',
    source: PROGRAMMATIC_TOOL,
    ref: 'ANT2-15',
  },
] as const
