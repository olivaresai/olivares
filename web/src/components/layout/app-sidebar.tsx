// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SIDEBAR (redesign §3.2): a place to work, not a directory.
//
//   brand + version
//   organization and workspace
//   New session (N) · Search and commands (Ctrl/⌘ K)
//   journeys: Home · Sessions · Workspaces · AI tools · Deploy     ← registry ids
//   sessions: Needs you · Working · Earlier                         ← live reads
//   All areas (9)                                                   ← NAV_AREAS directory
//   footer: engine state · theme · settings · account
//
// 272 px wide, 248 px at or below 1360 px; below 761 px the phone bar replaces it.
import { Link } from '@tanstack/react-router'
import { Plus, Search, SlidersHorizontal } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Kbd } from '@/components/ui/kbd'
import { useServerInfo } from '@/lib/hooks/use-server-info'
import { SETTINGS_UTILITY } from '@/features/navigation/model'
import { useCommandStore } from '@/stores/command'
import { AreasEntry } from './areas-entry'
import { Wordmark } from './brand'
import { EngineStatus } from './engine-status'
import { JourneyNav } from './journey-nav'
import { useNewSession } from './new-session'
import { SessionRailView } from './session-rail'
import { TenantSwitcher } from './tenant-switcher'
import { ThemeToggle } from './theme-toggle'
import { useSessionRail } from './use-session-rail'
import { UserMenu } from './user-menu'
import { WorkspaceSwitcher } from './workspace-switcher'

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
  const newSession = useNewSession()
  const rail = useSessionRail()
  const live = rail.groups
    .filter((g) => g.id !== 'earlier')
    .reduce((n, g) => n + g.rows.length, 0)

  return (
    <aside
      aria-label={t('common:a11y.primarySidebar')}
      className="flex w-[272px] min-h-0 flex-col gap-1.5 py-2.5 pr-2.5 pl-3 max-[1360px]:w-[248px]"
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

      <div className="mt-1 mb-0.5 flex shrink-0 gap-1.5">
        <button
          type="button"
          onClick={newSession}
          className="inline-flex h-7 min-w-0 flex-1 items-center gap-1.5 rounded-ctl border border-line-strong px-2.5 text-[13px] leading-5 font-medium text-text outline-none hover:bg-hover focus-visible:ring-2 focus-visible:ring-focus"
        >
          <Plus aria-hidden className="size-3.5 shrink-0" />
          <span className="min-w-0 truncate">{t('nav:shell.newSession')}</span>
          <Kbd className="ml-auto">N</Kbd>
        </button>
        <button
          type="button"
          onClick={() => openPalette(true)}
          aria-label={t('nav:shell.searchCommands')}
          aria-keyshortcuts="Control+K Meta+K"
          className="grid size-7 shrink-0 place-items-center rounded-ctl border border-line-strong text-text outline-none hover:bg-hover focus-visible:ring-2 focus-visible:ring-focus"
        >
          <Search aria-hidden className="size-3.5" />
        </button>
      </div>

      <JourneyNav counts={live ? { sessions: live } : undefined} />

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
          className={QUIET_ICON_CLASS}
        >
          <SlidersHorizontal aria-hidden />
        </Link>
        <UserMenu />
      </div>
    </aside>
  )
}
