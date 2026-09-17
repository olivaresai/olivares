// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { FavoriteButton } from './personal-navigation'
import { Link, useRouterState } from '@tanstack/react-router'
import { ChevronLeft, CircleHelp, Menu, Search } from 'lucide-react'
import { Fragment, type Ref } from 'react'
import { useTranslation } from 'react-i18next'
import {
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbLink,
  BreadcrumbList,
  BreadcrumbPage,
  BreadcrumbSeparator,
} from '@/components/ui/breadcrumb'
import { Button } from '@/components/ui/button'
import { Kbd } from '@/components/ui/kbd'
import {
  breadcrumbTrail,
  currentViewId as resolveViewId,
  resolveLocation,
} from '@/features/navigation/model'
import { useCommandStore } from '@/stores/command'
import { NotificationBell } from './notification-bell'
import { ThemeToggle } from './theme-toggle'
import { TenantSwitcher } from './tenant-switcher'
import { UserMenu } from './user-menu'
import { WorkspaceSwitcher } from './workspace-switcher'

/**
 * Where the help icon sends the operator.
 *
 * ⛔ HISTORY, kept because it is the reason the switch below exists at all. Until 2026-08-19 this
 * was `https://docs.olivares.ai` and that name had been WITHDRAWN from DNS (NXDOMAIN, while the
 * same `getent` run resolved the apex), so all 38 help destinations failed before they were even a
 * 404. Repointing the base to olivares.ai/docs was measured before choosing: the 38 `helpHref`
 * values are Diátaxis paths belonging to `docs-site/`, and the marketing site under
 * olivares.ai/docs has a different information architecture — **0 of the 38 existed there**. So
 * the icon went to the docs index, the per-view paths stayed in the registry because they ARE the
 * mapping, and the comment named the condition to flip on: *the day docs-site ships at this base*.
 *
 * ⇒ THAT DAY WAS 2026-08-23, and the condition is MET as of 2026-08-27. docs-site is deployed as
 * the `olivares-docs` Worker and `docs.olivares.ai` serves it. Measured this session, not assumed
 * and not taken from another lane's report: the 37 non-root `helpHref` values of
 * `web/src/features/registry.tsx`, requested one by one against `https://docs.olivares.ai<path>/`,
 * answered **37/37 = 200, zero non-200**. The base moves back and the deep links go on.
 *
 * `registry-help.test.ts` is what keeps the 38 honest against the docs tree, and it is the thing
 * to read before touching either constant.
 */
const DOCS_BASE = 'https://docs.olivares.ai'

/**
 * Are the per-view docs pages published AT `DOCS_BASE`? Flipped to `true` on 2026-08-27 on the
 * measurement above (37/37 live). Flip it back the moment that stops being true — a precise link
 * that 404s is worse than a general one that works, which is the whole trade this pair encodes.
 */
const DEEP_LINKS_PUBLISHED = true

/**
 * The registry id the breadcrumb names for a pathname. Kept as this module's export for its
 * existing callers and tests; the resolution itself lives in features/navigation/model.ts
 * beside the area model, so the trail and the sidebar can never disagree about where a
 * path sits. A resolved `/session-viewer/<id>` still names its view (the fix) — the
 * model matches the static prefix up to the first param.
 */
export function currentViewId(pathname: string): string | null {
  return resolveViewId(pathname)
}

/**
 * LAYOUT CONTRACT, measured on the rendered console (console-ui-current-baseline,
 * 2026-09-06, `captures/demo/console-light-en-d1024.png` and `*-m390.png`):
 *
 * - The bar was a single `h-12` flex row and the breadcrumb list wrapped. At 1024 px
 *   (sidebar open, 784 px of content) "Overview › Control console" broke onto two
 *   lines inside the 48 px bar; at 390 px it painted OVER the page header. Now the
 *   breadcrumb is one truncating line (`ui/breadcrumb.tsx`): the parent crumb gives
 *   way first (`shrink-[3]`), the page crumb truncates last, and below `sm` the
 *   first parent collapses to an icon link that KEEPS its accessible name — nothing is
 *   hidden, it is condensed.
 * - Below `lg` (where the sidebar is a drawer) the bar is two fixed rows: the first
 *   holds the menu, the trail and every icon action (search, help, notifications,
 *   theme, account); the second holds the CONTEXT — organization + workspace
 *   switchers — with the whole width to themselves, so tenant and workspace identity
 *   stay READABLE at 390 px instead of being cut to three letters or pushed off the
 *   right edge. The wrapper that carries the second row is `lg:contents`, so at `lg`
 *   and above its children rejoin the single row exactly where they sit in the DOM
 *   (between notifications and theme, the order the console has always had) — one
 *   DOM instance of every control, no duplicates.
 *   Measured trade-off, stated so nobody rediscovers it: below `lg` the Tab sequence
 *   is menu → trail → search → help → notifications → organization → workspace →
 *   theme → account, i.e. it visits the context row before returning to the last
 *   two icons of row one. The alternative (context row after account in the DOM)
 *   keeps the sequence strictly by row but moves the switchers to the far right of
 *   the desktop bar, after the avatar, on every screen. These controls are
 *   independent of each other, so the sequence still preserves meaning and
 *   operability (WCAG 2.4.3); the desktop arrangement was kept.
 * - The search button keeps its icon and accessible name everywhere; its text label
 *   and ⌘K hint show from `xl`, because at exactly 1024 px they are what squeezed the
 *   trail below its minimum width.
 * - The bar is `shrink-0` in the shell's column and `main` scrolls below it, so its
 *   bounds never overlap content at any of the three viewports.
 *
 * TRAIL (N1): resolved by features/navigation/model.ts from the registry and the area
 * model, never by splitting the url. `Overview` alone at `/`; `Overview › Area` on a
 * directory page (the area IS the location, with an explicit way home); `Area › Module`
 * on a module; `Area › Parent › Detail` on a deep-link-only detail; `System & settings ›
 * Settings` on the utility. Ancestors link, the page does not, and sections — grouping
 * labels, not pages — never appear.
 */
