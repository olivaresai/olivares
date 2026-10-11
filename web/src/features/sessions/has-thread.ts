// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { primaryRun } from './provenance'
import type { SessionResolution } from './use-session-resolution'

/**
 * Does this session have a conversation to show? Only a bridged run does. The thread
 * paints it; a session without one has the overview as its body, and a session with one
 * has the overview in the Context pane.
 */
export function hasThread(resolution: SessionResolution): boolean {
  const run = primaryRun(resolution.session.runs)
  return !!run && run.transport !== 'remote-control'
}
