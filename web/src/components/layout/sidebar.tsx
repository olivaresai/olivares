// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// One navigation column: Overview, work, governance, administration, and Settings.
// Settings replaces the places with its sections; a phone uses the same navigation in a drawer.
import { Link, useRouterState } from '@tanstack/react-router'
import {
  ArrowLeft,
  House,
  MessageSquare,
  MoreHorizontal,
  Plus,
  Search,
  Wrench,
  PanelLeftClose,
  PanelLeftOpen,
  X,
} from 'lucide-react'
import type { ReactNode, Ref, RefObject } from 'react'
import { SettingsSectionNavigation } from './settings-section-navigation'
import { Kbd } from '@/components/ui/kbd'
import { chordHint } from '@/lib/keybindings/model'
import { PALETTE_KEYS } from '@/lib/keybindings/table'
import { useMinWidth } from '@/lib/hooks/use-min-width'
import { UserMenu } from './user-menu'
import { useCommandStore } from '@/stores/command'
import { useNewSession } from './new-session'
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
  currentPlace,
  settingsPlace,
  sidebarPlaces,
  type PlaceId,
  type SidebarPlace,
} from '@/features/navigation/places'
import { cn } from '@/lib/utils'
import { usePreferencesStore } from '@/stores/preferences'
import { BrandMark, Wordmark } from './brand'
import { ScopeSwitcher } from './scope-switcher'
import { type RailRow } from './session-rail-model'
import { useSessionRail, type SessionRailData } from './use-session-rail'

// One state layer: hover, pressed, current; the focus ring is the base outline, drawn inside
// the row. Nothing scales or moves when pressed.
const ITEM_CLASS = cn(
  'relative flex min-h-[34px] min-w-0 items-center gap-[11px] rounded-md px-2.5 py-1.5 text-body font-medium text-text-2',
  'hover:bg-hover hover:text-text active:bg-[var(--pressed)] focus-visible:-outline-offset-2',
  // The current place is the neutral tone step, full ink and bold: the fill is light on
  // purpose, so the weight tells it apart too. Orange marks what needs a person.
  'aria-[current=page]:bg-active aria-[current=page]:font-bold aria-[current=page]:text-text',
  '[&_svg]:size-4 [&_svg]:shrink-0',
  'max-[760px]:min-h-11',
)

const ICON_BUTTON = cn(
  'inline-grid size-8 shrink-0 place-items-center rounded-md text-text-2 hover:bg-hover hover:text-text active:bg-[var(--pressed)]',
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
      className={cn(
        ITEM_CLASS,
        folded && 'mx-auto size-10 justify-center px-0',
      )}
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
            compact={folded}
            className={cn(folded && 'absolute top-px -right-[3px]')}
          />
          {attention ? <span className="sr-only">{attention}</span> : null}
        </>
      ) : null}
    </Link>
  )
}

/** The person's own sessions: the ones that need them first, then the working ones. They are
 * the rail's rows, so this list, the palette and the status bar agree. An idle session from
 * earlier is the list's; a handoff is Now's, and a snoozed or archived session stays out until
 * the person brings it back. */
const SIDEBAR_SESSION_GROUPS = new Set(['needsYou', 'working'])

function sidebarSessions(groups: SessionRailData['groups']): RailRow[] {
  return groups
    .filter((g) => SIDEBAR_SESSION_GROUPS.has(g.id))
    .flatMap((g) => g.rows)
    .filter((row) => row.kind === 'session')
    .slice(0, 5)
}

