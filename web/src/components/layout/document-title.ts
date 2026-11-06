// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE DOCUMENT TITLE: "<Page> · Olivares AI" on every route (WCAG 2.4.2), so a tab, a
// history entry and a screen reader's page announcement name the page. The top bar names
// the page's place instead (topBarLabel). A page is named by its registry label, except the
// page a place of the left navigation opens for this person (the place you click is the
// place you land on), which is named by the place; so is an address that is a place without
// being a page of its own (License). The one writer of document.title: session
// notifications put their count in front through setAttentionCount.
import { useRouterState } from '@tanstack/react-router'
import type { TFunction } from 'i18next'
import { useEffect, useReducer } from 'react'
import { useTranslation } from 'react-i18next'
import { create } from 'zustand'
import { isBusinessOnlyPath } from '@/features/business-build'
import { ROUTE_ALIASES } from '@/features/registry'
// The status-page title reads `health:`, and every route pays for this title, so the
// namespace is registered here, in the root closure, not by the status page's chunk.
import '@/features/health/i18n'
import '@/features/identity/i18n'
import {
  areaLabel,
  resolveLocation,
  viewLabel,
  type ViewGate,
} from '@/features/navigation/model'
import { currentPlace, placeOpenedAt } from '@/features/navigation/places'

let page = ''
let attention = 0

/** Which pages the signed-in person may open, set by the shell: the root title sits above
 * the sign-in, but the page a place opens is the first one this person may open. */
const useTitleGate = create<{ gate?: ViewGate }>(() => ({}))

export function setTitleGate(gate?: ViewGate) {
  useTitleGate.setState({ gate })
}

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
  '/pair': 'identity:sessions.pairTitle',
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
  gate?: ViewGate,
): string {
  // The router serves /login/ as /login; the title reads the same page.
  pathname = pathname.replace(/(.)\/+$/, '$1')
  // A key whose dictionary has not landed yet reads '' (the bare brand), never the raw key.
  if (PUBLIC_PAGES[pathname])
    return t(PUBLIC_PAGES[pathname], { defaultValue: '' })
  // The root selects a stored start page; its destination supplies the title.
  if (pathname === '/') return ''
  const location = resolveLocation(pathname)
  if (location.kind === 'unknown') {
    // A retired address redirects before it paints: it reads as where it lands (#3388).
    const alias = ROUTE_ALIASES.find((a) => a.from === pathname)
    return alias
      ? pageLabel(t, alias.to, { ...search, ...alias.search }, gate)
      : notFoundLabel(t, pathname)
  }
  // Settings and every one of its sections, by the place's name.
  if (location.kind === 'settings') return t('nav:places.settings')
  if (location.kind === 'area') return areaLabel(t, location.area.id)
  // The page a sidebar place opens (the approval queue among them) is named by the place.
  const place = placeOpenedAt(pathname, search, gate)
  return place ? t(`nav:places.${place}`) : viewLabel(t, location.view.id)
}

/** The top bar's word: the place the page belongs to, whose heading names the page itself.
 * An address in no place (an area directory, a 404, a Business notice) has none: its
 * heading alone names it, so the same word never shows twice. */
export function topBarLabel(t: TFunction, pathname: string): string {
  const place = currentPlace(pathname)
  return place ? t(`nav:places.${place}`) : ''
}

/** Called once, by the root route, so every route (public, shell or 404) is named. */
export function useDocumentTitle() {
  const { t, i18n } = useTranslation([
    'nav',
    'auth',
    'health',
    'identity',
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
  const gate = useTitleGate((s) => s.gate)
  const label = notFound
    ? notFoundLabel(t, pathname)
    : pageLabel(t, pathname, search, gate)
  const brand = t('common:app.name')
  const title = label ? `${label} · ${brand}` : brand
  useEffect(() => setPageTitle(title), [title])
}
