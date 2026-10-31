// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE ONE LEFT NAVIGATION (0.5): the places of features/navigation/places.ts in four groups
// (Work, Tools, Control, Admin) and Settings at the foot. From 761 px it is a 228 px column
// that folds to its 64 px icons and opens again; below that the same body opens as a drawer
// from the top bar's menu button, with the same items in the same order. There is no other
// navigation surface: no places in the top bar, no All areas sheet, no phone bar.
//
// Native links and buttons give the keyboard behavior: Tab walks the places, Enter opens one,
// and the fold button answers Enter and Space. Each link names its place even folded (the
// label stays for a screen reader and in the tooltip), and its count is part of its name.
import { Link, useRouterState } from '@tanstack/react-router'
import { PanelLeftClose, PanelLeftOpen, X } from 'lucide-react'
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
  currentPlace,
  settingsPlace,
  sidebarGroups,
  type PlaceId,
  type SidebarPlace,
} from '@/features/navigation/places'
import { cn } from '@/lib/utils'
import { usePreferencesStore } from '@/stores/preferences'
import { BrandMark } from './brand'
import { ScopeSwitcher } from './scope-switcher'
import { useSessionRail } from './use-session-rail'

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
 * never say two numbers (#3161); amber when one of them needs the person. Rules: the requests
 * waiting for a decision. Zero draws nothing. */
function usePlaceCounts(): Partial<Record<PlaceId, PlaceCount>> {
  const rules = pendingCount(usePendingApprovals().query.data)
  const { needsYou, open } = useSessionRail().sessionCounts
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

/** The navigation itself: brand, scope, the groups and Settings. The column and the drawer
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
  const counts = usePlaceCounts()
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const search = useRouterState({
    select: (s) => (s.location.search ?? {}) as Record<string, unknown>,
  })
  const here = currentPlace(pathname, search)
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

      {/* The organization and workspace this navigation works in, one control. */}
      {!folded && (
        <div
          data-slot="sidebar-scope"
          className="flex shrink-0 border-b border-line p-1.5"
        >
          <ScopeSwitcher />
        </div>
      )}

      <div className="min-h-0 flex-1 overflow-y-auto px-2 py-3">
        {sidebarGroups(listed).map((group) => (
          <section
            key={group.id}
            aria-label={t(`nav:groups.${group.id}`)}
            className="mb-3.5"
          >
            <h2
              aria-hidden
              className={cn(
                'px-2.5 pb-1 text-[10px] font-semibold tracking-[0.07em] text-text-3 uppercase',
                folded && 'sr-only',
              )}
            >
              {t(`nav:groups.${group.id}`)}
            </h2>
            <ul className="flex flex-col gap-0.5">
              {group.places.map((place) => (
                <li key={place.id}>
                  <PlaceLink
                    place={place}
                    current={here === place.id}
                    count={counts[place.id]}
                    folded={folded}
                    onNavigate={onNavigate}
                  />
                </li>
              ))}
            </ul>
          </section>
        ))}
      </div>

      <div className="shrink-0 border-t border-line p-2">
        <PlaceLink
          place={settingsPlace()}
          current={here === 'settings'}
          folded={folded}
          onNavigate={onNavigate}
        />
      </div>
    </div>
  )
}

/** The column, from 761 px: 228 px, folding to its 64 px icons and back. */
export function Sidebar() {
  const { t } = useTranslation(['nav', 'common'])
  const folded = usePreferencesStore((s) => s.navCollapsed)
  const setFolded = usePreferencesStore((s) => s.setNavCollapsed)
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
