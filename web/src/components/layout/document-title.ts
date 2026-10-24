// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE DOCUMENT TITLE: "<Page> · Olivares AI" on every route (WCAG 2.4.2), so a tab, a
// history entry and a screen reader's page announcement name the page. The page is the
// place label the top bar shows; a door (FeatureView.doorTo) is named by its own entrance,
// because it opens the room on its own tab. The one writer of document.title: session
// notifications put their count in front through setAttentionCount.
import { useRouterState } from '@tanstack/react-router'
import type { TFunction } from 'i18next'
import { useEffect, useReducer } from 'react'
import { useTranslation } from 'react-i18next'
import { isBusinessOnlyPath } from '@/features/business-build'
// The status-page title reads `health:`, and every route pays for this title, so the
// namespace is registered here, in the root closure, not by the status page's chunk.
import '@/features/health/i18n'
import {
  areaLabel,
  resolveLocation,
  SETTINGS_UTILITY,
  viewLabel,
} from '@/features/navigation/model'
import { destinationLabel, sectionAt } from './shell-destinations'

let page = ''
let attention = 0

function write() {
  if (page) document.title = attention ? `(${attention}) ${page}` : page
}

/** The page part of the title; the attention count stays in front of it. */
export function setPageTitle(title: string) {
  page = title
  write()
}

/** Sessions that need the person, shown as "(n) " in front of the page title. */
export function setAttentionCount(count: number) {
  attention = count
  write()
}

/** The public routes outside the shell, by the name each page goes by. */
const PUBLIC_PAGES: Readonly<Record<string, string>> = {
  '/login': 'auth:login.title',
  '/setup': 'auth:setup.title',
  '/accept-invite': 'auth:invite.title',
  // Its heading carries the brand already ("Olivares AI — Status").
  '/status-page': 'health:statusPageLink',
}

function notFoundLabel(t: TFunction, pathname: string): string {
  return isBusinessOnlyPath(pathname)
    ? t('errors:businessOnly.title')
    : t('errors:notFound.title')
}

/** The page a pathname names, or '' where the console has no label for it. */
export function pageLabel(
  t: TFunction,
  pathname: string,
  search: Readonly<Record<string, unknown>>,
): string {
  // The router serves /login/ as /login; the title reads the same page.
  pathname = pathname.replace(/(.)\/+$/, '$1')
  // A key whose dictionary has not landed yet reads '' (the bare brand), never the raw key.
  if (PUBLIC_PAGES[pathname])
    return t(PUBLIC_PAGES[pathname], { defaultValue: '' })
  const location = resolveLocation(pathname)
  if (location.kind === 'unknown') return notFoundLabel(t, pathname)
  if (location.kind === 'settings') return viewLabel(t, SETTINGS_UTILITY.id)
  if (location.kind === 'area') return areaLabel(t, location.area.id)
  if (location.kind === 'home') return viewLabel(t, location.view.id)
  if (location.view.doorTo) return viewLabel(t, location.view.id)
  const section = sectionAt(location.view.id, search)
  if (section) return t(`nav:shell.journeys.${section.key}`)
  return destinationLabel(t, location.view.id)
}

/** Called once, by the root route, so every route (public, shell or 404) is named. */
export function useDocumentTitle() {
  const { t, i18n } = useTranslation([
    'nav',
    'auth',
    'health',
    'errors',
    'common',
  ])
  // A dictionary fetched or registered after the root rendered re-reads the title.
  const [, dictionaryAdded] = useReducer((n: number) => n + 1, 0)
  useEffect(() => {
    i18n.store.on('added', dictionaryAdded)
    return () => i18n.store.off('added', dictionaryAdded)
  }, [i18n])
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const search = useRouterState({
    select: (s) => (s.location.search ?? {}) as Record<string, unknown>,
  })
  // The router's own 404 verdict: a path under a registry prefix (/audit/x) still 404s.
  const notFound = useRouterState({
    select: (s) => s.matches.some((m) => m._notFound === true),
  })
  const label = notFound
    ? notFoundLabel(t, pathname)
    : pageLabel(t, pathname, search)
  const brand = t('common:app.name')
  const title = label ? `${label} · ${brand}` : brand
  useEffect(() => setPageTitle(title), [title])
}
