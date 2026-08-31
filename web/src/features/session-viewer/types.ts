// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Types for the unified session recording viewer. The viewer presents
// a merged view of forensic frames and the operational timeline side-by-side.
// All frame/session/ledger shapes are imported from features/recordings — this
// file only defines the viewer-specific aggregations and the UnifiedResponse
// envelope that the /unified endpoint returns.
import type {
  FrameDTO,
  LedgerEventDTO,
  SessionDTO,
} from '@/features/recordings/types'

/** One entry in the operational timeline (tool call, MCP event, cost, finding). */
export interface TimelineEntry {
  at: string
  kind: 'tool' | 'mcp' | 'cost' | 'finding'
  tool_ref?: string
  resource_ref?: string
  mode?: string
  source?: string
  title?: string
}

/** Presence indicator for a live (still-active) session. */
export interface LiveCorrelation {
  session_ref: string
}

/** One failed ledger-anchor check returned by the recording verifier. */
export interface AnchorFailure {
  kind: 'open' | 'periodic' | 'seal' | (string & {})
  seq: number
  at_idx?: number
  reason: string
}

/** Complete server-side chain re-verification verdict. */
export interface VerifyResult {
  ok: boolean
  frames_checked: number
  break_at?: number
  reason?: string
  written: number
  reserved: number
  gap: boolean
  tip_match: boolean
  anchors_ok: boolean
  anchors_checked: number
  anchor_failures?: AnchorFailure[]
  anchored_through: number
}

/**
 * Timeline rows need an identity tied to their source page. The backend entries
 * have no id and timestamps can repeat, so the viewer assigns a key from the
 * lane's input cursor plus its source-page index.
 */
export interface KeyedTimelineEntry {
  key: string
  entry: TimelineEntry
}

/** Frame rows are content-addressed by their chain hash. */
export interface KeyedFrame {
  key: string
  frame: FrameDTO
}

/**
 * GET /sessions/{id}/unified — the single response the viewer page consumes.
 * Combines session header, a paginated frame page, a paginated timeline page,
 * the correlated ledger events, and an optional pre-computed verify verdict so
 * the viewer can open with integrity status without a second round-trip.
 */
export interface UnifiedResponse {
  schema: string
  semconv: string
  session: SessionDTO
  live: LiveCorrelation | null
  frames: { items: FrameDTO[]; cursor?: string; has_more: boolean }
  timeline: {
    items: TimelineEntry[]
    cursor?: string
    has_more: boolean
    /** False means the cross-module resolver was absent or failed; it is not
     * evidence that the session had no operational activity. */
    available: boolean
  }
  ledger: LedgerEventDTO[]
  ledger_truncated: boolean
  verify: VerifyResult | null
}

/** Aggregated tool-call statistics for the summary panel. */
export interface ToolAggregation {
  tool: string
  count: number
  successCount: number
  failCount: number
}

/** One file touched in the session (read, write, edit, or create). */
export interface FileNode {
  name: string
  path: string
  type: 'read' | 'write' | 'edit' | 'create'
}

/** A cluster of consecutive frames that share the same decision context. */
export interface DecisionGroup {
  startIdx: number
  tools: Array<{ tool: string; resource: string; at: string }>
}