export function Topbar({
  onMenuClick,
  menuButtonRef,
}: {
  onMenuClick: () => void
  /** The drawer gives focus back to this button when it closes (MobileNav). */
  menuButtonRef?: Ref<HTMLButtonElement>
}) {
  const { t } = useTranslation(['nav', 'common'])
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const setCommandOpen = useCommandStore((s) => s.setOpen)

  const location = resolveLocation(pathname)
  const trail = breadcrumbTrail(t, location)
  const parents = trail.slice(0, -1)
  const page = trail[trail.length - 1]
  // Contextual help: the current view's Diátaxis page on the docs site; a directory
  // page opens the console reference, which lists every screen.
  const helpHref =
    location.kind === 'view' || location.kind === 'home'
      ? location.view.helpHref
      : location.kind === 'area'
        ? location.area.helpHref
        : undefined

  return (
    <header
      data-slot="topbar"
      className="flex shrink-0 flex-wrap items-center gap-x-1 gap-y-0 border-b border-border bg-surface px-3 sm:gap-x-2 lg:flex-nowrap print:hidden"
    >
      <Button
        ref={menuButtonRef}
        variant="ghost"
        size="icon"
        className="lg:hidden"
        onClick={onMenuClick}
        aria-label={t('common:actions.openMenu')}
      >
        <Menu />
      </Button>

      <Breadcrumb className="flex min-h-12 min-w-24 flex-1 items-center">
        <BreadcrumbList>
          {parents.map((crumb, i) => (
            <Fragment key={`${crumb.to ?? ''}:${i}`}>
              {/* The parent crumb shrinks three times faster than the page crumb, so
                  the ancestor is what gets the ellipsis first; below `sm` the FIRST
                  ancestor is the icon link — same href, same accessible name. Both
                  variants are a 24 px target (WCAG 2.5.8): `size-6` for the icon,
                  `leading-6` for the text line — the baseline's only axe finding at
                  390 px was this link at 22 px. */}
              <BreadcrumbItem className="shrink-0 sm:shrink-[3]">
                {i === 0 ? (
                  <BreadcrumbLink
                    asChild
                    className="inline-flex size-6 items-center justify-center sm:hidden"
                  >
                    <Link
                      to={crumb.to as never}
                      aria-label={crumb.label}
                      title={crumb.label}
                    >
                      <ChevronLeft className="size-4" aria-hidden="true" />
                    </Link>
                  </BreadcrumbLink>
                ) : null}
                <BreadcrumbLink asChild className={cnParent(i === 0)}>
                  <Link to={crumb.to as never}>{crumb.label}</Link>
                </BreadcrumbLink>
              </BreadcrumbItem>
              <BreadcrumbSeparator
                className={i === 0 ? 'hidden sm:inline-flex' : undefined}
              />
            </Fragment>
          ))}
          <BreadcrumbItem>
            <BreadcrumbPage className="font-display">
              {page?.label ?? ''}
            </BreadcrumbPage>
          </BreadcrumbItem>
        </BreadcrumbList>
      </Breadcrumb>

      <button
        type="button"
        onClick={() => setCommandOpen(true)}
        aria-label={t('common:actions.search')}
        className="inline-flex h-8 shrink-0 items-center gap-2 rounded-md border border-border-strong bg-surface px-2.5 text-sm text-muted-foreground outline-none transition-colors hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring"
      >
        <Search className="size-4" aria-hidden />
        <span className="hidden xl:inline">{t('common:actions.search')}…</span>
        <Kbd className="ml-1 hidden xl:inline-flex">⌘K</Kbd>
      </button>

      <FavoriteButton />

      {helpHref ? (
        <Button asChild variant="ghost" size="icon">
          <a
            href={
              DEEP_LINKS_PUBLISHED && helpHref !== '/'
                ? `${DOCS_BASE}${helpHref}/`
                : DOCS_BASE
            }
            target="_blank"
            rel="noreferrer"
            aria-label={t('common:actions.help')}
          >
            <CircleHelp />
          </a>
        </Button>
      ) : null}

      <NotificationBell />

      {/* Row two below `lg` (`order-last basis-full`); dissolved into the single row
          at `lg` (`contents`), where its two children sit here, between the bell
          and the theme toggle. The negative horizontal margin lets its top hairline
          span the full bar width. */}
      <div
        data-slot="topbar-context"
        className="-mx-3 order-last flex h-10 min-w-0 basis-full items-center gap-x-2 border-t border-border px-3 lg:contents"
      >
        <TenantSwitcher />
        <WorkspaceSwitcher />
      </div>

      <ThemeToggle />
      <UserMenu />
    </header>
  )
}

/** The text rendering of a parent crumb: the first one hides below `sm` (its icon twin
 * shows instead); later ancestors — a detail's parent — stay text at every width.
 * `min-w-6` + `inline-block`: an area name can be two letters ("AI"/"IA"), and a 14 px
 * wide link is below the 24 px pointer target (WCAG 2.5.8) — axe measured exactly that on
 * the built console (console-navigation-n1, 2026-09-06, `/agentops`, 14×24 px). The old
 * parent crumb was always "Overview", so the case never existed before the areas. */
function cnParent(first: boolean): string {
  return first
    ? 'hidden min-w-6 leading-6 sm:inline-block'
    : 'inline-block min-w-6 leading-6'
}
