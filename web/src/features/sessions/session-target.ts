// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

/**
 * What the caller clicked. Any one identifies the session; the card resolves the
 * rest from the engine rather than from whatever list happened to be loaded.
 *  - `liveRef`    a live row by its own opaque id (B2) — the ONLY unambiguous name
 *                 of a profile-scoped row, and valid for a legacy row too.
 *  - `sessionRef` the LEGACY row of a bare provider session id.
 *  - `runRef`     a run; a profiled run resolves its managed row by `live_ref`, a
 *                 legacy run its session by `claude_session_id`.
 */
export interface SessionTarget {
  liveRef?: string
  sessionRef?: string
  runRef?: string
}
