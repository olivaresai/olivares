// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Link } from '@tanstack/react-router'
import type { LucideIcon } from 'lucide-react'
import {
  ChevronDown,
  PanelLeftClose,
  PanelLeftOpen,
  Search,
  Settings,
  X,
} from 'lucide-react'
import { useId, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
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
import { useAuth } from '@/lib/auth/context'
import {
  HUB_ORDER,
  nounsForView,
  viewsByHub,
  type FeatureView,
} from '@/features/registry'
import { usePreferencesStore } from '@/stores/preferences'
import { BrandMark, Wordmark } from './brand'

interface NavItemProps {
  to: string
  icon: LucideIcon
  label: string
  exact?: boolean
  collapsed?: boolean
  onNavigate?: () => void
}

function NavItem({
  to,
  icon: Icon,
  label,
  exact,
  collapsed,
  onNavigate,
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
      className={cn(
        'group relative flex h-8 items-center gap-2.5 rounded-md px-2.5 text-sm text-muted-foreground outline-none transition-colors',
        'hover:bg-muted hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring',
        'data-[status=active]:bg-accent-soft data-[status=active]:font-medium data-[status=active]:text-foreground',
        'before:absolute before:top-1/2 before:left-0 before:h-4 before:w-0.5 before:-translate-y-1/2 before:rounded-r-full before:bg-transparent',
        'data-[status=active]:before:bg-accent-text',
        'data-[status=active]:[&_svg]:text-accent-text [&_svg]:size-4 [&_svg]:shrink-0',
        collapsed && 'justify-center px-0',
      )}
    >
      <Icon />
      {!collapsed && <span className="truncate">{label}</span>}
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
  return link
}

/**
 * Fold case and strip accents so a Spanish operator typing "sesiones" matches "Sesión"
 * and a German one typing "prufung" matches "Prüfung". Without this the filter is a
 * trap in the Latin-script languages: it looks like it works until the word carries a
 * diacritic, and then it silently reports nothing.
 *
 * ⚠ THE STRIP IS SCOPED TO LATIN BASE LETTERS ON PURPOSE. A blanket
 * `.replace(/\p{Diacritic}/gu, '')` — which this had, until the adversarial contrast
 * measured it — is not accent-insensitivity outside Latin, it is corruption: NFD turns
 * Russian "й" into "и" + a combining breve and Japanese "が" into "か" + the combining
 * voiced mark, and stripping those changes the letter. "Задачи" would stop matching
 * itself. Recomposing with NFC afterwards leaves every non-Latin word exactly as typed.
 */
export function fold(s: string): string {
  return s
    .normalize('NFD')
    .replace(/([A-Za-z])[\u0300-\u036f]+/g, '$1')
    .normalize('NFC')
    .toLowerCase()
}

function SidebarBody({
  collapsed = false,
  onNavigate,
}: {
  collapsed?: boolean
  onNavigate?: () => void
}) {
  const { t } = useTranslation('nav')
  const { can } = useAuth()
  const hubs = viewsByHub()
  // Per-hub collapse: applies only to the expanded sidebar — the icon
  // rail has no headers, so it always shows every reachable item.
  const collapsedGroups = usePreferencesStore((s) => s.collapsedNavGroups)
  const toggleNavGroup = usePreferencesStore((s) => s.toggleNavGroup)
  const [query, setQuery] = useState('')
  const searchId = useId()
  const filtering = query.trim().length > 0

  /**
   * — the other half of the answer to 51 entries. Five hubs alone is still a menu
   * you scroll; the audit measured the concentration (P2-12: 17 of 49 in one
   * group, no search — 18 of 51 when re-measured today) and the remedy is BOTH.
   *
   * The index carries the NOUNS as well as the label, and that is the point rather than
   * a nicety: the hubs are verbs's thirteen questions are nouns, and a heading can
   * only be one of the two. Typing "identidades" finds /identity, /permissions, /console
   * and /access-map across two hubs — so re-hubbing never buried a thing an operator
   * knows by name. Path is indexed too, for anyone who thinks in urls.
   */
  const haystacks = useMemo(() => {
    const m = new Map<string, string>()
    for (const v of Object.values(hubs).flat()) {
      m.set(
        v.id,
        fold(
          [
            t(`items.${v.id}`),
            t(`descriptions.${v.id}`, { defaultValue: '' }),
            v.path,
            t(`hubs.${v.hub}`),
            ...nounsForView(v.id).map((n) => t(`nouns.${n}`)),
          ].join(' '),
        ),
      )
    }
    return m
  }, [hubs, t])

  const needle = fold(query.trim())
  const matches = (v: FeatureView) =>
    !filtering || (haystacks.get(v.id) ?? '').includes(needle)

  // Counts what the operator can actually SEE, Settings included — an announcement that
  // disagrees with the visible list is worse than no announcement.
  const hits = filtering
    ? HUB_ORDER.reduce(
        (n, hub) =>
          n +
          hubs[hub].filter(
            (v) => (!v.permission || can(v.permission)) && matches(v),
          ).length,
        0,
      ) + (fold(t('items.settings')).includes(needle) ? 1 : 0)
    : 0

  return (
    <div className="flex h-full flex-col">
      <div
        className={cn(
          'flex h-12 shrink-0 items-center border-b border-border',
          collapsed ? 'justify-center px-0' : 'px-3',
        )}
      >
        {collapsed ? <BrandMark className="text-foreground" /> : <Wordmark />}
      </div>

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
              className="h-8 w-full rounded-md border border-border bg-background pr-7 pl-8 text-sm outline-none placeholder:text-muted-foreground focus-visible:ring-2 focus-visible:ring-ring [&::-webkit-search-cancel-button]:appearance-none"
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
          className="flex flex-col gap-4 p-2 pb-6"
        >
          {HUB_ORDER.map((hub) => {
            const items = hubs[hub].filter(
              (v: FeatureView) =>
                (!v.permission || can(v.permission)) && matches(v),
            )
            if (items.length === 0) return null
            // While filtering, a collapsed hub must still show its hits — otherwise
            // the search reports matches the operator cannot see, which reads as the
            // search being broken.
            const hubCollapsed =
              !collapsed && !filtering && collapsedGroups.includes(hub)
            return (
              <div key={hub} className="flex flex-col gap-0.5">
                {!collapsed && (
                  <button
                    type="button"
                    onClick={() => toggleNavGroup(hub)}
                    aria-expanded={!hubCollapsed}
                    aria-controls={`nav-group-${hub}`}
                    className="flex items-center justify-between rounded px-2.5 pb-1 text-[0.6875rem] font-medium tracking-wider text-muted-foreground uppercase outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring"
                  >
                    <span>{t(`hubs.${hub}`)}</span>
                    <ChevronDown
                      aria-hidden="true"
                      className={cn(
                        'size-3 transition-transform',
                        hubCollapsed && '-rotate-90',
                      )}
                    />
                  </button>
                )}
                <div
                  id={`nav-group-${hub}`}
                  className={cn(
                    'flex flex-col gap-0.5',
                    hubCollapsed && 'hidden',
                  )}
                >
                  {items.map((v) => (
                    <NavItem
                      key={v.id}
                      to={v.path}
                      icon={v.icon}
                      label={t(`items.${v.id}`)}
                      exact={v.path === '/'}
                      collapsed={collapsed}
                      onNavigate={onNavigate}
                    />
                  ))}
                </div>
              </div>
            )
          })}
          {filtering && hits === 0 && (
            <p className="px-2.5 py-4 text-sm text-muted-foreground">
              {t('filter.empty', { query: query.trim() })}
            </p>
          )}
        </nav>
      </ScrollArea>

      {/* Settings is a pinned utility, not a registry view — but it IS a link, so while
          filtering it must obey the filter too. Leaving it always visible made the
          sr-only count say "0" with one link still on screen. */}
      {(!filtering || fold(t('items.settings')).includes(needle)) && (
        <div className="shrink-0 border-t border-border p-2">
          <NavItem
            to="/settings"
            icon={Settings}
            label={t('items.settings')}
            collapsed={collapsed}
            onNavigate={onNavigate}
          />
        </div>
      )}
    </div>
  )
}

/** The persistent desktop sidebar (≥ lg). Collapses to an icon rail with tooltips. */
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
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation(['nav', 'common'])
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="left" className="w-64 p-0">
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
