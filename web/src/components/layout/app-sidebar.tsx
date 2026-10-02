// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SIDEBAR (console remake 26.10, CONCEPT-IA): a place to work, not a directory.
//
//   brand + version
//   organization and workspace
//   Search and commands (Ctrl/⌘ K)
//   work: Now · Sessions · Work
//   Manage · Integrate · Secure                                     ← registry ids
//   favorites: the pages this user starred, saved on the engine
//   sessions: Needs you · Working · Earlier
//   All areas (9)                                                   ← NAV_AREAS directory
//   footer: engine state · theme · settings · account
//
// 236 px wide, 228 px at or below 1360 px; below 761 px the phone bar replaces it.
// New session stays one gesture away: N, ⌘K, and the composer on Now and Sessions.
import { Link, useRouterState } from '@tanstack/react-router'
import { Search, SlidersHorizontal } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Kbd } from '@/components/ui/kbd'
import { useServerInfo } from '@/lib/hooks/use-server-info'
import { SETTINGS_UTILITY, resolveLocation } from '@/features/navigation/model'
import { useCommandStore } from '@/stores/command'
import {
  pendingCount,
  usePendingApprovals,
} from '@/features/governance/use-pending-approvals'
import { AreasEntry } from './areas-entry'
import { SETTINGS_DESTINATION, destinationOf } from './shell-destinations'
import { Wordmark } from './brand'
import { EngineStatus } from './engine-status'
import { JourneyNav } from './journey-nav'
import { SidebarFavorites } from './personal-navigation'
import { SessionRailView } from './session-rail'
import { useSessionRail } from './use-session-rail'
import { TenantSwitcher } from './tenant-switcher'
import { ThemeToggle } from './theme-toggle'
import { UserMenu } from './user-menu'
import { WorkspaceSwitcher } from './workspace-switcher'

/** The sidebar counts that ask for a person. */
const ATTENTION: ReadonlySet<string> = new Set(['approvals'])

const QUIET_ICON_CLASS =
  'grid size-7 shrink-0 place-items-center rounded-ctl text-text-2 outline-none hover:bg-hover hover:text-text focus-visible:ring-2 focus-visible:ring-focus [&_svg]:size-4'

export function AppSidebar({
  areasOpen,
  onOpenAreas,
}: {
  areasOpen: boolean
  onOpenAreas: () => void
}) {
  const { t } = useTranslation(['nav', 'common'])
  const version = useServerInfo().data?.version
  const openPalette = useCommandStore((s) => s.setOpen)
  const approvals = pendingCount(usePendingApprovals().query.data)
  const rail = useSessionRail()
  const live = rail.groups
    .filter((g) => g.id !== 'earlier')
    .reduce((n, g) => n + g.rows.length, 0)
  // Administration, backups, logs, the setup wizard and the API playground belong to the
  // footer's Settings: its link stays current on them.
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const here = resolveLocation(pathname)
  const inSettings =
    here.kind === 'view' && destinationOf(here.view.id) === SETTINGS_DESTINATION

  return (
    <aside
      aria-label={t('common:a11y.primarySidebar')}
      className="flex w-[236px] min-h-0 flex-col gap-1.5 py-2.5 pr-2 pl-2.5"
    >
      <div className="flex h-9 shrink-0 items-center gap-2.5 px-1.5">
        <Link
          to={'/' as never}
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
      </div>

      <div className="flex shrink-0 flex-col gap-1">
        <TenantSwitcher className="w-full max-w-none justify-start" />
        <WorkspaceSwitcher variant="card" />
      </div>

      <button
        type="button"
        onClick={() => openPalette(true)}
        aria-keyshortcuts="Control+K Meta+K"
        className="mt-1 mb-1 inline-flex h-7 w-full shrink-0 items-center gap-2 rounded-ctl border border-line-strong px-2.5 text-[13px] leading-5 text-text-2 outline-none hover:bg-hover hover:text-text focus-visible:ring-2 focus-visible:ring-focus"
      >
        <Search aria-hidden className="size-3.5 shrink-0" />
        <span className="min-w-0 flex-1 truncate text-left">
          {t('nav:shell.searchCommands')}
        </span>
        <Kbd>⌘K</Kbd>
      </button>

      <JourneyNav
        counts={approvals ? { approvals, sessions: live } : { sessions: live }}
        attention={ATTENTION}
      />

      <SidebarFavorites />
      {rail.visible ? (
        <SessionRailView groups={rail.groups} status={rail.status} />
      ) : (
        <div className="flex-1" />
      )}

      <AreasEntry open={areasOpen} onOpen={onOpenAreas} />

      <div className="flex shrink-0 items-center gap-1 border-t border-line pt-2">
        <EngineStatus />
        <ThemeToggle />
        <Link
          to={SETTINGS_UTILITY.path as never}
          aria-label={t('nav:items.settings')}
          activeProps={{ 'aria-current': 'page' }}
          aria-current={inSettings ? 'page' : undefined}
          className={QUIET_ICON_CLASS}
        >
          <SlidersHorizontal aria-hidden />
        </Link>
        <UserMenu />
      </div>
    </aside>
  )
}
