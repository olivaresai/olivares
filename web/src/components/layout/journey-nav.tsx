// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SIDEBAR'S DESTINATIONS (console remake 26.10): the work section first, then the
// product's three verbs — Manage, Integrate, Secure. Registry ids, pinned in this order,
// each shown only when the principal may open it; a group with no permitted destination
// is not drawn. The selected destination carries the one orange row of the brand mark
// at its left edge.
import { Link, useRouterState } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { useViewAccess } from '@/features/navigation/authorization'
import { resolveLocation } from '@/features/navigation/model'
import {
  destinationOf,
  groupedDestinations,
  sectionAt,
} from './shell-destinations'
import { SHELL_ROW_CLASS } from './shell-row'

export function JourneyNav({
  counts,
  attention,
  onNavigate,
}: {
  /** A count shown at the right of a destination ("Approvals 2"), by its label key. */
  counts?: Readonly<Record<string, number | string>>
  /** Keys whose count asks for a person (drawn in the warning role, not as plain text). */
  attention?: ReadonlySet<string>
  onNavigate?: () => void
}) {
  const { t } = useTranslation('nav')
  const { navigable } = useViewAccess()
  const groups = groupedDestinations(navigable)
  // A destination that spans several views is current on each of its members, whose
  // paths are not under its own (/routine-policies belongs to Policies).
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const search = useRouterState({
    select: (s) => (s.location.search ?? {}) as Record<string, unknown>,
  })
  const location = resolveLocation(pathname)
  // A section address (Approvals: /permissions?tab=approvals) is the section's own: its
  // view's destination (Identity & access) is not marked as well.
  const memberOf =
    location.kind === 'view' && !sectionAt(location.view.id, search)
      ? destinationOf(location.view.id)
      : null
  return (
    <nav
      aria-label={t('shell.journeysLabel')}
      className="flex min-h-0 flex-col gap-3 overflow-y-auto pr-0.5 [scrollbar-width:thin]"
    >
      {groups.map((group) => {
        const labelled = group.id !== 'work'
        const headingId = `shell-group-${group.id}`
        return (
          <div
            key={group.id}
            role="group"
            aria-labelledby={labelled ? headingId : undefined}
            aria-label={labelled ? undefined : t('shell.groups.work')}
            data-shell-group={group.id}
            className="flex flex-col gap-px"
          >
            {labelled ? (
              <div
                id={headingId}
                className="px-2.5 pb-1 text-overline font-medium tracking-[0.06em] text-text-3 uppercase"
              >
                {t(`shell.groups.${group.id}`)}
              </div>
            ) : null}
            {group.destinations.map((j) => {
              const Icon = j.icon
              const count = counts?.[j.key]
              const urgent = attention?.has(j.key) ?? false
              return (
                <Link
                  key={j.key}
                  to={j.path as never}
                  search={j.search as never}
                  activeOptions={{ exact: j.exact }}
                  activeProps={{ 'aria-current': 'page' }}
                  aria-current={
                    !j.search && memberOf === j.id ? 'page' : undefined
                  }
                  onClick={onNavigate}
                  data-journey={j.key}
                  className={SHELL_ROW_CLASS}
                >
                  <Icon aria-hidden />
                  <span className="min-w-0 flex-1 py-1 break-words">
                    {t(`shell.journeys.${j.key}`)}
                  </span>
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
                </Link>
              )
            })}
          </div>
        )
      })}
    </nav>
  )
}
