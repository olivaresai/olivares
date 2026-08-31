// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Link, useRouterState } from '@tanstack/react-router'
import { CircleHelp, Menu, Search } from 'lucide-react'
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
import { FEATURE_VIEWS } from '@/features/registry'
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

/** Resolve the longest registry path that prefixes the current location — the
 * active top-level section — so the breadcrumb names the current view. Registry
 * paths may carry a dynamic segment (e.g. `/session-viewer/$id`); match on the
 * static prefix up to the first param, otherwise a resolved path like
 * `/session-viewer/sess-a11y` matches nothing and the breadcrumb renders blank
 * (a visible bug AND an axe aria-command-name violation on the empty crumb —).*/
function matchBase(path: string): string {
  const paramAt = path.indexOf('/$')
  return paramAt === -1 ? path : path.slice(0, paramAt)
}

export function currentViewId(pathname: string): string | null {
  if (pathname === '/settings') return 'settings'
  let best: { id: string; len: number } | null = null
  for (const v of FEATURE_VIEWS) {
    if (v.path === '/') continue
    const base = matchBase(v.path)
    if (pathname === base || pathname.startsWith(`${base}/`)) {
      if (!best || base.length > best.len) best = { id: v.id, len: base.length }
    }
  }
  return best?.id ?? (pathname === '/' ? 'home' : null)
}

export function Topbar({ onMenuClick }: { onMenuClick: () => void }) {
  const { t } = useTranslation(['nav', 'common'])
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const setCommandOpen = useCommandStore((s) => s.setOpen)

  const id = currentViewId(pathname)
  const title = id ? t(`items.${id}`) : ''
  const atRoot = pathname === '/'
  // Contextual help: the current view's Diátaxis page on the docs site.
  const helpHref = FEATURE_VIEWS.find((v) => v.id === id)?.helpHref

  return (
    <header className="flex h-12 shrink-0 items-center gap-2 border-b border-border bg-surface px-3 print:hidden">
      <Button
        variant="ghost"
        size="icon"
        className="lg:hidden"
        onClick={onMenuClick}
        aria-label={t('common:actions.openMenu')}
      >
        <Menu />
      </Button>

      <Breadcrumb className="min-w-0 flex-1">
        <BreadcrumbList>
          {!atRoot && (
            <>
              <BreadcrumbItem>
                <BreadcrumbLink asChild>
                  <Link to="/">{t('items.home')}</Link>
                </BreadcrumbLink>
              </BreadcrumbItem>
              <BreadcrumbSeparator />
            </>
          )}
          <BreadcrumbItem>
            <BreadcrumbPage className="font-display">{title}</BreadcrumbPage>
          </BreadcrumbItem>
        </BreadcrumbList>
      </Breadcrumb>

      <button
        type="button"
        onClick={() => setCommandOpen(true)}
        aria-label={t('common:actions.search')}
        className="inline-flex h-8 items-center gap-2 rounded-md border border-border-strong bg-surface px-2.5 text-sm text-muted-foreground outline-none transition-colors hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring"
      >
        <Search className="size-4" aria-hidden />
        <span className="hidden md:inline">{t('common:actions.search')}…</span>
        <Kbd className="ml-1 hidden md:inline-flex">⌘K</Kbd>
      </button>

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
      <TenantSwitcher />
      <WorkspaceSwitcher />
      <ThemeToggle />
      <UserMenu />
    </header>
  )
}
