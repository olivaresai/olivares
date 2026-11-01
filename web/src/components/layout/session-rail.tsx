// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SESSION RAIL's shared pieces: the status the reads report and the glyph of a row's
// state (the one status mark of the Sessions list). The grouping is `session-rail-model.ts`;
// the reads are `use-session-rail.ts`. The glyph is also drawn by the Business cockpit.
import type { SessionStatus } from '@/features/sessions/session-display-state'
import { SessionStatusMark } from '@/features/sessions/session-status'
import type { RailState } from './session-rail-model'

export interface RailStatus {
  loading: boolean
  error: boolean
}

/** The status each rail state says. A finished session is Done here; the rail's state
 * cannot tell a failed one, which the rows' own `status` can. */
const STATUS_OF: Record<RailState, SessionStatus> = {
  need: 'needs-you',
  live: 'working',
  ended: 'done',
  idle: 'idle',
}

/** The glyph of a state, with its word as the accessible name: the same mark the Sessions
 * list and Now draw (`SessionStatusMark`). Colour never carries the state alone: the word
 * is always there for a screen reader, and the shape differs. */
export function RailGlyph({ state }: { state: RailState }) {
  return <SessionStatusMark status={STATUS_OF[state]} />
}
