// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE DESTINATION'S OWN SECTIONS (console remake 26.10): on a page that belongs to a
// destination spanning several views (Policies: Claude Code governance, routine policies,
// the inference proxy, data residency, red-teaming…), one row above the page moves between
// the members this principal may open. The members are registry views, named by their own
// nav labels and gated by the same check as the sidebar, so the row never offers a page
// the route guard would refuse. It is drawn as links, not tabs: each member is its own
// page, with its own tabs below its title.
//
// Not drawn on a section address (Approvals is the queue, not a page of Identity & access)
// nor on a work-frame route (Sessions), whose surface owns the whole viewport.
import { Link, useRouterState } from '@tanstack/react-router'
import { useLayoutEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { useViewAccess } from '@/features/navigation/authorization'
import { SETTINGS_UTILITY, resolveLocation } from '@/features/navigation/model'
import { frameFor } from './page-frames'
import {
  DESTINATION_MEMBERS,
  SETTINGS_DESTINATION,
  destinationOf,
  sectionAt,
  shellDestinations,
} from './shell-destinations'

interface Member {
  id: string
  path: string
}

export function DestinationSections() {
  const { t } = useTranslation('nav')
  const { navigable } = useViewAccess()
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const search = useRouterState({
    select: (s) => (s.location.search ?? {}) as Record<string, unknown>,
  })
  const row = useRef<HTMLElement>(null)
  // On a narrow screen the row scrolls: bring the current member into view, moving the
  // row only (never the page).
  useLayoutEffect(() => {
    const el = row.current
    const current = el?.querySelector<HTMLElement>('[aria-current="page"]')
    if (!el || !current) return
    const left = current.offsetLeft - el.offsetLeft
    if (
      left < el.scrollLeft ||
      left + current.offsetWidth > el.scrollLeft + el.clientWidth
    )
      el.scrollLeft = Math.max(0, left - 8)
  }, [pathname])

  if (frameFor(pathname) === 'work') return null
  const location = resolveLocation(pathname)
  let currentId: string
  let destination: string | null
  if (location.kind === 'settings') {
    currentId = SETTINGS_UTILITY.id
    destination = SETTINGS_DESTINATION
  } else if (location.kind === 'view') {
    if (sectionAt(location.view.id, search)) return null
    currentId = location.view.id
    destination = destinationOf(location.view.id)
  } else return null
  if (!destination) return null

  const members: Member[] = shellDestinations(
    DESTINATION_MEMBERS[destination],
    navigable,
  )
  // The footer's Settings is not a registry view: it leads its own row by hand.
  if (destination === SETTINGS_DESTINATION)
    members.unshift({ id: SETTINGS_UTILITY.id, path: SETTINGS_UTILITY.path })
  // One permitted member is just the page: no row to move between.
  if (members.length < 2) return null
  const label =
    destination === SETTINGS_DESTINATION
      ? t('items.settings')
      : t(`shell.journeys.${destination}`)
  return (
    <nav
      ref={row}
      aria-label={label}
      data-slot="destination-sections"
      className="-mx-1 mb-3 flex gap-1 overflow-x-auto px-1 pb-1 [scrollbar-width:none]"
    >
      {members.map((m) => (
        <Link
          key={m.id}
          to={m.path as never}
          aria-current={m.id === currentId ? 'page' : undefined}
          data-member={m.id}
          className="inline-flex h-7 shrink-0 items-center rounded-ctl px-2.5 text-caption font-medium whitespace-nowrap text-text-2 outline-none transition-colors duration-fast hover:bg-hover hover:text-text focus-visible:ring-2 focus-visible:ring-focus aria-[current=page]:bg-active aria-[current=page]:text-text"
        >
          {t(`items.${m.id}`)}
        </Link>
      ))}
    </nav>
  )
}
