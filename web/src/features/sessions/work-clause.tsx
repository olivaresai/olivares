// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { workLine } from '@/features/home/work-line'
import { ActionName } from './action-name'
import type { LiveDTO } from './types'

/**
 * A session's name or clause as the naming ladder answered it. When the ladder answered
 * with what the engine reported the session doing — the action and the resource, both
 * machine identifiers such as `web.search` or `appdb.public.customers` — each renders as
 * an identifier (code, machine face) and only the separator between them is copy. Every
 * other rung (the operator's name, a summary, a goal, the untitled word) is plain text.
 */
export function WorkClause({
  text,
  live,
}: {
  text: string
  live?: LiveDTO | null
}) {
  const line = live ? workLine(live) : null
  const action = live?.current_action?.trim()
  if (!line || line.from !== 'action' || line.text !== text || !action)
    return <>{text}</>
  const resource = live?.current_resource?.trim()
  return (
    <>
      <ActionName value={action} />
      {resource ? (
        <>
          {' · '}
          <ActionName value={resource} />
        </>
      ) : null}
    </>
  )
}
