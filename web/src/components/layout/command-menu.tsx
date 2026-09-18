// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { LogOut, Moon, Search, Sun } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  CommandDialog,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandSeparator,
} from '@/components/ui/command'
import { usePersonalNavigation } from '@/features/navigation/personal-navigation'
import {
  dispatchable,
  useCommandActionAuthority,
} from '@/features/navigation/command-actions'
import { useViewAccess } from '@/features/navigation/authorization'
import { useAuth } from '@/lib/auth/context'
import {
  SEARCH_KIND_FEATURE,
  SEARCH_KIND_ROUTES,
  searchConsole,
  searchKeys,
} from '@/lib/api/search'
import { FEATURE_VIEWS, type FeatureView } from '@/features/registry'
import {
  authorizedEntries,
  buildNavSearchIndex,
  fold,
  rankNavMatches,
  type NavSearchEntry,
} from '@/features/navigation/model'
import { useCommandStore } from '@/stores/command'
import { useThemeStore } from '@/stores/theme'

/** Minimum query length before the federated search fires. */
const SEARCH_MIN_CHARS = 2
/** Debounce so fast typing does not fan out a request per keystroke. */
const SEARCH_DEBOUNCE_MS = 250

/** The ⌘K / Ctrl-K palette: jump to any visible area or module, run an action, change
 * theme, sign out — and search the tenant's own entities via the federated,
 * RBAC-aware GET /v1/search. Mounted once in the app shell; opened by
 * the shortcut or the topbar search.
 *
 * N1: the navigation half is the SAME index and the SAME ranking the sidebar filter uses
 * (features/navigation/model.ts) — exact label first, then label prefix and words, then
 * former names and the English label, then path, then area/section/noun vocabulary, then
 * description; ties in registry order. cmdk's own fuzzy re-ordering is switched off
 * (`shouldFilter={false}`) so the two surfaces can never disagree about what "session"
 * finds. Each result shows `Area › Section` so two doors into one screen stay
 * distinguishable. Hidden (deep-link-only) views are never offered. */
export function CommandMenu() {
  const { t } = useTranslation(['nav', 'common', 'auth'])
  const open = useCommandStore((s) => s.open)
  const setOpen = useCommandStore((s) => s.setOpen)

  // ⛔ ⌘K IS NOT HANDLED HERE ANY MORE. It is a row of the declared keybinding
  //    table, resolved by `GlobalShortcuts` — the console's one keyboard authority —
  //    so the chord an operator presses, the row a test enumerates and the line the
  //    help page prints are the same single fact. A second listener here would have
  //    toggled the palette twice.

  // Closing gives focus back to the control that opened the palette (Escape, outside
  // click, or a selection whose navigation did not move focus itself). The dialog's own
  // restore left focus on <body> on the built console, measured 2026-09-06; the store
  // remembered the opener at open time, and this is the one place it is consumed.
  useEffect(() => {
    if (open) return
    const el = useCommandStore.getState().takeOpener()
    if (!el || !el.isConnected) return
    const id = window.setTimeout(() => {
      if (document.activeElement === document.body || !document.activeElement)
        el.focus()
    }, 0)
    return () => window.clearTimeout(id)
  }, [open])

  return (
    <CommandDialog
      open={open}
      onOpenChange={setOpen}
      title={t('common:commandPalette.placeholder')}
      description={t('common:commandPalette.navigation')}
      shouldFilter={false}
      onCloseAutoFocus={(e) => {
        const el = useCommandStore.getState().opener
        if (el && el.isConnected) {
          e.preventDefault()
          el.focus()
        }
      }}
    >
      {/* The dialog unmounts its content when it closes, so the query and the search
          state below start clean on every open without a reset effect. */}
      <PaletteBody />
    </CommandDialog>
  )
}

