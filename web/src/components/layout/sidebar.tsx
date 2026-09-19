// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SIDEBAR (N1) — Overview, then the nine areas, then the pinned Settings utility.
//
// Every list here is a projection of features/navigation/model.ts over the single
// FEATURE_VIEWS registry: the expanded sidebar, the icon rail and the mobile drawer render
// the SAME areas, sections and leaves for the same principal, and the filter searches the
// SAME index the ⌘K palette does. Nothing in this file names a route.
//
// Shape of an area row: a LINK to the area's directory page (`/areas/<id>`) and, beside it,
// a separate BUTTON that expands or folds the area's modules. Two controls, because they do
// two things and a screen-reader user must be able to do either without triggering the
// other. `aria-current="page"` marks exactly one link — the current page — and the area
// that contains it is distinguished visually (`data-branch="active"`) without claiming to be
// the page. Sections inside an area are grouping labels, not links.
//
// Expansion: arriving inside an area opens it; the operator may hold several open, open or
// fold them all, or fold the active one without leaving it (stores/preferences.ts navAreas).
//
// FILTERING IS A RANKED PROJECTION (R2, independent review F1). With a query the grouped
// tree is replaced by ONE flat list of the authorized matches in the order `rankNavMatches`
// returns them — the same index, the same ranking and the same authorization projection the
// ⌘K palette renders — each entry carrying its `Area › Section` context. The earlier build
// kept the canonical area order and only hid non-matches, so a weak description hit in an
// early area sat above an exact label hit in a later one; it was measured with
// "admin" (Provider profiles before Administration). Without a query the canonical grouped
// order returns untouched, and the fold preference is neither read nor written by a query.
//
// IDS ARE INSTANCE-SCOPED (R2, independent review F2). The desktop sidebar stays mounted
// (CSS-hidden below `lg`) while the drawer mounts a second SidebarBody, and the rail's
// flyouts render a third set of section groups. Every `aria-controls` / `aria-labelledby`
// target therefore carries this instance's `useId()` prefix, so each control names the
// panel or heading it actually renders and `getElementById` can only resolve to that one.
import { Link, useRouterState } from '@tanstack/react-router'
import type { LucideIcon } from 'lucide-react'
import {
  ArrowRight,
  ChevronDown,
  ChevronsDownUp,
  ChevronsUpDown,
  PanelLeftClose,
  PanelLeftOpen,
  Search,
  Star,
  X,
} from 'lucide-react'
import {
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type RefObject,
} from 'react'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover'
import { ScrollArea } from '@/components/ui/scroll-area'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetTitle,
} from '@/components/ui/sheet'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { useViewAccess } from '@/features/navigation/authorization'
import { NAV_AREAS, type AreaId, type NavArea } from '@/features/registry'
import {
  SETTINGS_UTILITY,
  activeAreaId as activeAreaOf,
  areaLabel,
  areaQuestion,
  authorizedEntries,
  authorizedSections,
  buildNavSearchIndex,
  fold,
  rankNavMatches,
  resolveLocation,
  sectionLabel,
  viewLabel,
  type AreaSection,
  type NavSearchEntry,
  type ViewGate,
} from '@/features/navigation/model'
import { railKey, type RailRow } from '@/features/navigation/rail-keys'
import { usePersonalNavigation } from '@/features/navigation/personal-navigation'
import { personalLink } from '@/features/navigation/personal-navigation-store'
import { isAreaOpen, usePreferencesStore } from '@/stores/preferences'
import { BrandMark, Wordmark } from './brand'
import { PersonalNavigation } from './personal-navigation'
import { TenantSwitcher } from './tenant-switcher'
import { WorkspaceSwitcher } from './workspace-switcher'

// The folding helper stays importable from here for its existing callers and tests.
export { fold }

/** Row chrome shared by the Overview link, the area links and every leaf. */
const ROW_CLASS = cn(
  'group relative flex min-h-8 min-w-0 items-center gap-2.5 rounded-md px-2.5 py-1 text-body text-muted-foreground outline-none transition-colors',
  'hover:bg-muted hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring',
  'data-[status=active]:bg-accent-soft data-[status=active]:font-medium data-[status=active]:text-foreground',
  'before:absolute before:top-1/2 before:left-0 before:h-4 before:w-0.5 before:-translate-y-1/2 before:rounded-r-full before:bg-transparent',
  'data-[status=active]:before:bg-accent-text',
  'data-[status=active]:[&_svg]:text-accent-text [&_svg]:size-4 [&_svg]:shrink-0',
)

