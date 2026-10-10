// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { FavoriteButton } from './personal-navigation'
import { Link, useRouterState } from '@tanstack/react-router'
import { ChevronLeft, CircleHelp, Search } from 'lucide-react'
import { Fragment } from 'react'
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
import { cn } from '@/lib/utils'
import { useServerInfo } from '@/lib/hooks/use-server-info'
import { useMinWidth } from '@/lib/hooks/use-min-width'
import {
  currentViewId as resolveViewId,
  resolveLocation,
  type Crumb,
} from '@/features/navigation/model'
import { useCommandStore } from '@/stores/command'
import { useWorkspaceStore } from '@/stores/workspace'
import { BrandMark } from './brand'
import { KillSwitchStatus } from './killswitch-status'
import { NotificationBell } from './notification-bell'
import { trailFor } from './shell-destinations'
import { SidePanelToggle } from './side-panel'
import { UserMenu } from './user-menu'

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
 * and not taken from a second-hand report: the 37 non-root `helpHref` values of
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

function useTrail(): Crumb[] {
  const { t } = useTranslation(['nav', 'common'])
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const search = useRouterState({
    select: (s) => (s.location.search ?? {}) as Record<string, unknown>,
  })
  const workspaceName = useWorkspaceStore((s) => s.activeWorkspaceName)
  return trailFor(t, resolveLocation(pathname), search, workspaceName)
}

/**
 * THE TOP BAR (redesign §3.2): one 52 px row at every width — breadcrumb · page state ·
 * page actions · panel toggles. Search moved to the sidebar beside New session, and the
 * account, theme and settings to the sidebar footer; below 761 px, where the sidebar is
 * replaced by the phone bar, the bar carries the brand mark, search and the account.
 *
 * `page-state` and `page-actions` are the places a screen fills (the session's "Working ·
 * 6m 12s", its Pause and Stop); the frame reserves them and draws nothing of its own.
 *
 * The breadcrumb is one truncating line (`ui/breadcrumb.tsx`): the parent crumb gives way
 * first, the page crumb truncates last, and below `sm` the first parent that links
 * collapses to an icon link that keeps its accessible name.
 */
export function Topbar() {
  const { t } = useTranslation(['nav', 'common'])
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const setCommandOpen = useCommandStore((s) => s.setOpen)
  const info = useServerInfo()
  const version = useMinWidth(761) ? undefined : info.data?.version

  const location = resolveLocation(pathname)
  const trail = useTrail()
  const parents = trail.slice(0, -1)
  const page = trail[trail.length - 1]
  // Contextual help: the current view's documentation page; a directory page opens
  // the console reference, which lists every screen.
  const helpHref =
    location.kind === 'view' || location.kind === 'home'
      ? location.view.helpHref
      : location.kind === 'area'
        ? location.area.helpHref
        : undefined

  return (
    <header
      data-slot="topbar"
      className="flex h-13 min-h-13 shrink-0 flex-nowrap items-center gap-x-1 border-b border-line bg-canvas pr-3 pl-5 max-[760px]:pl-3 sm:gap-x-2 print:hidden"
    >
      <div
        data-testid={version ? 'deployment-identity' : undefined}
        className="flex shrink-0 flex-col items-center min-[761px]:hidden"
      >
        <Link
          to={'/' as never}
          aria-label={t('nav:shell.brandHome')}
          className="grid size-8 shrink-0 place-items-center rounded-ctl text-text outline-none focus-visible:ring-2 focus-visible:ring-focus"
        >
          <BrandMark />
        </Link>
        {version ? (
          <span className="block shrink-0 whitespace-nowrap font-mono text-mono-s text-text-2">
            {version}
          </span>
        ) : null}
      </div>

      <Breadcrumb className="flex h-full min-w-24 flex-1 items-center">
        <BreadcrumbList className="text-body font-medium">
          {parents.map((crumb, i) => (
            <Fragment key={`${crumb.to ?? ''}:${i}`}>
              {/* The parent crumb shrinks three times faster than the page crumb; below
                  `sm` the FIRST linking ancestor is the icon link — same href, same
                  accessible name, and a 24 px target (WCAG 2.5.8). */}
              <BreadcrumbItem className="shrink-0 sm:shrink-[3]">
                {i === 0 && crumb.to ? (
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
                {crumb.to ? (
                  <BreadcrumbLink asChild className={cnParent(i === 0)}>
                    <Link to={crumb.to as never}>{crumb.label}</Link>
                  </BreadcrumbLink>
                ) : (
                  <span
                    className={cn(
                      'inline-block min-w-6 truncate leading-6 text-text-2',
                      i === 0 && 'max-[639px]:hidden',
                    )}
                    title={crumb.label}
                  >
                    {crumb.label}
                  </span>
                )}
              </BreadcrumbItem>
              {/* The design's slash, quiet: the trail reads as a path. */}
              <BreadcrumbSeparator
                className={cn(
                  'px-0.5 text-text-3',
                  i === 0 && 'hidden sm:inline-flex',
                )}
              >
                /
              </BreadcrumbSeparator>
            </Fragment>
          ))}
          <BreadcrumbItem>
            <BreadcrumbPage className="text-text">
              {page?.label ?? ''}
            </BreadcrumbPage>
          </BreadcrumbItem>
        </BreadcrumbList>
      </Breadcrumb>

      <div
        data-slot="page-state"
        className="flex min-w-0 items-center empty:hidden"
      />
      <div
        data-slot="page-actions"
        className="flex shrink-0 items-center gap-2 empty:hidden"
      />

      <KillSwitchStatus />

      <button
        type="button"
        onClick={() => setCommandOpen(true)}
        aria-label={t('nav:shell.searchCommands')}
        className="grid size-8 shrink-0 place-items-center rounded-ctl text-text-2 outline-none hover:bg-hover hover:text-text focus-visible:ring-2 focus-visible:ring-focus min-[761px]:hidden"
      >
        <Search className="size-4" aria-hidden />
      </button>

      <FavoriteButton />
      <NotificationBell />

      {helpHref ? (
        <Button
          asChild
          variant="ghost"
          size="icon"
          className="max-[760px]:hidden"
        >
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

      <SidePanelToggle />

      <span className="min-[761px]:hidden">
        <UserMenu />
      </span>
    </header>
  )
}

/** The text rendering of a parent crumb that links: the first one hides below `sm` (its
 * icon twin shows instead); later ancestors stay text at every width. `min-w-6` +
 * `inline-block`: an area name can be two letters ("AI"/"IA"), and a 14 px wide link is
 * below the 24 px pointer target (WCAG 2.5.8). */
function cnParent(first: boolean): string {
  return first
    ? 'hidden min-w-6 leading-6 text-text-2 sm:inline-block'
    : 'inline-block min-w-6 leading-6 text-text-2'
}