function SessionLink({
  row,
  current,
  onNavigate,
}: {
  row: RailRow
  /** The row's own address is the page: the session open on the work surface. */
  current: boolean
  onNavigate?: () => void
}) {
  const { t } = useTranslation('nav')
  const word = useStatusWord()
  const title = row.title ?? `${t('shell.rail.session')} ${row.reference}`
  return (
    <Link
      to={row.to as never}
      search={row.search as never}
      activeOptions={{ exact: true, includeSearch: true }}
      aria-current={current ? 'page' : undefined}
      onClick={onNavigate}
      className={cn(
        ITEM_CLASS,
        'min-h-[30px] gap-[9px] py-1 pr-2.5 pl-[9px] font-normal aria-[current=page]:font-semibold',
      )}
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
  onSearchSettings,
}: {
  folded?: boolean
  /** The fold button in the column, the close button in the drawer; none on the fixed rail. */
  control?: ReactNode
  onNavigate?: () => void
  /** Folded in Settings: opens the column on the settings search; none on the fixed rail. */
  onSearchSettings?: () => void
}) {
  const { t } = useTranslation(['nav', 'common'])
  const { listed } = useViewAccess()
  const rail = useSessionRail()
  const counts = usePlaceCounts(rail.sessionCounts)
  const sessions = folded ? [] : sidebarSessions(rail.groups)
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const search = useRouterState({
    select: (s) => s.location.search as Record<string, unknown>,
  })
  const here = currentPlace(pathname)
  const places = sidebarPlaces(listed)
  const settings = here === 'settings'
  const isOpen = (row: RailRow) =>
    pathname === row.to &&
    Object.entries(row.search).every(([k, v]) => String(search[k] ?? '') === v)
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
            <ul className="mt-0.5 mb-1.5 flex flex-col gap-px pl-[27px]">
              {sessions.map((row) => (
                <li key={row.key}>
                  <SessionLink
                    row={row}
                    current={isOpen(row)}
                    onNavigate={onNavigate}
                  />
                </li>
              ))}
              <li>
                <Link
                  to={'/sessions' as never}
                  onClick={onNavigate}
                  className="block rounded-md py-1 pr-2.5 pl-[9px] text-caption text-text-3 hover:text-text focus-visible:-outline-offset-2"
                >
                  {t('nav:shell.rail.seeAll', {
                    count: rail.sessionCounts.open,
                  })}
                </Link>
              </li>
            </ul>
          ) : null}
        </li>
      ))}
    </ul>
  )
  return (
    <div className="flex h-full min-h-0 flex-col">
      {/* The head: the mark and the traced name (never live text), the fold at the end.
          Folded, the rail opens with the mark alone and the unfold button under it. */}
      <div
        data-slot="sidebar-head"
        className={cn(
          'mt-2.5 flex h-10 shrink-0 items-center gap-[9px] pr-3 pl-[15px]',
          folded && 'justify-center px-0',
        )}
      >
        {folded ? (
          <BrandMark className="size-[26px] shrink-0 text-text" />
        ) : (
          <>
            <Wordmark
              className="min-w-0 flex-1 gap-[9px] text-text"
              markClassName="size-6 shrink-0"
            />
            {control}
          </>
        )}
      </div>
      {folded && control ? (
        <div className="mb-1 flex shrink-0 justify-center">{control}</div>
      ) : null}

      {/* The organization and workspace this navigation works in, one control; the
          slot hides when there is nothing to choose (scope-switcher.tsx). Settings has
          its own list and search, so neither the scope nor the palette is drawn there. */}
      {!folded && !settings && (
        <div
          data-slot="sidebar-scope"
          className="mx-2 mt-1 mb-2 flex shrink-0 empty:hidden"
        >
          <ScopeSwitcher />
        </div>
      )}

      {!settings && (
        <button
          type="button"
          onClick={() => useCommandStore.getState().setOpen(true)}
          // The name starts with what the button shows (WCAG 2.5.3), then the name it had.
          aria-label={`${t('nav:shell.searchGo')}, ${t('nav:shell.searchCommands')}`}
          // A tooltip only while folded: expanded, it would repeat the name.
          title={folded ? t('nav:shell.searchCommands') : undefined}
          className={cn(
            'mx-2 mb-2.5 flex h-[34px] shrink-0 items-center gap-2 rounded-ctl border-0 bg-[color-mix(in_srgb,var(--text)_8%,transparent)] px-2 text-body text-text-2',
            'hover:bg-[color-mix(in_srgb,var(--text)_12%,transparent)] hover:text-text active:bg-[var(--pressed)]',
            folded && 'mx-auto size-10 justify-center px-0',
          )}
        >
          <Search aria-hidden className="size-4" />
          {!folded && (
            <>
              <span className="flex-1 text-left">
                {t('nav:shell.searchGo')}
              </span>
              {/* Drawn, not text, as in the top bar (WCAG 2.5.3). */}
              {PALETTE_KEYS ? (
                <Kbd
                  aria-hidden
                  data-keys={chordHint(PALETTE_KEYS)}
                  className="before:content-[attr(data-keys)]"
                />
              ) : null}
            </>
          )}
        </button>
      )}
      {settings ? (
        <>
          <Link
            to={'/now' as never}
            className={cn(
              ITEM_CLASS,
              'mx-2 h-10 text-body-l font-bold text-text',
              folded && 'mx-auto size-10 justify-center px-0',
            )}
            onClick={onNavigate}
          >
            <ArrowLeft aria-hidden />
            <span className={folded ? 'sr-only' : undefined}>
              {t('nav:places.settings')}
            </span>
          </Link>
          <SettingsSectionNavigation
            folded={folded}
            onNavigate={onNavigate}
            onSearch={onSearchSettings}
          />
        </>
      ) : (
        <div className="flex min-h-0 flex-1 flex-col gap-0.5 overflow-y-auto px-2 py-1">
          {list(places.filter((p) => !p.group))}
          {(['control', 'admin'] as const).map((group) => {
            const items = places.filter((p) => p.group === group)
            return items.length ? (
              <section key={group} className="mt-4">
                <h2
                  className={
                    folded
                      ? 'mx-auto my-2 w-6 border-t border-line text-[0px]'
                      : 'px-2.5 pb-[5px] text-overline font-bold text-text-3'
                  }
                >
                  {t(`nav:groups.${group}`)}
                </h2>
                {list(items)}
              </section>
            ) : null
          })}
        </div>
      )}
      <div className="flex shrink-0 flex-col gap-0.5 p-2 pt-2.5">
        {!settings && (
          <PlaceLink
            place={settingsPlace()}
            current={false}
            folded={folded}
            onNavigate={onNavigate}
          />
        )}
        <UserMenu expanded={!folded} />
      </div>
    </div>
  )
}

