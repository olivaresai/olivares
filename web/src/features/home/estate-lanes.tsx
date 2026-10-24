// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { PANEL_EXTENSIONS } from '@/features/extensions'
import { isLiveSession } from './now-sessions'
import { Swimlane, type SwimlaneProps } from './swimlane'
import { FLEET_SESSIONS } from './swimlane-model'

/** Estate's last hour: one swimlane per session, or, past FLEET_SESSIONS live sessions, the
 * fleet panel a build supplies (#2565). Only one of the two mounts, so the fleet does not
 * read every session's hour. */
export function EstateLanes(props: SwimlaneProps) {
  const Fleet = PANEL_EXTENSIONS.estateFleet
  const live = props.sessions.filter(isLiveSession).length
  return Fleet && live > FLEET_SESSIONS ? (
    <Fleet {...props} />
  ) : (
    <Swimlane {...props} />
  )
}