interface NavItemProps {
  to: string
  icon: LucideIcon
  label: string
  /** `Area › Section` (or the "Area" tag) under the label — ranked results only. */
  context?: string
  exact?: boolean
  collapsed?: boolean
  onNavigate?: () => void
  className?: string
  /** 0 for a top-level row, 1 for a leaf inside an area. Read by the rail keyboard. */
  depth?: 0 | 1
  /** The feature id this row pins, when the principal has a personal partition. */
  pinId?: string
}

/**
 * ⛔ LABELS WRAP; THEY ARE NEVER CUT, and the reason is a measurement rather than a
 *    preference. On 2026-09-18, at 1600 px against a live engine, the rail cut **65
 *    labels across the seven console languages** — en 5, es 12, ja 9, de 8, ru 16, fr 15,
 *    zh 0. The worst was 102 px of French past the edge
 *    (*Boîte de réception des communications*). An earlier review recorded "three
 *    truncated labels", which was Spanish, area rows only.
 *
 *    A width that fits 102 px more is not a rail, and a per-language character budget is
 *    a gate that has to be re-tuned for every string and cannot cover the eighth language.
 *    So the row grows to a second line and the label stays whole. `title` is kept for the
 *    pointer, but a tooltip was never the answer: it is invisible to a keyboard and to a
 *    reader who is just trying to find a module.
 */
function NavItem({
  to,
  icon: Icon,
  label,
  context,
  exact,
  collapsed,
  onNavigate,
  className,
  depth = 0,
  pinId,
}: NavItemProps) {
  const link = (
    <Link
      // The feature registry IS the route table, so these paths are always valid.
      to={to as never}
      activeOptions={{ exact: !!exact }}
      // WCAG 4.1.2 / 2.4.8: announce the current page to assistive tech (the router
      // only sets data-status, so set aria-current explicitly when active).
      activeProps={{ 'aria-current': 'page' }}
      onClick={onNavigate}
      aria-label={collapsed ? label : undefined}
      // The rail is ONE tab stop with a roving tabindex (see `useRailKeyboard`), so every
      // row starts untabbable and the hook promotes exactly one.
      tabIndex={-1}
      data-nav-row=""
      data-depth={depth}
      data-pin-id={pinId}
      className={cn(ROW_CLASS, collapsed && 'justify-center px-0', className)}
    >
      <Icon />
      {!collapsed && !context && (
        <span className="min-w-0 break-words" title={label}>
          {label}
        </span>
      )}
      {!collapsed && context && (
        <span className="flex min-w-0 flex-col">
          <span className="break-words" title={label}>
            {label}
          </span>
          <span
            className="break-words text-overline font-normal tracking-normal text-muted-foreground"
            title={context}
          >
            {context}
          </span>
        </span>
      )}
    </Link>
  )
  if (collapsed) {
    return (
      <Tooltip>
        <TooltipTrigger asChild>{link}</TooltipTrigger>
        <TooltipContent side="right">{label}</TooltipContent>
      </Tooltip>
    )
  }
  if (!pinId) return link
  return (
    <span
      data-rail-row-group=""
      className="group/row flex min-w-0 items-center gap-0.5"
    >
      {link}
      <RailPin id={pinId} label={label} />
    </span>
  )
}

/**
 * The pin, ON THE ROW and NAMED BEFORE IT HAPPENS.
 *
 * ⛔ IT IS NOT A TAB STOP, and it does not need to be: the rail owns one, and this control
 *    carries `tabIndex={-1}` so it stays in the accessibility tree — reachable in a screen
 *    reader's browse mode and by pointer — without putting eighty-five stops back. `p` on
 *    the focused row does the same thing, and the button's own name says so, which is what
 *    makes the two EQUAL paths rather than a key and a secret.
 *
 * ⚠ A link cannot contain a button (invalid HTML, and assistive technology reports the
 *   inner control inconsistently), so it is a SIBLING inside the row, exactly as the
 *   work rail resolves the same question.
 */
