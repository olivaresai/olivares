// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ALL AREAS, ON THE RAIL: one icon button that opens the directory of the registry areas
// (NAV_AREAS) as a sheet. The full sidebar lists the areas itself (SidebarAreas); the rail
// has no room for them. While no sidebar destination is current on the page (no pin shares
// its section) and the footer's Settings does not hold it, the button is marked as the
// branch, so "where am I" is answered on the rail too.
import { useRouterState } from '@tanstack/react-router'
import { LayoutGrid } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import { useViewAccess } from '@/features/navigation/authorization'
import { activeAreaId, resolveLocation } from '@/features/navigation/model'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { RAIL_ITEM_CLASS } from './shell-row'
import {
  authorizedAreas,
  currentDestination,
  inSettingsArea,
} from './shell-destinations'

export function AreasEntry({
  open,
  onOpen,
}: {
  open: boolean
  onOpen: () => void
}) {
  const { t } = useTranslation('nav')
  const { listed } = useViewAccess()
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const search = useRouterState({
    select: (s) => (s.location.search ?? {}) as Record<string, unknown>,
  })
  const location = resolveLocation(pathname)
  const held =
    currentDestination(location, search, listed) !== null ||
    inSettingsArea(location)
  const area = held ? null : activeAreaId(location)
  if (authorizedAreas(listed).length === 0) return null
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <button
          type="button"
          onClick={onOpen}
          aria-haspopup="dialog"
          aria-expanded={open}
          aria-label={t('shell.allAreas')}
          data-branch={area ? 'active' : undefined}
          className={cn(
            RAIL_ITEM_CLASS,
            'data-[branch=active]:bg-active data-[branch=active]:text-text',
          )}
        >
          <LayoutGrid aria-hidden />
        </button>
      </TooltipTrigger>
      <TooltipContent side="right">{t('shell.allAreas')}</TooltipContent>
    </Tooltip>
  )
}
