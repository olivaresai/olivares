// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE ONE LEFT NAVIGATION (0.5, its 0.7 places): the places of features/navigation/places.ts.
// Sessions leads with the person's own sessions under it, then Files and Tools; Settings is at
// the foot with the administrative places one level under it (its More toggle), no group
// headers. From 761 px it is a 228 px column
// that folds to its 64 px icons and opens again; below that the same body opens as a drawer
// from the top bar's menu button, with the same items in the same order. There is no other
// navigation surface: no places in the top bar, no All areas sheet, no phone bar.
//
// Native links and buttons give the keyboard behavior: Tab walks the places, Enter opens one,
// and the fold button answers Enter and Space. Each link names its place even folded (the
// label stays for a screen reader and in the tooltip), and its count is part of its name.
import { Link, useRouterState } from '@tanstack/react-router'
import { ChevronDown, PanelLeftClose, PanelLeftOpen, X } from 'lucide-react'
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { CountBadge } from '@/components/ui/count-badge'
import { Sheet, SheetContent, SheetTitle } from '@/components/ui/sheet'
import {
  pendingCount,
  usePendingApprovals,
} from '@/features/governance/use-pending-approvals'
import { useViewAccess } from '@/features/navigation/authorization'
import {
  SessionStatusMark,
  useStatusWord,
} from '@/features/sessions/session-status'
import {
  FIRST_PLACES,
  currentPlace,
  settingsPlace,
  sidebarPlaces,
  type PlaceId,
  type SidebarPlace,
} from '@/features/navigation/places'
import { cn } from '@/lib/utils'
import { usePreferencesStore } from '@/stores/preferences'
import { BrandMark } from './brand'
import { ScopeSwitcher } from './scope-switcher'
import { RAIL_ROWS_PER_GROUP, type RailRow } from './session-rail-model'
import { useSessionRail, type SessionRailData } from './use-session-rail'

const ITEM_CLASS = cn(
  'relative flex min-h-[34px] min-w-0 items-center gap-2.5 rounded-md px-2.5 py-1.5 text-body font-medium text-text-2 outline-none',
  'hover:bg-hover hover:text-text focus-visible:ring-2 focus-visible:ring-focus',
  // The current place is the neutral tone step and full ink: orange marks what needs a person.
  'aria-[current=page]:bg-active aria-[current=page]:font-semibold aria-[current=page]:text-text',
  '[&_svg]:size-4 [&_svg]:shrink-0',
  'max-[760px]:min-h-11',
)

const ICON_BUTTON = cn(
  'inline-grid size-8 shrink-0 place-items-center rounded-md text-text-2 outline-none hover:bg-hover hover:text-text focus-visible:ring-2 focus-visible:ring-focus',
  'max-[760px]:size-11',
)

interface PlaceCount {
  count: number | string
  /** Amber: something here waits for the person. */
  needsYou: boolean
}

/** The counts a place carries, read from current state. Sessions: every session the list
 * shows that has not ended, the status bar's "N sessions open" from the same read, so the two
 * never say two numbers; amber when one of them needs the person. Rules: the requests
 * waiting for a decision. Zero draws nothing. */
function usePlaceCounts(
  sessionCounts: SessionRailData['sessionCounts'],
): Partial<Record<PlaceId, PlaceCount>> {
  const rules = pendingCount(usePendingApprovals().query.data)
  const { needsYou, open } = sessionCounts
  return {
    sessions: { count: open, needsYou: needsYou > 0 },
    ...(rules ? { rules: { count: rules, needsYou: true } } : {}),
  }
}

function PlaceLink({
  place,
  current,
  count,
  folded,
  onNavigate,
}: {
  place: SidebarPlace
  current: boolean
  count?: PlaceCount
  folded: boolean
  onNavigate?: () => void
}) {
  const { t } = useTranslation('nav')
  const Icon = place.icon
  const label = t(`places.${place.id}`)
  const attention =
    place.id === 'sessions' && count?.needsYou
      ? `, ${t('shell.rail.state.need')}`
      : ''
  return (
    <Link
      to={place.path as never}
      search={place.search as never}
      // The router marks a link current on its own whenever its path is a prefix of the
      // address (Now on every page, Settings under License). Exact: only on its own address,
      // which is always the place marked here.
      activeOptions={{ exact: true }}
      aria-current={current ? 'page' : undefined}
      title={folded ? label : undefined}
      onClick={onNavigate}
      data-place={place.id}
      className={cn(ITEM_CLASS, folded && 'justify-center px-0')}
    >
      <Icon aria-hidden />
      <span className={cn('min-w-0 flex-1 break-words', folded && 'sr-only')}>
        {label}
      </span>
      {count?.count ? (
        <>
          <span className="sr-only">, </span>
          <CountBadge
            count={count.count}
            attention={count.needsYou}
            className={cn(folded && 'absolute -top-1 -right-1')}
          />
          {attention ? <span className="sr-only">{attention}</span> : null}
        </>
      ) : null}
    </Link>
  )
}