function PaletteBody() {
  const { t } = useTranslation(['nav', 'common', 'auth'])
  const setOpen = useCommandStore((s) => s.setOpen)
  const navigate = useNavigate()
  const { activeTenant, logout } = useAuth()
  const { navigable } = useViewAccess()
  // The verbs' own authority, which is NOT the view's (see features/navigation/command-actions).
  const { authorized: mayRun, capture } = useCommandActionAuthority()
  const personal = usePersonalNavigation()
  const setTheme = useThemeStore((s) => s.setTheme)
  const [query, setQuery] = useState('')
  const [debounced, setDebounced] = useState('')

  useEffect(() => {
    const id = window.setTimeout(() => setDebounced(query), SEARCH_DEBOUNCE_MS)
    return () => window.clearTimeout(id)
  }, [query])

  const term = debounced.trim()
  const searchQ = useQuery({
    queryKey: searchKeys.query(activeTenant, term),
    queryFn: () => searchConsole(term),
    // GET /v1/search resolves a tenant like every other scoped route
    // (core/api/search.go handleSearch), so with none selected it can only answer
    // 400 "tenant required". The palette still navigates — only the data search
    // is held back until there is a tenant to search IN.
    enabled: term.length >= SEARCH_MIN_CHARS && !!activeTenant,
    staleTime: 30_000,
    retry: false,
  })

  const go = (to: string) => {
    setOpen(false)
    void navigate({ to: to as never })
  }

  // Same visibility rule as the sidebar, and now literally the same predicate: the
  // shared projection + never a hideInNav view. A hidden view is
  // parameterized/deep-link-only (e.g. /session-viewer/$id) — navigating to its literal
  // path would 404 on the placeholder segment.
  const views = FEATURE_VIEWS.filter(
    (v: FeatureView) => !v.hideInNav && navigable(v),
  )

  // The shared index, narrowed to what THIS principal may open (the same projection the
  // sidebar filter uses), ranked by the query.
  const index = useMemo(() => buildNavSearchIndex(t), [t])
  const authorized = useMemo(
    () => authorizedEntries(index, navigable),
    [index, navigable],
  )
  const ranked = useMemo(
    () => rankNavMatches(authorized, query),
    [authorized, query],
  )
  const querying = query.trim().length > 0
  // Without a query the palette is a table of contents: the areas, then every module.
  // With one it is a single list in rank order, areas interleaved and tagged, because
  // splitting a ranked list into two groups would put a weak area hit above an exact
  // module hit and the ranking the sidebar shares would stop meaning anything.
  const favoriteHits = querying
    ? []
    : (personal?.favorites ?? []).flatMap((link) => {
        const entry = ranked.find((e) => e.id === link.id && e.kind !== 'area')
        return entry ? [entry] : []
      })
  const recentHits = querying
    ? []
    : (personal?.recents ?? []).flatMap((link) => {
        const entry = ranked.find((e) => e.id === link.id && e.kind !== 'area')
        return entry ? [entry] : []
      })
  const areaHits = querying ? [] : ranked.filter((e) => e.kind === 'area')
  const navHits = querying ? ranked : ranked.filter((e) => e.kind !== 'area')

  // The non-navigation entries (actions, theme, sign-out) follow a plain folded-substring
  // rule against their own label: they are not modules and have no area to rank by.
  const needle = fold(query.trim())
  const shows = (label: string) => !needle || fold(label).includes(needle)

  const searchHits = searchQ.data?.results ?? []

  const renderNav = (e: NavSearchEntry) => {
    const Icon = e.icon
    return (
      <CommandItem
        key={`${e.kind}:${e.id}`}
        value={`${e.kind}:${e.id} ${e.label}`}
        onSelect={() => go(e.path)}
      >
        <Icon />
        <span className="flex min-w-0 flex-1 flex-col">
          <span className="truncate">{e.label}</span>
          {e.description ? (
            <span className="truncate text-caption text-muted-foreground">
              {e.description}
            </span>
          ) : null}
        </span>
        <span className="ml-2 shrink-0 text-caption text-muted-foreground">
          {e.kind === 'area' ? t('nav:directory.area') : e.context}
        </span>
      </CommandItem>
    )
  }

  // A VERB IS OFFERED BY ITS OWN PERMISSION, NEVER BY THE PAGE'S. `views` is already
  // filtered by `navigable` — the parent's read authority, which stays required because a
  // verb that lands on a page this principal cannot open is no better than a dead end —
  // and each action is then filtered by `authorized`: its declared mutation permission and
  // an established tenant to write in. Until 2026-09-11 the second filter did not exist,
  // and `notify:route:read` alone bought "New alert route" (spec04 §1).
  const actionItems = views
    .filter((v) => v.commandActions?.length)
    .flatMap((v) =>
      (v.commandActions ?? []).map((action) => {
        if (!mayRun(action)) return null
        const label = t(`nav:commandActions.${v.id}.${action.id}`, {
          defaultValue: '',
        })
        if (!label || !shows(label)) return null
        return (
          <CommandItem
            key={`${v.id}:${action.id}`}
            value={`action:${v.id}:${action.id} ${label}`}
            onSelect={() => {
              // RE-ASKED AT SELECTION, against the identity that is live in this
              // handler: the list was built in an earlier render, and a movement between
              // that render and this keystroke must not dispatch. With no established
              // context — or none with a tenant to write in — there is nothing to bind the
              // command to, so nothing is queued and nothing is navigated to: an
              // unbindable command would either be unusable or, if the match were relaxed
              // to compensate, consumable by whoever arrived next.
              //
              // AND THE PALETTE STAYS OPEN. Closing it would spend the operator's ⌘K on
              // nothing and hide the correction: this path is reached by a movement, and
              // the movement re-renders the list without the verb. Leaving them where they
              // are, in front of a list that fixes itself, is the smaller surprise.
              const context = capture()
              if (!mayRun(action) || !dispatchable(context)) return
              useCommandStore
                .getState()
                .setPendingAction(v.id, action.id, context)
              go(v.path)
            }}
          >
            <v.icon />
            {label}
          </CommandItem>
        )
      }),
    )
    .filter((x) => x !== null)
  const lightLabel = t('common:theme.light')
  const darkLabel = t('common:theme.dark')
  const signOutLabel = t('auth:account.signOut')
  const themeLabel = t('common:theme.label')
  const showLight = shows(`${themeLabel} ${lightLabel}`)
  const showDark = shows(`${themeLabel} ${darkLabel}`)
  const showSignOut = shows(signOutLabel)
  const anyAction =
    actionItems.length > 0 || showLight || showDark || showSignOut

  return (
    <>
      <CommandInput
        placeholder={t('common:commandPalette.placeholder')}
        value={query}
        onValueChange={setQuery}
      />
      <CommandList label={t('common:commandPalette.results')}>
        <CommandEmpty>{t('common:commandPalette.empty')}</CommandEmpty>

        {searchHits.length > 0 ? (
          <>
            <CommandGroup heading={t('common:commandPalette.searchResults')}>
              {searchHits.map((hit) => {
                const featureId = SEARCH_KIND_FEATURE[hit.kind]
                const route = SEARCH_KIND_ROUTES[hit.kind]
                const view = views.find((v) => v.id === featureId)
                const Icon = view?.icon ?? Search
                if (!route || (featureId && !view)) return null
                return (
                  <CommandItem
                    key={`${hit.kind}:${hit.id}`}
                    value={`search ${hit.kind} ${hit.id} ${hit.name}`}
                    keywords={[query]}
                    onSelect={() => go(route)}
                  >
                    <Icon />
                    <span className="min-w-0 flex-1 truncate">{hit.name}</span>
                    <span className="ml-2 shrink-0 text-caption text-muted-foreground">
                      {featureId ? t(`nav:items.${featureId}`) : hit.kind}
                      {hit.detail ? ` · ${hit.detail}` : ''}
                    </span>
                  </CommandItem>
                )
              })}
            </CommandGroup>
            {searchQ.data?.truncated ? (
              <p className="px-3 pb-1 text-caption text-muted-foreground">
                {t('common:commandPalette.searchTruncated')}
              </p>
            ) : null}
            <CommandSeparator />
          </>
        ) : null}

        {/* A DEGRADED SEARCH IS NOT A TRUNCATED ONE, AND IT RENDERS OUTSIDE THE HITS BLOCK.
            Truncated means "narrow your query"; degraded means a source failed and this list
            is missing whatever it held.

            It sits here, not inside `searchHits.length > 0`, because of where it was first
            put — and the adversarial contrast of 2026-08-06 caught that placement the same
            day. Nested under the hits, the warning appeared only when something ELSE had
            matched, so the one case that matters most was silent: when the failed provider
            was the only one that would have matched, `{results: [], degraded: true}` drew
            the ordinary "no results" screen. An incomplete list presented as an empty one is
            precisely the defect the flag was added to remove, surviving in the UI after the
            API had been fixed.

            Destructive tone rather than muted: it is a failure, not a hint. */}
        {searchQ.data?.degraded ? (
          <p className="px-3 pb-2 pt-1 text-caption text-destructive">
            {t('common:commandPalette.searchDegraded')}
          </p>
        ) : null}

        {favoriteHits.length > 0 ? (
          <CommandGroup heading={t('nav:personal.favorites')}>
            {favoriteHits.map((e) =>
              renderNav({ ...e, id: `favorite:${e.id}` }),
            )}
          </CommandGroup>
        ) : null}

        {recentHits.length > 0 ? (
          <CommandGroup heading={t('nav:personal.recents')}>
            {recentHits.map((e) => renderNav({ ...e, id: `recent:${e.id}` }))}
          </CommandGroup>
        ) : null}

        {areaHits.length > 0 ? (
          <CommandGroup heading={t('nav:directory.areas')}>
            {areaHits.map(renderNav)}
          </CommandGroup>
        ) : null}

        {navHits.length > 0 ? (
          <CommandGroup heading={t('common:commandPalette.navigation')}>
            {navHits.map(renderNav)}
          </CommandGroup>
        ) : null}

        {anyAction ? <CommandSeparator /> : null}

        {anyAction ? (
          <CommandGroup heading={t('common:commandPalette.actions')}>
            {actionItems}
            {showLight ? (
              <CommandItem
                value={`theme:light ${themeLabel} ${lightLabel}`}
                onSelect={() => {
                  setTheme('light')
                  setOpen(false)
                }}
              >
                <Sun />
                {lightLabel}
              </CommandItem>
            ) : null}
            {showDark ? (
              <CommandItem
                value={`theme:dark ${themeLabel} ${darkLabel}`}
                onSelect={() => {
                  setTheme('dark')
                  setOpen(false)
                }}
              >
                <Moon />
                {darkLabel}
              </CommandItem>
            ) : null}
            {showSignOut ? (
              <CommandItem
                value={`signout ${signOutLabel}`}
                onSelect={() => {
                  setOpen(false)
                  void logout()
                }}
              >
                <LogOut />
                {signOutLabel}
              </CommandItem>
            ) : null}
          </CommandGroup>
        ) : null}
      </CommandList>
    </>
  )
}
