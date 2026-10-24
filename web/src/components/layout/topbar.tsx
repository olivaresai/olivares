// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Search } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Kbd } from '@/components/ui/kbd'
import {
  pendingCount,
  usePendingApprovals,
} from '@/features/governance/use-pending-approvals'
import { chordHint } from '@/lib/keybindings/model'
import { KEYBINDINGS } from '@/lib/keybindings/table'
import { useCommandStore } from '@/stores/command'
import { NotificationBell } from './notification-bell'
import { PlacesNav } from './places-nav'
import { ScopeSwitcher } from './scope-switcher'
import { SidePanelToggle } from './side-panel'
import { UserMenu } from './user-menu'
import { useSessionRail } from './use-session-rail'

/** The palette's chord, as the table declares it (Mod+k): no rule, no hint. */
const PALETTE_KEYS = KEYBINDINGS.find(
  (rule) => rule.command === 'palette.open',
)?.keys

/** The place counts that always ask for a person. */
const ATTENTION_ALWAYS: readonly string[] = ['rules']

/**
 * THE TOP BAR (console Estate #2544, slimmed in #3161): one 56 px row and no sidebar.
 * The scope (organization and workspace in one control), the places (PlacesNav), then
 * search with its key (⌘K on a Mac, Ctrl+K elsewhere), notifications and the account.
 * Nothing else: the version, the engine, the stops and Stop all live in the status bar
 * (status-bar.tsx); the theme, favorites and settings in the account menu. "All areas"
 * (every other page) opens from the account menu and ⌘K. Below 1101 px search folds to
 * its icon; the places fold to theirs when the room the bar gives them runs out
 * (PlacesNav measures the places box); below 1024 px the scope moves into All areas;
 * below 761 px the places move to the phone bar.
 *
 * The side panel's toggle shows only on a page that declares a panel (side-panel.tsx).
 */
export function Topbar() {
  const { t } = useTranslation(['nav', 'common'])
  const setCommandOpen = useCommandStore((s) => s.setOpen)
  const rulesWaiting = pendingCount(usePendingApprovals().query.data)
  // The status bar's count, from the same read: every session that has not ended, idle
  // ones too, so the badge and "N sessions open" never say two numbers (#3161).
  const { needsYou, running } = useSessionRail().sessionCounts
  const counts: Record<string, number | string> = {}
  if (running) counts.sessions = running
  if (rulesWaiting) counts.rules = rulesWaiting
  const attention = new Set(
    needsYou ? [...ATTENTION_ALWAYS, 'sessions'] : ATTENTION_ALWAYS,
  )

  return (
    <header
      data-slot="topbar"
      className="flex h-14 min-h-14 shrink-0 flex-nowrap items-center gap-2 border-b border-line bg-canvas px-4 max-[760px]:px-3 print:hidden"
    >
      {/* Below 1024 px the scope leaves the bar for All areas, which carries the same
          choices there: the places and search keep their room. */}
      <div
        data-slot="topbar-scope"
        className="flex min-w-0 shrink items-center max-[1023px]:hidden"
      >
        <ScopeSwitcher />
      </div>

      {/* PlacesNav measures this box: the places fold on the room it gets, whatever
          else takes the room. Below the 16rem floor the scope control gives up its
          own room first (down to its minimum, issue #2764's order), so the floor is
          gated to the viewport band the scope exists in: below 1024 px there is no
          switcher left to yield and a floor would push the actions out of the bar. */}
      <div className="flex min-w-0 flex-1 items-center max-[760px]:hidden min-[1024px]:min-w-[16rem]">
        <PlacesNav counts={counts} attention={attention} />
      </div>

      <div className="ml-auto flex shrink-0 items-center gap-1.5">
        <button
          type="button"
          onClick={() => setCommandOpen(true)}
          aria-label={t('nav:shell.searchCommands')}
          title={t('nav:shell.searchCommands')}
          aria-keyshortcuts="Control+K Meta+K"
          className="inline-flex h-9 w-50 shrink-0 items-center gap-2 rounded-full bg-hover pr-2 pl-3 text-body text-text-2 outline-none hover:bg-active hover:text-text focus-visible:ring-2 focus-visible:ring-focus max-[1100px]:w-9 max-[1100px]:justify-center max-[1100px]:p-0"
        >
          <Search aria-hidden className="size-4 shrink-0" />
          <span
            aria-hidden
            className="min-w-0 flex-1 truncate text-left max-[1100px]:hidden"
          >
            {t('nav:shell.search')}
          </span>
          {PALETTE_KEYS ? (
            <Kbd className="max-[1100px]:hidden">{chordHint(PALETTE_KEYS)}</Kbd>
          ) : null}
        </button>
        <NotificationBell />
        <SidePanelToggle />
        <UserMenu />
      </div>
    </header>
  )
}
