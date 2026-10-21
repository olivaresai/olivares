// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A turn the tool ended on a usage or rate limit: one sentence in the session header, and
// the Move to action where the person can use it. The fact is the run's `limit`, taken from
// the tool's own result by the engine; this only says it.
import { ArrowRightLeft } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import type { RunDTO } from '@/features/agentops/types'
import type { Capability } from './provenance'
import { canMoveSession } from './provenance'
import { limitLifted, limitSentence } from './session-limit-text'
import { MoveSessionDialog } from './session-move'
import './i18n'

export function LimitNotice({
  run,
  caps,
}: {
  run: RunDTO
  caps: Capability[]
}) {
  const { t } = useTranslation('sessions')
  const [moveOpen, setMoveOpen] = useState(false)
  if (!run.limit || limitLifted(run.limit)) return null
  // A live session is stopped first and a stopped one moves as it is: either way the
  // person needs the control that gets it there.
  const canMove =
    canMoveSession(run) &&
    caps.some((c) => (c.id === 'stop' || c.id === 'resume') && c.available)
  return (
    <span
      data-testid="thread-limit"
      className="flex min-w-0 shrink-0 items-center gap-1"
    >
      <span className="truncate text-caption font-medium text-warning">
        {limitSentence(run.limit, t)}
      </span>
      {canMove && run.limit.kind === 'usage' ? (
        <>
          <Button
            variant="ghost"
            size="sm"
            data-testid="thread-limit-move"
            onClick={() => setMoveOpen(true)}
          >
            <ArrowRightLeft />
            {t('move.open')}
          </Button>
          {moveOpen && (
            <MoveSessionDialog
              key={run.run_ref}
              run={run}
              onClose={() => setMoveOpen(false)}
            />
          )}
        </>
      ) : null}
    </span>
  )
}
