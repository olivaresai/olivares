// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE PAGES BESIDE THIS ONE: one row above the page that moves between the pages of its
// own section (Policies: Claude Code policy, routine policies, the inference proxy), the same
// section All areas and the breadcrumb place it in. Under the footer's Settings the row is
// the whole System & settings area, led by Settings. The members are registry views, named by
// their own nav labels and gated by the same check as the sidebar, so the row never offers a
// page the route guard would refuse. It is drawn as links, not tabs: each member is its own
// page, with its own tabs below its title.
//
// Not drawn on a section address (Approvals is the queue, not a page of Identity & access)
// nor on a work-frame route (Sessions), whose surface owns the whole viewport.
//
// Drawn on the phone only (below 761 px, where there is no sidebar). From 761 px the
// sidebar lists the same pages in its areas, and a page with its own tab strip would show
// two strips.
import { Link, useRouterState } from '@tanstack/react-router'
import { useLayoutEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { useViewAccess } from '@/features/navigation/authorization'
import {
  SETTINGS_UTILITY,
  areaLabel,
  resolveLocation,
  sectionLabel,
} from '@/features/navigation/model'
import { frameFor } from './page-frames'
import { inSettingsArea, pagesBeside, sectionAt } from './shell-destinations'

export function DestinationSections() {
  const { t } = useTranslation('nav')
  const { listed } = useViewAccess()
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
  if (location.kind === 'view' && sectionAt(location.view.id, search))
    return null
  const members = pagesBeside(location, listed)
  // One permitted member is just the page: no row to move between.
  if (members.length < 2) return null
  // A deep-link detail (a recording's viewer) is read as its parent's page; the row of
  // the footer's Settings is named by its area, any other row by its section.
  let currentId: string = SETTINGS_UTILITY.id
  let label = areaLabel(t, SETTINGS_UTILITY.areaId)
  if (location.kind === 'view' && !inSettingsArea(location)) {
    const place = location.view.navigation
    if (place.kind === 'root') return null
    currentId = place.kind === 'detail' ? place.parentViewId : location.view.id
    label = sectionLabel(t, place.areaId, place.sectionId)
  } else if (location.kind === 'view') {
    currentId = location.view.id
  }
  return (
    <nav
      ref={row}
      aria-label={label}
      data-slot="destination-sections"
      className="-mx-1 mb-3 flex gap-1 overflow-x-auto px-1 pb-1 [scrollbar-width:none] min-[761px]:hidden"
    >
      {members.map((m) => (
        <Link
          key={m.id}
          to={m.path as never}
          // One current member, the page itself: the router would also mark every
          // member whose path is a prefix of this one (/communications under its inbox).
          activeOptions={{ exact: true }}
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
