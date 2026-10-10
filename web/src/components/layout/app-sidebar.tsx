// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SIDEBAR (console 1.0): navigation only, in two widths.
//
//   FULL (240 px)                      RAIL (56 px)
//   wordmark · fold                    brand mark · unfold
//   organization and workspace         search
//   Search and commands (⌘K)           destinations as icons (counts as badges)
//   destinations: Now · Sessions · …   …
//   favorites                          All areas
//   areas, grouped and folding         settings · account
//   engine state · theme · settings · account
//
// It lists no sessions: the Sessions page is the one list of sessions. The Sessions row
// carries the live count, in the warning role when a session needs the person. Below
// 761 px the phone bar replaces it. Who chooses the width: sidebar-mode.ts.
import { Link, useRouterState } from '@tanstack/react-router'
import type { ReactNode } from 'react'
import {
  PanelLeftClose,
  PanelLeftOpen,
  Search,
  SlidersHorizontal,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Kbd } from '@/components/ui/kbd'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { useServerInfo } from '@/lib/hooks/use-server-info'
import { useMinWidth } from '@/lib/hooks/use-min-width'
import { cn } from '@/lib/utils'
import { SETTINGS_UTILITY, resolveLocation } from '@/features/navigation/model'
import { useCommandStore } from '@/stores/command'
import {
  pendingCount,
  usePendingApprovals,
} from '@/features/governance/use-pending-approvals'
import { AreasEntry } from './areas-entry'
import { inSettingsArea } from './shell-destinations'
import { BrandMark, Wordmark } from './brand'
import { EngineStatus } from './engine-status'
import { JourneyNav } from './journey-nav'
import { SidebarFavorites } from './personal-navigation'
import { SidebarAreas } from './sidebar'
import { RAIL_ITEM_CLASS } from './shell-row'
import type { SidebarMode } from './sidebar-mode'
import { useSessionRail } from './use-session-rail'
import { TenantSwitcher } from './tenant-switcher'
import { ThemeToggle } from './theme-toggle'
import { UserMenu } from './user-menu'
import { WorkspaceSwitcher } from './workspace-switcher'

/** The sidebar counts that ask for a person. */
const ATTENTION_ALWAYS: readonly string[] = ['approvals']

const QUIET_ICON_CLASS =
  'grid size-7 shrink-0 place-items-center rounded-ctl text-text-2 outline-none hover:bg-hover hover:text-text focus-visible:ring-2 focus-visible:ring-focus [&_svg]:size-4'

/** An icon button of the rail, named for a screen reader and shown in a tooltip. */
function RailButton({
  label,
  onClick,
  shortcut,
  children,
}: {
  label: string
  onClick: () => void
  shortcut?: string
  children: ReactNode
}) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <button
          type="button"
          onClick={onClick}
          aria-label={label}
          aria-keyshortcuts={shortcut}
          className={RAIL_ITEM_CLASS}
        >
          {children}
        </button>
      </TooltipTrigger>
      <TooltipContent side="right">{label}</TooltipContent>
    </Tooltip>
  )
}

