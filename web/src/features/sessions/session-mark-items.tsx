// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE PERSON'S OWN MARKS AS MENU ITEMS: unread, snooze and done. The thread
// header's menu and the session row's menu both render this, over the one hook that sends
// them (`use-session-marks.ts`), so the two menus cannot say different things.
import {
  AlarmClock,
  AlarmClockOff,
  CircleCheck,
  CircleDashed,
  Mail,
  MailOpen,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { DropdownMenuItem } from '@/components/ui/dropdown-menu'
import type { UnifiedSession } from './provenance'
import { isMarkedDone, isSnoozed, isUnread } from './session-groups'
import { markableRun, type SessionMarks } from './use-session-marks'
import './i18n'

const HOUR_MS = 3_600_000

/** The moments a snooze offers, from now. The CLI takes any duration or time. */
const SNOOZE_FOR = [
  { label: 'rail.snoozeHour', ms: HOUR_MS },
  { label: 'rail.snoozeDay', ms: 24 * HOUR_MS },
  { label: 'rail.snoozeWeek', ms: 7 * 24 * HOUR_MS },
] as const

/**
 * Mark unread (or read), the snooze presets (or Unsnooze) and Mark done (or not done).
 * A session the engine no longer lists a run for is offered only the way back from a
 * mark it already has: nothing it cannot take.
 */
export function SessionMarkItems({
  session,
  marks,
}: {
  session: Pick<UnifiedSession, 'runs'>
  marks: SessionMarks
}) {
  const { t } = useTranslation('sessions')
  const markable = !!markableRun(session)
  const unread = isUnread(session)
  const snoozed = isSnoozed(session)
  const markedDone = isMarkedDone(session)
  return (
    <>
      {unread || markable ? (
        <DropdownMenuItem onSelect={() => marks.setUnread(session, !unread)}>
          {unread ? <MailOpen /> : <Mail />}
          {unread ? t('rail.markRead') : t('rail.markUnread')}
        </DropdownMenuItem>
      ) : null}
      {snoozed ? (
        <DropdownMenuItem onSelect={() => marks.unsnooze(session)}>
          <AlarmClockOff />
          {t('rail.unsnooze')}
        </DropdownMenuItem>
      ) : markable ? (
        // Three plain items, not a submenu: no hover to hold, the same on a touch
        // screen, and each one names the whole action.
        SNOOZE_FOR.map(({ label, ms }) => (
          <DropdownMenuItem
            key={label}
            onSelect={() => marks.snooze(session, ms)}
          >
            <AlarmClock />
            {t(label)}
          </DropdownMenuItem>
        ))
      ) : null}
      {markedDone || markable ? (
        <DropdownMenuItem onSelect={() => marks.setDone(session, !markedDone)}>
          {markedDone ? <CircleDashed /> : <CircleCheck />}
          {markedDone ? t('rail.markNotDone') : t('rail.markDone')}
        </DropdownMenuItem>
      ) : null}
    </>
  )
}
