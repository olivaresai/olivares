// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE JOURNEYS (redesign §3.2): Home, Sessions, Workspaces, AI tools, Deploy — registry
// ids, pinned in this order, each shown only when the principal may open it. The selected
// journey carries the one orange row of the brand mark at its left edge.
import { Link } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { useViewAccess } from '@/features/navigation/authorization'
import { journeyDestinations } from './shell-destinations'
import { SHELL_ROW_CLASS } from './shell-row'

export function JourneyNav({
  counts,
  onNavigate,
}: {
  /** A count shown at the right of a journey ("Sessions 5"), by registry id. */
  counts?: Readonly<Record<string, number>>
  onNavigate?: () => void
}) {
  const { t } = useTranslation('nav')
  const { navigable } = useViewAccess()
  const journeys = journeyDestinations(navigable)
  return (
    <nav aria-label={t('shell.journeysLabel')} className="flex flex-col gap-px">
      {journeys.map((j) => {
        const Icon = j.icon
        const label = t(`shell.journeys.${j.id}`)
        const count = counts?.[j.id]
        return (
          <Link
            key={j.id}
            to={j.path as never}
            activeOptions={{ exact: j.exact }}
            activeProps={{ 'aria-current': 'page' }}
            onClick={onNavigate}
            data-journey={j.id}
            className={SHELL_ROW_CLASS}
          >
            <Icon aria-hidden />
            <span className="min-w-0 flex-1 py-1 break-words">{label}</span>
            {count ? (
              <span className="text-overline text-text-3 tabular-nums">
                <span className="sr-only">, </span>
                {count}
              </span>
            ) : null}
          </Link>
        )
      })}
    </nav>
  )
}