/** The column, from 761 px: 248 px, folding to its 60 px icons and back; the rail up to 960 px. */
export function Sidebar() {
  const { t } = useTranslation(['nav', 'common'])
  const chosen = usePreferencesStore((s) => s.navCollapsed)
  const setFolded = usePreferencesStore((s) => s.setNavCollapsed)
  // From 961 px the places are named on every page and folding to icons is the person's own
  // choice; from 761 to 960 px only the rail fits.
  const column = useMinWidth(761)
  const roomy = useMinWidth(961)
  const railOnly = column && !roomy
  const folded = chosen === true || railOnly
  const label = t(folded ? 'nav:shell.expand' : 'nav:shell.collapse')
  return (
    <nav
      aria-label={t('common:a11y.mainNavigation')}
      data-slot="sidebar"
      data-folded={folded ? 'true' : undefined}
      className={cn(
        'flex min-h-0 shrink-0 flex-col bg-frame max-[760px]:hidden print:hidden',
        folded ? 'w-[60px]' : 'w-[248px]',
      )}
    >
      <SidebarBody
        folded={folded}
        onSearchSettings={
          railOnly
            ? undefined
            : () => {
                setFolded(false)
                // The field mounts with the open column; focus it on the next frame.
                requestAnimationFrame(() =>
                  document.getElementById('settings-search')?.focus(),
                )
              }
        }
        control={
          railOnly ? undefined : (
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
          )
        }
      />
    </nav>
  )
}

/** Below 761 px: the same navigation as a drawer from the left. Choosing a place closes it;
 * the dialog primitive contains focus, and closing returns it to `returnFocus` (the phone
 * bar's More): the drawer has no trigger of its own for the primitive to refocus. */
export function NavigationDrawer({
  open,
  onOpenChange,
  returnFocus,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  returnFocus?: RefObject<HTMLElement | null>
}) {
  const { t } = useTranslation(['nav', 'common'])
  const close = () => onOpenChange(false)
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent
        side="left"
        hideClose
        aria-describedby={undefined}
        onCloseAutoFocus={(event) => {
          event.preventDefault()
          returnFocus?.current?.focus()
        }}
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

/** A place in the phone bar: icon over an 11 px label, a 44 px target, the current place
 * bold and in full ink as well as marked for assistive technology. */
const PHONE_ITEM =
  'flex min-h-11 min-w-0 flex-col items-center justify-center gap-1 rounded-md text-metadata font-medium text-text-2 hover:text-text active:bg-[var(--pressed)] focus-visible:-outline-offset-2 aria-[current=page]:font-bold aria-[current=page]:text-text [&_svg]:size-5'

/** Below 761 px: five equal columns on the frame. Overview, Sessions, a new session, Tools,
 * and More, which opens the navigation drawer. A place the person may not open keeps its
 * column empty, so the new-session button stays in the middle. */
export function PhoneNavigation({
  onMore,
  moreRef,
}: {
  onMore: () => void
  moreRef?: Ref<HTMLButtonElement>
}) {
  const { t } = useTranslation('nav')
  const { listed } = useViewAccess()
  const places = sidebarPlaces(listed)
  const newSession = useNewSession()
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const here = currentPlace(pathname)
  const link = (id: 'now' | 'sessions' | 'tools', Icon: typeof House) => {
    const place = places.find((p) => p.id === id)
    return place ? (
      <Link
        to={place.path as never}
        search={place.search as never}
        activeOptions={{ exact: true }}
        aria-current={here === id ? 'page' : undefined}
        className={PHONE_ITEM}
      >
        <Icon aria-hidden />
        {t(`places.${id}`)}
      </Link>
    ) : (
      <span aria-hidden />
    )
  }
  return (
    <nav
      aria-label={t('common:a11y.mainNavigation')}
      data-slot="phone-navigation"
      className="fixed inset-x-0 bottom-0 z-20 grid h-14 grid-cols-5 items-center border-t border-line bg-frame min-[761px]:hidden print:hidden"
    >
      {link('now', House)}
      {link('sessions', MessageSquare)}
      <button
        type="button"
        onClick={newSession}
        aria-label={t('shell.keys.command.session.new')}
        className="mx-auto grid size-11 place-items-center rounded-full"
      >
        <span className="grid size-10 place-items-center rounded-full bg-accent text-on-accent">
          <Plus aria-hidden className="size-5" />
        </span>
      </button>
      {link('tools', Wrench)}
      <button
        ref={moreRef}
        type="button"
        onClick={onMore}
        className={PHONE_ITEM}
      >
        <MoreHorizontal aria-hidden />
        {t('shell.more')}
      </button>
    </nav>
  )
}