function RailPin({ id, label }: { id: string; label: string }) {
  const personal = usePersonalNavigation()
  const { t } = useTranslation('nav')
  // `personalLink` returns undefined for an id the registry cannot resolve — a retired
  // module still listed somewhere. No link, no pin, and no crash.
  const link = personalLink(id)
  if (!link || !personal?.available || !personal.visible(link)) return null
  const pinned = personal.favorites.some((f) => f.id === id)
  const name = t(pinned ? 'personal.unpinRow' : 'personal.pinRow', {
    name: label,
  })
  return (
    <button
      type="button"
      tabIndex={-1}
      aria-pressed={pinned}
      aria-label={name}
      title={name}
      data-rail-pin={id}
      onClick={() => personal.setFavorite(link, !pinned)}
      className={cn(
        'flex size-6 shrink-0 items-center justify-center rounded text-muted-foreground outline-none',
        'hover:bg-muted hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring',
        // Quiet until the row is under the pointer or holds focus — a column of stars
        // beside seventy modules is noise, and the key works whether it is painted or not.
        pinned
          ? 'text-accent-text'
          : 'opacity-0 group-hover/row:opacity-100 group-focus-within/row:opacity-100',
      )}
    >
      <Star
        aria-hidden
        className={cn('size-3.5', pinned && 'fill-accent-soft')}
      />
    </button>
  )
}

/** One area's sections and leaves — the same markup inside the expanded sidebar, the rail's
 * flyout and the mobile drawer. */
function AreaSections({
  area,
  sections,
  idPrefix,
  onNavigate,
  dense,
}: {
  area: NavArea
  sections: AreaSection[]
  /** This instance's id namespace (a `useId()` of the owning body plus the surface). */
  idPrefix: string
  onNavigate?: () => void
  dense?: boolean
}) {
  const { t } = useTranslation('nav')
  return (
    <>
      {sections.map((s) => {
        const headingId = `${idPrefix}${area.id}-${s.sectionId}`
        return (
          <div key={s.sectionId} role="group" aria-labelledby={headingId}>
            <p
              id={headingId}
              className={cn(
                'break-words px-2.5 pb-0.5 text-overline text-muted-foreground uppercase',
                dense ? 'pt-1' : 'pt-1.5',
              )}
            >
              {sectionLabel(t, area.id, s.sectionId)}
            </p>
            <ul className="flex flex-col gap-0.5">
              {s.views.map((v) => (
                <li key={v.id}>
                  <NavItem
                    to={v.path}
                    icon={v.icon}
                    label={viewLabel(t, v.id)}
                    onNavigate={onNavigate}
                    depth={1}
                    pinId={v.id}
                  />
                </li>
              ))}
            </ul>
          </div>
        )
      })}
    </>
  )
}

/**
 * THE RAIL FROM A KEYBOARD — one tab stop, a roving tabindex, `rail-keys.ts` for the
 * rules.
 *
 * ⛔ WHY: measured 2026-09-18 at 1600 px against a live engine, the sidebar held **85 tab
 *    stops**. Tab eighty-five times, then the page. `DataTable` solved the same problem
 *    for ten thousand rows and the work rail does it too, with the same sentence: moving focus
 *    is not choosing.
 *
 * ⛔ THE ROWS ARE READ FROM THE DOM, not held in state, and that is deliberate. The rail's
 *    shape changes with the filter, the fold preference, the permission projection and the
 *    personal partition — four sources — and a mirrored array would be a fifth that
 *    disagrees with all of them. `querySelectorAll` at keystroke time is the shape the
 *    operator is actually looking at.
 *
 * ⛔ AND THE ACTIVE ROW IS THE CURRENT PAGE. Arriving at a route puts the roving index on
 *    the row that names it and scrolls it into view, so "where am I" is answered before
 *    the first keystroke rather than by counting arrows from the top.
 */
