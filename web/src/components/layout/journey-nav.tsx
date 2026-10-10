// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Pinned destinations, each shown only when the principal may open it. The current
// destination uses the neutral selection fill; session attention uses the warning role.
import { Link, useRouterState } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { useViewAccess } from '@/features/navigation/authorization'
import { resolveLocation } from '@/features/navigation/model'
import { currentDestination, journeyDestinations } from './shell-destinations'
import { RAIL_ITEM_CLASS, SHELL_ROW_CLASS } from './shell-row'

export function JourneyNav({
  counts,
  attention,
  onNavigate,
  variant = 'full',
}: {
  /** A count shown at the right of a destination ("Approvals 2"), by its label key. */
  counts?: Readonly<Record<string, number | string>>
  /** Keys whose count asks for a person (drawn in the warning role, not as plain text). */
  attention?: ReadonlySet<string>
  onNavigate?: () => void
  /** `rail`: icons only, each named by its label and shown in a tooltip. */
  variant?: 'full' | 'rail'
}) {
  const { t } = useTranslation('nav')
  const { listed } = useViewAccess()
  const destinations = journeyDestinations(listed)
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const search = useRouterState({
    select: (s) => (s.location.search ?? {}) as Record<string, unknown>,
  })
  const current = currentDestination(resolveLocation(pathname), search, listed)
  return (
    <nav
      aria-label={t('shell.journeysLabel')}
      className={
        variant === 'rail'
          ? 'flex min-h-0 flex-col items-center gap-1 overflow-y-auto [scrollbar-width:none]'
          : 'flex min-h-0 flex-col gap-px overflow-y-auto pr-0.5 [scrollbar-width:thin]'
      }
    >
      {destinations.map((j) => {
        const Icon = j.icon
        const count = counts?.[j.key]
        const urgent = attention?.has(j.key) ?? false
        const label = t(`shell.journeys.${j.key}`)
        const sessionAttention =
          j.key === 'sessions' && urgent ? (
            <span
              data-slot="session-attention"
              className={
                variant === 'rail'
                  ? 'absolute right-1 bottom-1'
                  : 'flex shrink-0 items-center'
              }
            >
              <span
                aria-hidden
                className="block size-1.5 rounded-full bg-warning"
              />
              <span className="sr-only">
                , {t('shell.rail.groups.needsYou')}
              </span>
            </span>
          ) : null
        // The rail draws the count as a badge, so its accessible name carries it.
        const railName = count
          ? `${label}, ${count}${urgent ? `, ${t('shell.rail.groups.needsYou')}` : ''}`
          : label
        if (variant === 'rail')
          return (
            <Tooltip key={j.key}>
              <TooltipTrigger asChild>
                <Link
                  to={j.path as never}
                  search={j.search as never}
                  activeOptions={{ exact: true }}
                  aria-current={current === j.key ? 'page' : undefined}
                  aria-label={railName}
                  onClick={onNavigate}
                  data-journey={j.key}
                  className={RAIL_ITEM_CLASS}
                >
                  <Icon aria-hidden />
                  {count ? (
                    <span
                      aria-hidden
                      className={
                        urgent
                          ? 'absolute -top-0.5 -right-0.5 grid h-4 min-w-4 place-items-center rounded-full bg-warning px-1 text-[10px] leading-none font-semibold text-canvas tabular-nums'
                          : 'absolute -top-0.5 -right-0.5 grid h-4 min-w-4 place-items-center rounded-full bg-active px-1 text-[10px] leading-none font-medium text-text-2 tabular-nums'
                      }
                    >
                      {count}
                    </span>
                  ) : null}
                  {sessionAttention}
                </Link>
              </TooltipTrigger>
              <TooltipContent side="right">
                {count ? `${label} · ${count}` : label}
              </TooltipContent>
            </Tooltip>
          )
        return (
          <Link
            key={j.key}
            to={j.path as never}
            search={j.search as never}
            // The model decides the current row. The router marks a link current on its
            // own (aria-current, after these props) wherever its path matches, so it is
            // held to the exact address, where it agrees with the model.
            activeOptions={{ exact: true }}
            aria-current={current === j.key ? 'page' : undefined}
            onClick={onNavigate}
            data-journey={j.key}
            className={SHELL_ROW_CLASS}
          >
            <Icon aria-hidden />
            <span className="min-w-0 flex-1 py-1 break-words">{label}</span>
            {count ? (
              <span
                className={
                  urgent
                    ? 'rounded-full bg-warning-soft px-1.5 py-px text-overline font-medium text-warning tabular-nums'
                    : 'text-overline text-text-3 tabular-nums'
                }
              >
                <span className="sr-only">, </span>
                {count}
              </span>
            ) : null}
            {sessionAttention}
          </Link>
        )
      })}
    </nav>
  )
}
