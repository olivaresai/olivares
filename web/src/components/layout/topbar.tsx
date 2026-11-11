// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useRouterState } from '@tanstack/react-router'
import { Search } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { ShellHeaderSlot } from '@/components/ui/page-actions'
import { Kbd } from '@/components/ui/kbd'
import { chordHint } from '@/lib/keybindings/model'
import { PALETTE_KEYS } from '@/lib/keybindings/table'
import { useCommandStore } from '@/stores/command'
import { BrandMark } from './brand'
import { topBarLabel } from './document-title'
import { NotificationBell } from './notification-bell'
import { SidePanelToggle } from './side-panel'
import { TopbarSearchSlot } from './topbar-search'
import { DestinationSections } from './destination-sections'
import { currentPlace, placeById } from '@/features/navigation/places'

/**
 * THE HEADER BAR: one flat 52 px row with the place's name, its pages, search with its key
 * (⌘K on a Mac, Ctrl+K elsewhere; the Ledger's query on the Ledger) and notifications. The
 * places live in the left navigation only; below 761 px the bar opens with the mark and the
 * phone bar's More opens the navigation as a drawer. The version, the engine, the stops and
 * Stop all live in the status bar (status-bar.tsx); the theme and settings in the account
 * menu. Below 1101 px search folds to its icon; below 761 px the bar wraps and the place's
 * pages take a second row. Its hairline shows once a document scrolls under it (index.css).
 *
 * The side panel's toggle shows only on a page that declares a panel (side-panel.tsx).
 */
export function Topbar() {
  const { t } = useTranslation(['nav', 'common'])
  const setCommandOpen = useCommandStore((s) => s.setOpen)
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  // The top bar names the place; the page's heading names the page.
  const label = topBarLabel(t, pathname)
  const place = currentPlace(pathname)
  const Icon = place ? placeById(place).icon : Search

  return (
    <header
      data-slot="topbar"
      className="flex h-[52px] min-h-[52px] shrink-0 flex-nowrap items-center gap-2.5 border-b border-line bg-canvas pr-3.5 pl-5 max-[760px]:h-auto max-[760px]:flex-wrap max-[760px]:px-4 print:hidden"
    >
      {/* On a phone there is no sidebar: the bar opens with the mark, a decoration. */}
      <BrandMark className="size-6 shrink-0 text-text min-[761px]:hidden" />
      <Icon
        aria-hidden
        className="size-4 shrink-0 text-text-2 max-[760px]:hidden"
      />
      <ShellHeaderSlot part="title" fallback={label} />

      <DestinationSections />
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
              className="inline-flex h-9 w-50 min-[761px]:hidden shrink-0 items-center gap-2 rounded-full bg-hover pr-2 pl-3 text-body text-text-2 hover:bg-active hover:text-text active:bg-[var(--pressed)] max-[1100px]:w-9 max-[1100px]:justify-center max-[1100px]:p-0 max-[760px]:size-11"
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
        <ShellHeaderSlot part="actions" />
        <NotificationBell />
        <SidePanelToggle />
      </div>
    </header>
  )
}