/** The person's own sessions: the ones that need them first, then working, then earlier ones.
 * They are the rail's rows, so this list, the palette and the status bar agree. A handoff is
 * Now's, and a snoozed or archived session stays out until the person brings it back. */
const SIDEBAR_SESSION_GROUPS = new Set(['needsYou', 'working', 'earlier'])

function sidebarSessions(groups: SessionRailData['groups']): RailRow[] {
  return groups
    .filter((g) => SIDEBAR_SESSION_GROUPS.has(g.id))
    .flatMap((g) => g.rows)
    .filter((row) => row.kind === 'session')
    .slice(0, RAIL_ROWS_PER_GROUP)
}

function SessionLink({
  row,
  onNavigate,
}: {
  row: RailRow
  onNavigate?: () => void
}) {
  const { t } = useTranslation('nav')
  const word = useStatusWord()
  const title = row.title ?? `${t('shell.rail.session')} ${row.reference}`
  return (
    <Link
      to={row.to as never}
      search={row.search as never}
      onClick={onNavigate}
      className={cn(ITEM_CLASS, 'min-h-8 py-1 font-normal')}
    >
      <SessionStatusMark status={row.status} decorative />
      <span className="min-w-0 flex-1 truncate">{title}</span>
      <span className="sr-only">, {word(row.status)}</span>
    </Link>
  )
}

/** The navigation itself: brand, scope, the places and Settings. The column and the drawer
 * both render it, so the two cannot disagree about which places exist or which is current. */
function SidebarBody({
  folded = false,
  control,
  onNavigate,
}: {
  folded?: boolean
  /** The fold button in the column, the close button in the drawer. */
  control: ReactNode
  onNavigate?: () => void
}) {
  const { t } = useTranslation(['nav', 'common'])
  const { listed } = useViewAccess()
  const rail = useSessionRail()
  const counts = usePlaceCounts(rail.sessionCounts)
  const sessions = folded ? [] : sidebarSessions(rail.groups)
  const showAll = usePreferencesStore((s) => s.navAll)
  const setShowAll = usePreferencesStore((s) => s.setNavAll)
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const here = currentPlace(pathname)
  const places = sidebarPlaces(listed)
  const first = places.filter((p) => FIRST_PLACES.has(p.id))
  const more = places.filter((p) => !FIRST_PLACES.has(p.id))
  // Folded away never means out of reach of what needs the person: the place they are on
  // and a place with something waiting stay, under Settings.
  const moreShown = more.filter(
    (p) => showAll || p.id === here || counts[p.id]?.needsYou,
  )
  const list = (items: SidebarPlace[], className?: string) => (
    <ul className={cn('flex flex-col gap-0.5', className)}>
      {items.map((place) => (
        <li key={place.id}>
          <PlaceLink
            place={place}
            current={here === place.id}
            count={counts[place.id]}
            folded={folded}
            onNavigate={onNavigate}
          />
          {place.id === 'sessions' && sessions.length > 0 ? (
            <ul className="mt-0.5 flex flex-col gap-0.5 pl-3">
              {sessions.map((row) => (
                <li key={row.key}>
                  <SessionLink row={row} onNavigate={onNavigate} />
                </li>
              ))}
            </ul>
          ) : null}
        </li>
      ))}
    </ul>
  )
  return (
    <div className="flex h-full min-h-0 flex-col">
      <div
        className={cn(
          'flex h-14 shrink-0 items-center gap-2 border-b border-line px-3.5',
          folded && 'justify-center px-2',
        )}
      >
        {!folded && (
          <>
            <BrandMark className="size-6 shrink-0 text-text" />
            <span className="min-w-0 flex-1 truncate text-body font-semibold text-text">
              {t('common:app.name')}
            </span>
          </>
        )}
        {control}
      </div>

      {/* The organization and workspace this navigation works in, one control; the
          slot hides when there is nothing to choose (scope-switcher.tsx). */}
      {!folded && (
        <div
          data-slot="sidebar-scope"
          className="flex shrink-0 border-b border-line p-1.5 empty:hidden"
        >
          <ScopeSwitcher />
        </div>
      )}

      <div className="flex min-h-0 flex-1 flex-col gap-0.5 overflow-y-auto px-2 py-3">
        {first.length > 0 ? list(first) : null}
      </div>

      {/* Settings, and one level under it the administrative places: its More toggle opens
          them, indented. Closed, the place the person is on and a place with something
          waiting stay shown. */}
      <div className="flex max-h-[60%] shrink-0 flex-col gap-0.5 overflow-y-auto border-t border-line p-2">
        <div
          className={cn('flex gap-0.5', folded ? 'flex-col' : 'items-center')}
        >
          <div className="min-w-0 flex-1">
            <PlaceLink
              place={settingsPlace()}
              current={here === 'settings'}
              folded={folded}
              onNavigate={onNavigate}
            />
          </div>
          {more.length > 0 ? (
            <button
              type="button"
              aria-expanded={showAll}
              aria-label={t('nav:shell.more')}
              title={t('nav:shell.more')}
              onClick={() => setShowAll(!showAll)}
              className={
                folded ? cn(ITEM_CLASS, 'justify-center px-0') : ICON_BUTTON
              }
            >
              <ChevronDown
                aria-hidden
                className={cn('size-4', showAll && 'rotate-180')}
              />
            </button>
          ) : null}
        </div>
        {moreShown.length > 0 ? list(moreShown, cn(!folded && 'pl-3')) : null}
      </div>
    </div>
  )
}