export function AppSidebar({
  mode,
  onToggle,
  areasOpen,
  onOpenAreas,
  overlay = false,
  onNavigate,
}: {
  mode: SidebarMode
  /** Fold to the rail, or unfold it (the caller decides what unfolding means). */
  onToggle: () => void
  areasOpen: boolean
  onOpenAreas: () => void
  /** Drawn over a work page: a plain region, so the page keeps one Primary landmark. */
  overlay?: boolean
  /** A destination was chosen (the overlay closes on it). */
  onNavigate?: () => void
}) {
  const { t } = useTranslation(['nav', 'common'])
  const openPalette = useCommandStore((s) => s.setOpen)
  const info = useServerInfo()
  // The phone brand owns the identity below the sidebar's breakpoint.
  const version = useMinWidth(761) ? info.data?.version : undefined
  const approvals = pendingCount(usePendingApprovals().query.data)
  const rail = useSessionRail()
  const { needsYou, live } = rail.sessionCounts
  const counts: Record<string, number | string> = {}
  if (live) counts.sessions = live
  if (approvals) counts.approvals = approvals
  const attention = new Set(
    needsYou ? [...ATTENTION_ALWAYS, 'sessions'] : ATTENTION_ALWAYS,
  )
  // The footer's Settings stands for the System & settings area (administration, backups,
  // logs, the setup wizard, the API playground): its link stays current on all of them.
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const inSettings = inSettingsArea(resolveLocation(pathname))
  const Region = overlay ? 'div' : 'aside'
  const settingsLink = (
    <Link
      to={SETTINGS_UTILITY.path as never}
      onClick={onNavigate}
      aria-label={t('nav:items.settings')}
      activeOptions={{ exact: true }}
      aria-current={inSettings ? 'page' : undefined}
      className={mode === 'rail' ? RAIL_ITEM_CLASS : QUIET_ICON_CLASS}
    >
      <SlidersHorizontal aria-hidden />
    </Link>
  )

  if (mode === 'rail')
    return (
      <Region
        aria-label={t('common:a11y.primarySidebar')}
        data-sidebar-mode="rail"
        className="flex w-14 min-h-0 flex-col items-center gap-1 py-2.5"
      >
        <Link
          to={'/' as never}
          onClick={onNavigate}
          aria-label={t('nav:shell.brandHome')}
          className="grid size-10 shrink-0 place-items-center rounded-ctl text-text outline-none focus-visible:ring-2 focus-visible:ring-focus"
        >
          <BrandMark className="size-[22px]" />
        </Link>
        {version ? (
          <span className="max-w-full shrink-0 break-all px-0.5 text-center font-mono text-mono-s text-text-2">
            {version}
          </span>
        ) : null}
        <RailButton
          label={t('common:actions.expandSidebar')}
          shortcut="Control+B Meta+B"
          onClick={onToggle}
        >
          <PanelLeftOpen aria-hidden />
        </RailButton>
        <RailButton
          label={t('nav:shell.searchCommands')}
          shortcut="Control+K Meta+K"
          onClick={() => openPalette(true)}
        >
          <Search aria-hidden />
        </RailButton>
        <div className="my-1 h-px w-6 shrink-0 bg-line" aria-hidden />
        <JourneyNav variant="rail" counts={counts} attention={attention} />
        <div className="flex-1" />
        <AreasEntry open={areasOpen} onOpen={onOpenAreas} />
        <Tooltip>
          <TooltipTrigger asChild>{settingsLink}</TooltipTrigger>
          <TooltipContent side="right">
            {t('nav:items.settings')}
          </TooltipContent>
        </Tooltip>
        <UserMenu />
      </Region>
    )

  return (
    <Region
      aria-label={t('common:a11y.primarySidebar')}
      data-sidebar-mode="full"
      className={cn(
        'flex w-60 min-h-0 flex-col gap-1.5 py-2.5 pr-2 pl-2.5',
        overlay && 'h-full',
      )}
    >
      <div className="flex h-9 shrink-0 items-center gap-2 px-1.5">
        <Link
          to={'/' as never}
          onClick={onNavigate}
          aria-label={t('nav:shell.brandHome')}
          className="rounded-ctl outline-none focus-visible:ring-2 focus-visible:ring-focus"
        >
          <Wordmark className="text-text" />
        </Link>
        {version ? (
          <span className="ml-auto truncate font-mono text-mono-s text-text-3">
            {version}
          </span>
        ) : null}
        <button
          type="button"
          onClick={onToggle}
          aria-label={t('common:actions.collapseSidebar')}
          aria-keyshortcuts="Control+B Meta+B"
          className={cn(QUIET_ICON_CLASS, !version && 'ml-auto')}
        >
          <PanelLeftClose aria-hidden />
        </button>
      </div>

      <div className="flex shrink-0 flex-col gap-1">
        <TenantSwitcher className="w-full max-w-none justify-start" />
        <WorkspaceSwitcher variant="card" />
      </div>

      <button
        type="button"
        onClick={() => openPalette(true)}
        aria-keyshortcuts="Control+K Meta+K"
        className="mt-1 mb-1 inline-flex h-8 w-full shrink-0 items-center gap-2 rounded-ctl bg-hover px-2.5 text-[13px] leading-5 text-text-2 outline-none hover:bg-active hover:text-text focus-visible:ring-2 focus-visible:ring-focus"
      >
        <Search aria-hidden className="size-3.5 shrink-0" />
        <span className="min-w-0 flex-1 truncate text-left">
          {t('nav:shell.searchCommands')}
        </span>
        <Kbd>⌘K</Kbd>
      </button>

      <JourneyNav
        counts={counts}
        attention={attention}
        onNavigate={onNavigate}
      />

      {/* Favorites and the areas scroll within the sidebar; the pins above, and the
          search and the footer, stay where they are. */}
      <div className="flex min-h-0 flex-1 flex-col gap-1 overflow-y-auto pr-0.5 [scrollbar-width:thin]">
        <SidebarFavorites onNavigate={onNavigate} />
        <SidebarAreas onNavigate={onNavigate} />
      </div>

      <div className="flex shrink-0 items-center gap-1 border-t border-line pt-2">
        <EngineStatus />
        <ThemeToggle />
        {settingsLink}
        <UserMenu />
      </div>
    </Region>
  )
}
