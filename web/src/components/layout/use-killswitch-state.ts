// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The kill switch's live posture for the shell and Now (console remake 26.10): the kill
// switch page's own read and cache key, so every surface that states it shares one answer.
import { useQuery } from '@tanstack/react-query'
import { killswitchApi, killswitchKeys } from '@/features/killswitch/api'
import { useAuth } from '@/lib/auth/context'

/** The kill switch page's permission (registry entry `killswitch`). */
const KILLSWITCH_READ = 'governance:killswitch:read'
/** An emergency state: read often enough that a stop shows within half a minute. */
const STATE_POLL_MS = 30_000

/** The live stop posture as the kill switch page reads it, under its own cache key:
 * `permitted` says whether this principal may read it at all, `query` carries the read's
 * own loading and error states, and `posture` is set only from an answer — never "no
 * stop" by default. Shared by the top bar pill and Now. */
export function useKillSwitchState() {
  const { can, activeTenant } = useAuth()
  const permitted = can(KILLSWITCH_READ)
  const query = useQuery({
    queryKey: killswitchKeys.state(activeTenant),
    queryFn: () => killswitchApi.state(),
    enabled: permitted && !!activeTenant,
    refetchInterval: STATE_POLL_MS,
  })
  const data = query.data
  const posture =
    permitted && data
      ? {
          estate: data.estate_stopped,
          active: data.active.length,
          agentStops: data.active.filter((s) => s.scope_kind === 'agent')
            .length,
        }
      : null
  return { permitted, query, posture }
}