/** The column, from 761 px: 228 px, folding to its 64 px icons and back. */
export function Sidebar() {
  const { t } = useTranslation(['nav', 'common'])
  const chosen = usePreferencesStore((s) => s.navCollapsed)
  const setFolded = usePreferencesStore((s) => s.setNavCollapsed)
  // Named on every page, as the drawer is; folding to icons is the person's own choice.
  const folded = chosen === true
  const label = t(folded ? 'nav:shell.expand' : 'nav:shell.collapse')
  return (
    <nav
      aria-label={t('common:a11y.mainNavigation')}
      data-slot="sidebar"
      data-folded={folded ? 'true' : undefined}
      className={cn(
        'flex min-h-0 shrink-0 flex-col border-r border-line bg-frame max-[760px]:hidden print:hidden',
        folded ? 'w-16' : 'w-[228px]',
      )}
    >
      <SidebarBody
        folded={folded}
        control={
          <button
            type="button"
            onClick={() => setFolded(!folded)}
            aria-label={label}
            aria-expanded={!folded}
            title={label}
            className={ICON_BUTTON}
          >
            {folded ? (
              <PanelLeftOpen aria-hidden className="size-4" />
            ) : (
              <PanelLeftClose aria-hidden className="size-4" />
            )}
          </button>
        }
      />
    </nav>
  )
}

/** Below 761 px: the same navigation as a drawer from the left. Choosing a place closes it;
 * the dialog primitive contains focus and returns it to the menu button. */
export function NavigationDrawer({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation(['nav', 'common'])
  const close = () => onOpenChange(false)
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent
        side="left"
        hideClose
        aria-describedby={undefined}
        className="w-72 max-w-[88vw] gap-0 bg-frame p-0"
      >
        <SheetTitle className="sr-only">
          {t('common:a11y.mainNavigation')}
        </SheetTitle>
        <nav
          aria-label={t('common:a11y.mainNavigation')}
          data-slot="navigation-drawer"
          className="h-full"
        >
          <SidebarBody
            onNavigate={close}
            control={
              <button
                type="button"
                onClick={close}
                aria-label={t('nav:shell.closeNavigation')}
                title={t('nav:shell.closeNavigation')}
                className={ICON_BUTTON}
              >
                <X aria-hidden className="size-4" />
              </button>
            }
          />
        </nav>
      </SheetContent>
    </Sheet>
  )
}
