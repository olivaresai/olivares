// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A NEW SESSION GETS ITS TITLE FROM ITS FIRST MESSAGE.
//
// The engine cuts the title (POST /runs/{ref}/title) from the transcript on its own host;
// no model is asked. This asks it once a session that has neither a name nor a title
// shows activity, and again only after a pause: before the first message is written the
// engine answers that there is nothing to cut from yet, and that is not a failure.
//
// ⛔ THE ASK IS `if_untitled`, SO TWO TABS ON ONE SESSION CANNOT TITLE IT TWICE and a name
//    typed in between is never replaced. And a person who may not read the session's
//    output, or write to it, is never asked on their behalf: the engine would refuse.
//
// ⛔ AN ANSWER OF NO IS NOT SHOWN. A title is a nicety on a row that already reads
//    "untitled"; a toast on viewing a session, every time the engine declines (a session
//    another person started is refused in Community), would be noise. The Regenerate
//    title item in the session menu says why when it fails.
import { useQueryClient } from '@tanstack/react-query'
import { useEffect } from 'react'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import type { RunDTO } from '@/features/agentops/types'
import { useAuth } from '@/lib/auth/context'
import { usePreferencesStore } from '@/stores/preferences'

/** The shortest wait before a session is asked again. */
export const AUTO_TITLE_RETRY_MS = 30_000

const lastAsked = new Map<string, number>()

/** Asks the engine to title `run` when it has no name and no title and the person may. */
export function useAutoTitle(run: RunDTO | undefined): void {
  const { activeTenant, can } = useAuth()
  const qc = useQueryClient()
  const style = usePreferencesStore((s) => s.titleStyle)
  const runRef = run?.run_ref
  const untitled = !!run && !run.name?.trim() && !run.title?.trim()
  const allowed = can('sessions:run:write') && can('sessions:output:read')
  const activity = run?.last_activity_at
  useEffect(() => {
    if (!runRef || !untitled || !allowed) return
    const key = `${activeTenant}/${runRef}`
    const now = Date.now()
    if (now - (lastAsked.get(key) ?? -Infinity) < AUTO_TITLE_RETRY_MS) return
    lastAsked.set(key, now)
    agentOpsApi
      .generateTitle(runRef, { style, if_untitled: true })
      .then(() =>
        qc.invalidateQueries({ queryKey: agentOpsKeys.all(activeTenant) }),
      )
      .catch(() => undefined)
  }, [runRef, untitled, allowed, activity, style, activeTenant, qc])
}