function useRailKeyboard(
  rail: RefObject<HTMLElement | null>,
  {
    onExpand,
    onFold,
  }: { onExpand: (id: string) => void; onFold: (id: string) => void },
) {
  // ⚠ NOT `useCallback`. These read `rail.current`, and the React Compiler refuses to
  //   preserve a memo whose input is a ref it cannot see change ("Compilation Skipped:
  //   Existing memoization could not be preserved" — an ESLint ERROR in this repository).
  //   Plain functions let the compiler do the memoizing it is here to do.
  const rowsOf = (): HTMLElement[] =>
    Array.from(
      rail.current?.querySelectorAll<HTMLElement>('[data-nav-row]') ?? [],
    )

  /** Exactly one row is tabbable, and it is the current page when there is one. */
  const settle = (): HTMLElement | undefined => {
    const rows = rowsOf()
    if (rows.length === 0) return undefined
    let active = rows.findIndex(
      (r) => r.getAttribute('aria-current') === 'page',
    )
    if (active < 0) active = rows.findIndex((r) => r.tabIndex === 0)
    if (active < 0) active = 0
    rows.forEach((r, i) => {
      r.tabIndex = i === active ? 0 : -1
    })
    return rows[active]
  }

  // Every render: the rail's shape changes with the filter, the fold preference, the
  // permission projection and the personal partition, and the tab stop has to follow it.
  // `nearest` rather than `center` — a rail that jumps on every navigation is its own
  // defect.
  useEffect(() => {
    settle()?.scrollIntoView({ block: 'nearest' })
  })

  return (event: React.KeyboardEvent<HTMLElement>) => {
    // Never steal a keystroke from the filter field above the tree.
    if (event.target instanceof HTMLInputElement) return
    if (event.metaKey || event.ctrlKey || event.altKey) return
    const rows = rowsOf()
    const from = (event.target as HTMLElement).closest<HTMLElement>(
      '[data-nav-row]',
    )
    const at = from ? rows.indexOf(from) : -1
    if (at < 0) return
    const model: RailRow[] = rows.map((r) => {
      const areaId = r.getAttribute('data-nav-area-row')
      return {
        depth: r.getAttribute('data-depth') === '1' ? 1 : 0,
        pinId: r.getAttribute('data-pin-id') ?? undefined,
        area: areaId
          ? { id: areaId, open: r.getAttribute('data-area-open') === 'true' }
          : undefined,
      }
    })
    const action = railKey(event.key, model, at)
    if (action.kind === 'none') return
    event.preventDefault()
    if (action.kind === 'focus') {
      rows.forEach((r, i) => {
        r.tabIndex = i === action.index ? 0 : -1
      })
      rows[action.index]?.focus()
      rows[action.index]?.scrollIntoView({ block: 'nearest' })
      return
    }
    if (action.kind === 'expand') return onExpand(action.areaId)
    if (action.kind === 'fold') return onFold(action.areaId)
    if (action.kind === 'pin') {
      // The row's own control, clicked. ONE implementation of the action, so the key and
      // the named menu path cannot drift into two behaviours.
      rows[action.index]
        ?.closest('[data-rail-row-group]')
        ?.querySelector<HTMLButtonElement>('[data-rail-pin]')
        ?.click()
    }
  }
}

interface ProjectedArea {
  area: NavArea
  /** The sections this principal may open — the union that makes the area visible. */
  sections: AreaSection[]
}

/**
 * Areas this principal may see — the union of authorized leaves, in the ratified order.
 * An area with no authorized leaf is never offered.
 */
function useProjectedAreas(gate: ViewGate): ProjectedArea[] {
  return useMemo(
    () =>
      NAV_AREAS.map((area) => ({
        area,
        sections: authorizedSections(area.id, gate),
      })).filter((a) => a.sections.length > 0),
    [gate],
  )
}

/** The ranked filtered projection: authorized entries in `rankNavMatches` order. */
function useRankedMatches(
  index: readonly NavSearchEntry[],
  gate: ViewGate,
  query: string,
): NavSearchEntry[] {
  return useMemo(
    () =>
      query.trim() ? rankNavMatches(authorizedEntries(index, gate), query) : [],
    [index, gate, query],
  )
}

