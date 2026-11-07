// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE PAGES OF THIS PLACE: one row above the page that moves between the pages of the place
// the left navigation marks (Tools: AI tools, Models, Extensions, Knowledge). Under Settings
// the row is led by Settings. The members are registry views, named by their own nav labels
// and gated by the same check as the navigation, so the row never offers a page the route
// guard would refuse. It is drawn as links, not tabs: each member is its own page, with its
// own tabs below its title. At most five pages are drawn; the rest of the place sits in one
// More menu at the row's end, in place order (placePages).
//
// Not drawn on a work-frame route (Sessions), whose surface owns the whole viewport: there
// the list's heading opens the same pages (PagesMenu, an internal design note (not shipped)).
import { Link, useRouterState } from '@tanstack/react-router'
import { Check, ChevronDown } from 'lucide-react'
import { useLayoutEffect, useRef, type ReactElement } from 'react'
import { useTranslation } from 'react-i18next'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { useViewAccess } from '@/features/navigation/authorization'
import { resolveLocation, viewById } from '@/features/navigation/model'
import {
  currentPlace,
  placePages,
  type PageLink,
} from '@/features/navigation/places'
import { frameFor } from './page-frames'

const MEMBER =
  'inline-flex h-7 shrink-0 items-center rounded-ctl px-2.5 text-caption font-medium whitespace-nowrap text-text-2 outline-none transition-colors duration-fast hover:bg-hover hover:text-text focus-visible:ring-2 focus-visible:ring-focus aria-[current=page]:bg-active aria-[current=page]:text-text'

/**
 * Pages of a place behind one native menu button: the row's More, and the Sessions pages on
 * the work surface. The menu (Radix) opens with Enter or Space, moves with the arrows and
 * closes with Escape, back on its button. The current page is marked and checked.
 */
export function PagesMenu({
  pages,
  currentId,
  children,
}: {
  pages: readonly PageLink[]
  currentId: string
  /** The button that opens the menu. */
  children: ReactElement
}) {
  const { t } = useTranslation('nav')
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>{children}</DropdownMenuTrigger>
      <DropdownMenuContent align="start">
        {pages.map((p) => (
          <DropdownMenuItem key={p.id} asChild>
            <Link
              to={p.path as never}
              activeOptions={{ exact: true }}
              aria-current={p.id === currentId ? 'page' : undefined}
              data-member={p.id}
              className="aria-[current=page]:font-medium aria-[current=page]:text-text"
            >
              {t(`items.${p.id}`)}
              {p.id === currentId ? (
                <Check aria-hidden="true" className="ml-auto" />
              ) : null}
            </Link>
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

export function DestinationSections() {
  const { t } = useTranslation('nav')
  const { listed } = useViewAccess()
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const row = useRef<HTMLElement>(null)
  // On a narrow screen the row scrolls: bring the current member into view, moving the
  // row only (never the page).
  useLayoutEffect(() => {
    const el = row.current
    const current = el?.querySelector<HTMLElement>(
      '[aria-current="page"], [data-current]',
    )
    if (!el || !current) return
    const left = current.offsetLeft - el.offsetLeft
    if (
      left < el.scrollLeft ||
      left + current.offsetWidth > el.scrollLeft + el.clientWidth
    )
      el.scrollLeft = Math.max(0, left - 8)
  }, [pathname])

  if (frameFor(pathname) === 'work') return null
  const place = currentPlace(pathname)
  if (!place) return null
  const members = placePages(place, listed)
  // One permitted member is just the page: no row to move between.
  if (members.length < 2) return null
  const location = resolveLocation(pathname)
  // A deep-link detail (a recording's viewer) reads as its parent's page; a door folded
  // into a member (/provider-profiles into AI tools) reads as its room.
  let currentId = 'settings'
  if (location.kind === 'view') {
    const view = location.parent ?? location.view
    const room = view.doorTo ? viewById(view.doorTo) : undefined
    currentId =
      room && members.some((m) => m.id === room.id) ? room.id : view.id
  }
  const more = members.filter((m) => m.more)
  return (
    <nav
      ref={row}
      aria-label={t(`places.${place}`)}
      data-slot="destination-sections"
      className="-mx-1 mb-3 flex gap-1 overflow-x-auto px-1 pb-1 [scrollbar-width:none]"
    >
      {members
        .filter((m) => !m.more)
        .map((m) => (
          <Link
            key={m.id}
            to={m.path as never}
            // One current member, the page itself: the router would also mark every
            // member whose path is a prefix of this one (/communications under its inbox).
            activeOptions={{ exact: true }}
            aria-current={m.id === currentId ? 'page' : undefined}
            data-member={m.id}
            className={MEMBER}
          >
            {t(`items.${m.id}`)}
          </Link>
        ))}
      {more.length > 0 ? (
        <PagesMenu pages={more} currentId={currentId}>
          <button
            type="button"
            // The current page is inside: the button wears the current member's look.
            data-current={more.some((m) => m.id === currentId) ? '' : undefined}
            className={`${MEMBER} gap-1 data-[current]:bg-active data-[current]:text-text data-[state=open]:bg-hover`}
          >
            {t('shell.more')}
            <ChevronDown aria-hidden="true" className="size-3.5" />
          </button>
        </PagesMenu>
      ) : null}
    </nav>
  )
}
