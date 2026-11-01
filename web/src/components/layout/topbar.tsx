// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useRouterState } from '@tanstack/react-router'
import { Menu, Search } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Kbd } from '@/components/ui/kbd'
import { chordHint } from '@/lib/keybindings/model'
import { PALETTE_KEYS } from '@/lib/keybindings/table'
import { useCommandStore } from '@/stores/command'
import { pageLabel } from './document-title'
import { NotificationBell } from './notification-bell'
import { SidePanelToggle } from './side-panel'
import { TopbarSearchSlot } from './topbar-search'
import { UserMenu } from './user-menu'

/**
 * THE TOP BAR (0.5): one 56 px row with the page title, search with its key (⌘K on a Mac,
 * Ctrl+K elsewhere; the Ledger's query on the Ledger), notifications and the account. The
 * places live in the left navigation only; below 761 px the menu button opens it as a
 * drawer. The version, the engine, the stops and Stop all live in the status bar
 * (status-bar.tsx); the theme and settings in the account menu. Below 1101 px search folds
 * to its icon.
 *
 * The side panel's toggle shows only on a page that declares a panel (side-panel.tsx).
 */
export function Topbar({ onMenu }: { onMenu: () => void }) {
  const { t } = useTranslation(['nav', 'common'])
  const setCommandOpen = useCommandStore((s) => s.setOpen)
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const search = useRouterState({
    select: (s) => (s.location.search ?? {}) as Record<string, unknown>,
  })

  return (
    <header
      data-slot="topbar"
      className="flex h-14 min-h-14 shrink-0 flex-nowrap items-center gap-2 border-b border-line bg-canvas px-4 max-[760px]:px-2 print:hidden"
    >
      <button
        type="button"
        onClick={onMenu}
        aria-label={t('nav:shell.openNavigation')}
        title={t('nav:shell.openNavigation')}
        className="inline-grid size-11 shrink-0 place-items-center rounded-md text-text-2 outline-none hover:bg-hover hover:text-text focus-visible:ring-2 focus-visible:ring-focus min-[761px]:hidden"
      >
        <Menu aria-hidden className="size-5" />
      </button>
      <p
        data-slot="topbar-title"
        className="min-w-0 flex-1 truncate text-heading font-semibold text-text"
      >
        {pageLabel(t, pathname, search)}
      </p>

      <div className="ml-auto flex shrink-0 items-center gap-1.5">
        {/* A place whose list is a query (the Ledger) puts its own field here. */}
        <TopbarSearchSlot
          fallback={
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
              {/* The hint is drawn, not text, so the text on the button is a run of its
                  name (WCAG 2.5.3); aria-keyshortcuts gives the chord. */}
              {PALETTE_KEYS ? (
                <Kbd
                  aria-hidden
                  data-keys={chordHint(PALETTE_KEYS)}
                  className="before:content-[attr(data-keys)] max-[1100px]:hidden"
                />
              ) : null}
            </button>
          }
        />
        <NotificationBell />
        <SidePanelToggle />
        <UserMenu />
      </div>
    </header>
  )
}