function SidebarBody({
  collapsed = false,
  onNavigate,
}: {
  collapsed?: boolean
  onNavigate?: () => void
}) {
  const { t } = useTranslation('nav')
  // ⛔ ONE projection, shared with the palette, the shortcuts and the nine directories.
  //    `useAuth().can` is no longer read here: a view whose authority is a registered
  //    capability question would answer from the reflection, and the sidebar would offer
  //    a door the route then refuses (or hide one the engine allows).
  const { navigable } = useViewAccess()
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const location = useMemo(() => resolveLocation(pathname), [pathname])
  const activeAreaId = activeAreaOf(location)

  const expansion = usePreferencesStore((s) => s.navAreas)
  const setAreaOpen = usePreferencesStore((s) => s.setAreaOpen)
  const setAreasOpen = usePreferencesStore((s) => s.setAreasOpen)
  const revealArea = usePreferencesStore((s) => s.revealArea)
  // Arriving inside an area opens it (unless the operator folds it again while here).
  useEffect(() => {
    if (activeAreaId) revealArea(activeAreaId)
  }, [activeAreaId, revealArea])

  const [query, setQuery] = useState('')
  // One namespace per rendered body: the desktop sidebar, the drawer and each rail flyout
  // get their own, so an `aria-controls` never names another instance's panel.
  const uid = useId()
  const searchId = `${uid}filter`
  const filtering = query.trim().length > 0
  const index = useMemo(() => buildNavSearchIndex(t), [t])
  const areas = useProjectedAreas(navigable)
  const ranked = useRankedMatches(index, navigable, query)
  const homeLabel = viewLabel(t, 'home')
  const settingsLabel = viewLabel(t, SETTINGS_UTILITY.id)
  const homeIcon = index.find((e) => e.kind === 'view' && e.id === 'home')?.icon

  // Counts what the operator can actually SEE: with a query, the ranked list IS the
  // visible list (Overview and Settings included when they match), so the announcement
  // and the screen cannot disagree.
  const hits = filtering ? ranked.length : 0

  // ONE TAB STOP, arrows inside — the rules live in `features/navigation/rail-keys.ts`.
  const railRef = useRef<HTMLDivElement>(null)
  const onRailKeyDown = useRailKeyboard(railRef, {
    onExpand: (id) => setAreaOpen(id as AreaId, true),
    onFold: (id) => setAreaOpen(id as AreaId, false),
  })

  // The rail's flyouts: one open at a time, closed by navigating from inside it.
  const [flyout, setFlyout] = useState<AreaId | null>(null)
  const closeFlyout = () => setFlyout(null)

  const visibleIds = areas.map((a) => a.area.id)
  const allOpen =
    visibleIds.length > 0 &&
    visibleIds.every((id) => isAreaOpen(expansion, id, activeAreaId))

  return (
    // The rail's keyboard region is the WHOLE body, not just the tree: `Settings` is a
    // navigation row that happens to be pinned to the foot, and `End` must reach it. The
    // handler ignores keystrokes aimed at the filter field above.
    <div
      ref={railRef}
      onKeyDown={onRailKeyDown}
      className="flex h-full flex-col"
    >
      <div
        className={cn(
          'flex h-[var(--console-header-height)] shrink-0 items-center border-b border-border',
          collapsed ? 'justify-center px-0' : 'px-3',
        )}
      >
        {collapsed ? <BrandMark className="text-foreground" /> : <Wordmark />}
      </div>

      {/* THE SCOPE, AND IT IS THE RAIL'S CAPTION AND NOT THE PAGE'S.
          Organisation and workspace used to live in the topbar, in a SECOND 40 px row
          below `lg` — 40 px of every phone viewport, and half of the 52 % of chrome the
          measurement put there. They belong here on first principles too: the
          scope is what the NAVIGATION operates on, so it reads as a caption under the
          wordmark rather than as a page control.

          Nothing was cut. Below `lg` this rail is a drawer, so the switchers travel
          with the navigation they scope, and the drawer trigger is the first control in
          the header.

          The icon rail has no room for a 14 rem label — the same reason the filter
          field is not rendered there — and both switchers return `null` when there is
          nothing to choose, so a single-membership deployment spends no pixels here. */}
      {!collapsed && (
        <div
          data-slot="rail-scope"
          className="flex shrink-0 flex-col gap-0.5 border-b border-border p-1.5"
        >
          <TenantSwitcher className="w-full max-w-none justify-start" />
          <WorkspaceSwitcher className="w-full max-w-none justify-start" />
        </div>
      )}

      {/* The icon rail has no room for a field, and hiding matches behind a tooltip
          would be worse than not offering search at all. */}
      {!collapsed && (
        <div className="shrink-0 border-b border-border p-2">
          <div className="relative">
            <Search
              aria-hidden="true"
              className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted-foreground"
            />
            <input
              id={searchId}
              type="search"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              onKeyDown={(e) => e.key === 'Escape' && setQuery('')}
              placeholder={t('filter.placeholder')}
              aria-label={t('filter.label')}
              className="h-8 w-full rounded-md border border-border bg-background pr-7 pl-8 text-body outline-none placeholder:text-muted-foreground focus-visible:ring-2 focus-visible:ring-ring [&::-webkit-search-cancel-button]:appearance-none"
            />
            {filtering && (
              <button
                type="button"
                onClick={() => {
                  setQuery('')
                  // Clearing unmounts this button, so focus would fall to <body> and a
                  // keyboard user would have to tab back in from the top of the page.
                  document.getElementById(searchId)?.focus()
                }}
                aria-label={t('filter.clear')}
                className="absolute top-1/2 right-1.5 -translate-y-1/2 rounded p-1 text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring"
              >
                <X aria-hidden="true" className="size-3.5" />
              </button>
            )}
          </div>
          {/* Announce the count to assistive tech: a sighted user sees the list shrink,
              a screen-reader user gets nothing unless we say it (WCAG 4.1.3). */}
          <div aria-live="polite" className="sr-only">
            {filtering ? t('filter.results', { n: hits }) : ''}
          </div>
        </div>
      )}

      {/* ⛔ EL BORDE DE ABAJO, y el defecto es de LECTURA, no de layout. Medido a
          1440x900: el area de scroll y el pie `Settings` NO se solapan (solape = 0
          px exactos), pero el viewport termina en y=851 justo donde empieza el
          `border-t` del pie, y a esa altura el ultimo item visible —«Setup
          wizard»— queda cortado a 22 de sus 32 px. Con una linea dura en el corte,
          un item partido se lee como un item PISADO por el pie.

          ⚠ «Un scroll que no parta ningun item» NO es alcanzable: con 754 px de
          viewport y un paso de 32 px, casi cualquier altura de ventana parte
          alguno (a 768 y 1080 no pasa, a 900 si). Lo alcanzable es que un item
          parcial SE LEA como parcial, y eso son dos piezas que solo funcionan
          juntas:
            · la mascara desvanece los ultimos 16 px del area;
            · `pb-6` deja 24 px por debajo del ultimo item, para que al bajar del
              todo el desvanecido caiga sobre el hueco y NO sobre el item — sin
              ese padding, la cura dejaria «Supply chain» atenuado para siempre,
              que es otro defecto. */}
      <ScrollArea className="flex-1 [mask-image:linear-gradient(to_bottom,#000_calc(100%-16px),transparent_100%)]">
        <nav
          aria-label={t('common:a11y.mainNavigation')}
          // The rail is a composite: one tab stop, arrows inside. `aria-keyshortcuts`
          // names the one key that is NOT movement, so a screen reader announces it
          // with the region rather than leaving it to the help overlay.
          aria-keyshortcuts="p"
          className="flex flex-col gap-1 p-2 pb-6 [contain:inline-size]"
        >
          {filtering && !collapsed && (
            // The ranked projection: every authorized match, in the shared ranking's
            // order, with its area/section context. Native links, `aria-current` on the
            // current page, and the same permissions as the grouped tree; the fold
            // preference is untouched underneath.
            <ul
              data-nav-ranked=""
              aria-label={t('filter.results', { n: hits })}
              className="flex flex-col gap-0.5"
            >
              {ranked.map((e) => (
                <li key={`${e.kind}:${e.id}`}>
                  <NavItem
                    to={e.path}
                    icon={e.icon}
                    label={e.label}
                    context={
                      e.kind === 'area' ? t('directory.area') : e.context
                    }
                    exact={e.path === '/' || e.kind === 'area'}
                    onNavigate={onNavigate}
                  />
                </li>
              ))}
            </ul>
          )}

          {!filtering && homeIcon && (
            <NavItem
              to="/"
              icon={homeIcon}
              label={homeLabel}
              exact
              collapsed={collapsed}
              onNavigate={onNavigate}
              pinId="home"
            />
          )}

          {!filtering && (
            <PersonalNavigation collapsed={collapsed} onNavigate={onNavigate} />
          )}

          {!filtering && !collapsed && areas.length > 0 && (
            <div className="mt-1 flex items-center justify-between px-2.5 pb-0.5">
              <span className="text-overline text-muted-foreground uppercase">
                {t('directory.areas')}
              </span>
              <button
                type="button"
                onClick={() => setAreasOpen(visibleIds, !allOpen)}
                aria-label={
                  allOpen
                    ? t('directory.collapseAll')
                    : t('directory.expandAll')
                }
                className="rounded p-1 text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring"
              >
                {allOpen ? (
                  <ChevronsDownUp aria-hidden="true" className="size-3.5" />
                ) : (
                  <ChevronsUpDown aria-hidden="true" className="size-3.5" />
                )}
              </button>
            </div>
          )}

          {(!filtering || collapsed) &&
            areas.map(({ area, sections }) => {
              const label = areaLabel(t, area.id)
              const isBranch = activeAreaId === area.id
              const Icon = area.icon

              if (collapsed) {
                // The rail shows AREAS, not fifty icons. The trigger is a real button that
                // opens the area's modules in a flyout — reachable by keyboard and by touch,
                // never hover-only — and the flyout's first entry is the directory link.
                return (
                  <Popover
                    key={area.id}
                    open={flyout === area.id}
                    onOpenChange={(open) => setFlyout(open ? area.id : null)}
                  >
                    <Tooltip>
                      <TooltipTrigger asChild>
                        <PopoverTrigger asChild>
                          <button
                            type="button"
                            aria-label={t('directory.modulesOf', {
                              area: label,
                            })}
                            data-status={isBranch ? 'active' : undefined}
                            data-branch={isBranch ? 'active' : undefined}
                            className={cn(
                              ROW_CLASS,
                              'w-full justify-center px-0',
                            )}
                          >
                            <Icon />
                          </button>
                        </PopoverTrigger>
                      </TooltipTrigger>
                      <TooltipContent side="right">{label}</TooltipContent>
                    </Tooltip>
                    <PopoverContent
                      side="right"
                      align="start"
                      className="w-64 p-2"
                      aria-label={t('directory.modulesOf', { area: label })}
                    >
                      <Link
                        to={area.path as never}
                        activeOptions={{ exact: true }}
                        activeProps={{ 'aria-current': 'page' }}
                        onClick={closeFlyout}
                        className={cn(ROW_CLASS, 'font-medium text-foreground')}
                      >
                        <Icon />
                        <span className="min-w-0 flex-1 truncate" title={label}>
                          {label}
                        </span>
                        <ArrowRight
                          aria-hidden="true"
                          className="size-3.5 text-muted-foreground"
                        />
                      </Link>
                      <p className="px-2.5 pb-1 text-caption text-muted-foreground">
                        {areaQuestion(t, area.id)}
                      </p>
                      <AreaSections
                        area={area}
                        sections={sections}
                        idPrefix={`${uid}flyout-`}
                        onNavigate={closeFlyout}
                        dense
                      />
                    </PopoverContent>
                  </Popover>
                )
              }

              const open = isAreaOpen(expansion, area.id, activeAreaId)
              const panelId = `${uid}nav-area-${area.id}`
              return (
                <div
                  key={area.id}
                  data-nav-area={area.id}
                  data-branch={isBranch ? 'active' : undefined}
                  className="flex flex-col gap-0.5"
                >
                  <div className="flex items-center gap-0.5">
                    <Link
                      to={area.path as never}
                      activeOptions={{ exact: true }}
                      activeProps={{ 'aria-current': 'page' }}
                      onClick={onNavigate}
                      tabIndex={-1}
                      data-nav-row=""
                      data-depth="0"
                      data-nav-area-row={area.id}
                      data-area-open={open ? 'true' : 'false'}
                      className={cn(
                        ROW_CLASS,
                        'flex-1',
                        isBranch && 'text-foreground',
                      )}
                    >
                      <Icon />
                      <span className="min-w-0 break-words" title={label}>
                        {label}
                      </span>
                    </Link>
                    <button
                      type="button"
                      onClick={() => setAreaOpen(area.id, !open)}
                      aria-expanded={open}
                      aria-controls={panelId}
                      tabIndex={-1}
                      aria-label={
                        open
                          ? t('directory.collapse', { area: label })
                          : t('directory.expand', { area: label })
                      }
                      className="flex size-8 shrink-0 items-center justify-center rounded-md text-muted-foreground outline-none hover:bg-muted hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring"
                    >
                      <ChevronDown
                        aria-hidden="true"
                        className={cn(
                          'size-3.5 transition-transform',
                          !open && '-rotate-90',
                        )}
                      />
                    </button>
                  </div>
                  <div
                    id={panelId}
                    className={cn(
                      'ml-4 flex flex-col gap-0.5 border-l border-border pl-1',
                      !open && 'hidden',
                    )}
                  >
                    <AreaSections
                      area={area}
                      sections={sections}
                      idPrefix={`${uid}nav-area-`}
                      onNavigate={onNavigate}
                    />
                  </div>
                </div>
              )
            })}

          {filtering && hits === 0 && (
            <p className="px-2.5 py-4 text-body text-muted-foreground">
              {t('filter.empty', { query: query.trim() })}
            </p>
          )}
        </nav>
      </ScrollArea>

      {/* Settings is a pinned utility, not a registry view — but it IS a link, so while
          filtering it obeys the filter: it then appears in the ranked list when it
          matches, and never twice. Leaving it always visible made the sr-only count say
          "0" with one link still on screen. */}
      {!filtering && (
        <div className="shrink-0 border-t border-border p-2">
          <NavItem
            to={SETTINGS_UTILITY.path}
            icon={SETTINGS_UTILITY.icon}
            label={settingsLabel}
            collapsed={collapsed}
            onNavigate={onNavigate}
            pinId={SETTINGS_UTILITY.id}
          />
        </div>
      )}
    </div>
  )
}

