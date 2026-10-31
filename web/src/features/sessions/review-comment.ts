// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SENTENCE A LINE COMMENT BECOMES. A comment on a line of a session's branch diff goes
// back to the agent as its next message, whatever tool the session runs: the agent needs the
// file, the line, which version of the file the line belongs to, the line itself and then the
// comment, in plain text it can act on. This is the one place that wording is made. It is
// written to the agent, not to the person, so it is not translated.
import type { ReviewCommentPlace } from '@/features/agentops/session-turn'

/** A quoted line longer than this is cut: the agent has the file, it needs the place. */
const QUOTED_LINE_MAX = 200

/** The message to send for a comment: where, the line quoted, then the comment as typed. */
export function reviewCommentText(
  place: ReviewCommentPlace,
  lineText: string,
  comment: string,
): string {
  const version =
    place.side === 'original' ? 'at the base' : 'at the branch tip'
  const line = lineText.replace(/\r$/, '')
  const quoted =
    line.length > QUOTED_LINE_MAX ? `${line.slice(0, QUOTED_LINE_MAX)}…` : line
  return `Comment on ${place.path}, line ${place.line} (${version}):\n> ${quoted}\n\n${comment.trim()}`
}
