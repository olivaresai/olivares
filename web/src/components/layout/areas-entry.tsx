// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ALL AREAS (redesign §3.2): one entry below the sessions that opens the directory of the
// nine registry areas (NAV_AREAS), with every module this principal may open, the filter
// and the pins. The count is the areas this principal may open. While the current page
// sits in an area that no journey names (the Work area, say), the entry is marked as the
// branch and names that area, so "where am I" is answered in the sidebar too.
import { useRouterState } from '@tanstack/react-router'
import { LayoutGrid } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import { useViewAccess } from '@/features/navigation/authorization'
import {
  activeAreaId,
  areaLabel,
  resolveLocation,
} from '@/features/navigation/model'
import { SHELL_ROW_CLASS } from './shell-row'
import {
  DESTINATION_VIEW_IDS,
  authorizedAreas,
  coveredByDestination,
} from './shell-destinations'

const JOURNEYS: ReadonlySet<string> = new Set(DESTINATION_VIEW_IDS)

export function AreasEntry({
  open,
  onOpen,
}: {
  open: boolean
  onOpen: () => void
}) {
  const { t } = useTranslation('nav')
  const { navigable } = useViewAccess()
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const search = useRouterState({
    select: (s) => (s.location.search ?? {}) as Record<string, unknown>,
  })
  const location = resolveLocation(pathname)
  const inJourney =
    (location.kind === 'view' || location.kind === 'home') &&
    (JOURNEYS.has(location.view.id) ||
      coveredByDestination(location.view.id, search))
  const area = inJourney ? null : activeAreaId(location)
  const count = authorizedAreas(navigable).length
  if (count === 0) return null
  return (
    <button
      type="button"
      onClick={onOpen}
      aria-haspopup="dialog"
      aria-expanded={open}
      data-branch={area ? 'active' : undefined}
      className={cn(
        SHELL_ROW_CLASS,
        // Its own row below the session rail, never squeezed into it (EU-17).
        'w-full shrink-0 text-left',
        'data-[branch=active]:bg-active data-[branch=active]:text-text',
      )}
    >
      <LayoutGrid aria-hidden />
      <span className="flex min-w-0 flex-1 flex-col py-1">
        <span className="break-words">{t('shell.allAreas')}</span>
        {area ? (
          <span className="text-caption font-normal break-words text-text-3">
            {areaLabel(t, area)}
          </span>
        ) : null}
      </span>
      <span className="text-overline text-text-3 tabular-nums">
        <span className="sr-only">, </span>
        {count}
      </span>
    </button>
  )
}