/** The persistent desktop sidebar (≥ lg). Collapses to an icon rail with flyouts. */
export function Sidebar() {
  const { t } = useTranslation('common')
  const collapsed = usePreferencesStore((s) => s.sidebarCollapsed)
  const toggle = usePreferencesStore((s) => s.toggleSidebar)
  return (
    <aside
      aria-label={t('a11y.primarySidebar')}
      className={cn(
        'relative hidden shrink-0 flex-col border-r border-border bg-surface lg:flex print:hidden',
        collapsed ? 'w-14' : 'w-60',
      )}
    >
      <SidebarBody collapsed={collapsed} />
      <Button
        variant="ghost"
        size="icon-sm"
        onClick={toggle}
        aria-expanded={!collapsed}
        aria-label={
          collapsed ? t('actions.expandSidebar') : t('actions.collapseSidebar')
        }
        className="absolute -right-3 top-3 z-10 rounded-full border border-border bg-surface text-muted-foreground shadow-sm"
      >
        {collapsed ? <PanelLeftOpen /> : <PanelLeftClose />}
      </Button>
    </aside>
  )
}

/** The mobile off-canvas navigation (< lg), opened from the topbar menu button. */
export function MobileNav({
  open,
  onOpenChange,
  returnFocusTo,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  /**
   * The control that opened the drawer; closing gives focus back to it. The sheet
   * primitive's own restore left focus on <body> on the built console (measured
   * 2026-09-06, keyboard and pointer paths), so the drawer restores it explicitly.
   */
  returnFocusTo?: RefObject<HTMLElement | null>
}) {
  const { t } = useTranslation(['nav', 'common'])
  const restore = () => {
    const el = returnFocusTo?.current
    if (el && el.isConnected) el.focus()
  }
  useEffect(() => {
    if (open) return
    const id = window.setTimeout(() => {
      if (document.activeElement === document.body || !document.activeElement)
        restore()
    }, 0)
    return () => window.clearTimeout(id)
    // eslint-disable-next-line react-hooks/exhaustive-deps -- the ref is stable; only the open flag matters
  }, [open])
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent
        side="left"
        className="w-72 p-0"
        onCloseAutoFocus={(e) => {
          const el = returnFocusTo?.current
          if (el && el.isConnected) {
            e.preventDefault()
            el.focus()
          }
        }}
      >
        {/* Name the drawer for what it IS — navigation — not the "Overview" group
            label / "Search…" that previously misled the SR announcement. */}
        <SheetTitle className="sr-only">
          {t('common:a11y.mainNavigation')}
        </SheetTitle>
        <SheetDescription className="sr-only">
          {t('common:commandPalette.placeholder')}
        </SheetDescription>
        <SidebarBody onNavigate={() => onOpenChange(false)} />
      </SheetContent>
    </Sheet>
  )
}
