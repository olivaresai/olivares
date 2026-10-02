// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE KILL SWITCH, ON EVERY PAGE (console remake 26.10, CONCEPT-IA row "Kill switch"): while
// a stop is engaged, the top bar carries one status pill that opens the kill switch —
// danger for an estate stop, warning for agent stops — and nothing when no stop is active.
// It lives IN the top bar, not as a strip above the work: the shell keeps one header row
// (app-layout.regions.test.tsx), and an emergency state must not move every page down.
//
// The read is the kill switch's own live posture under its own cache key, so this pill and
// the kill switch page share one answer; it is made only for a principal who may open that
// page. The words are the kill switch banner's, carried in the shell's `nav` namespace so
// the shell does not load the whole kill switch dictionary.
import { Link } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import { useKillSwitchState } from './use-killswitch-state'

export function KillSwitchStatus() {
  const { t } = useTranslation('nav')
  const { posture } = useKillSwitchState()
  if (!posture || (!posture.estate && posture.agentStops === 0)) return null
  const { estate, agentStops } = posture
  // A live region around the link, not on it: `status` is not a role a link may take
  // (axe aria-allowed-role), and a stop that starts or ends is announced either way.
  return (
    <span role="status" className="inline-flex shrink-0">
      <Link
        to={'/killswitch' as never}
        data-testid="killswitch-status"
        title={
          estate
            ? `${t('shell.killswitch.estateStopped')} — ${t('shell.killswitch.estateBody')}`
            : t('shell.killswitch.agentStops', { count: agentStops })
        }
        className={cn(
          'inline-flex h-7 shrink-0 items-center gap-1.5 rounded-full px-2.5 text-caption font-semibold whitespace-nowrap outline-none focus-visible:ring-2 focus-visible:ring-focus',
          // On a phone the bar holds the brand, search and the account: the pill keeps its
          // colour and its dot, and its words go to the accessible name and the tooltip.
          'max-[760px]:px-2',
          estate
            ? 'bg-danger-soft text-danger hover:bg-danger-soft/80'
            : 'bg-warning-soft text-warning hover:bg-warning-soft/80',
        )}
      >
        <span
          aria-hidden
          className={cn(
            'size-1.5 rounded-full',
            estate ? 'bg-danger' : 'bg-warning',
          )}
        />
        <span className="max-[760px]:sr-only">
          {estate
            ? t('shell.killswitch.estateStopped')
            : t('shell.killswitch.agentStops', { count: agentStops })}
        </span>
      </Link>
    </span>
  )
}
