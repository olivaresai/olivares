// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { LogOut, Moon, Search, Settings, Sun } from 'lucide-react'
import { useEffect, useState } from 'react'
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
import { useAuth } from '@/lib/auth/context'
import {
  SEARCH_KIND_FEATURE,
  SEARCH_KIND_ROUTES,
  searchConsole,
  searchKeys,
} from '@/lib/api/search'
import { FEATURE_VIEWS, type FeatureView } from '@/features/registry'
import { useCommandStore } from '@/stores/command'
import { useThemeStore } from '@/stores/theme'

/** Minimum query length before the federated search fires. */
const SEARCH_MIN_CHARS = 2
/** Debounce so fast typing does not fan out a request per keystroke. */
const SEARCH_DEBOUNCE_MS = 250

/** The ⌘K / Ctrl-K palette: jump to any visible module, run an action, change
 * theme, sign out — and search the tenant's own entities via the federated,
 * RBAC-aware GET /v1/search. Mounted once in the app shell; opened by
 * the shortcut or the topbar search. */
export function CommandMenu() {
  const { t } = useTranslation(['nav', 'common', 'auth'])
  const open = useCommandStore((s) => s.open)
  const setOpen = useCommandStore((s) => s.setOpen)
  const navigate = useNavigate()
  const { activeTenant, can, logout } = useAuth()
  const setTheme = useThemeStore((s) => s.setTheme)
  const [query, setQuery] = useState('')
  const [debounced, setDebounced] = useState('')

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        useCommandStore.getState().toggle()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  useEffect(() => {
    const id = window.setTimeout(() => setDebounced(query), SEARCH_DEBOUNCE_MS)
    return () => window.clearTimeout(id)
  }, [query])

  // Reset the query when the palette closes so reopening starts clean.
  useEffect(() => {
    if (!open) {
      setQuery('')
      setDebounced('')
    }
  }, [open])

  const term = debounced.trim()
  const searchQ = useQuery({
    queryKey: searchKeys.query(activeTenant, term),
    queryFn: () => searchConsole(term),
    // GET /v1/search resolves a tenant like every other scoped route
    // (core/api/search.go handleSearch), so with none selected it can only answer
    // 400 "tenant required". The palette still navigates — only the data search
    // is held back until there is a tenant to search IN.
    enabled: open && term.length >= SEARCH_MIN_CHARS && !!activeTenant,
    staleTime: 30_000,
    retry: false,
  })

  const go = (to: string) => {
    setOpen(false)
    void navigate({ to: to as never })
  }

  // Same visibility rule as the sidebar: RBAC + never a hideInNav view. A
  // hidden view is parameterized/deep-link-only (e.g. /session-viewer/$id) —
  // navigating to its literal path would 404 on the placeholder segment.
  const views = FEATURE_VIEWS.filter(
    (v: FeatureView) => !v.hideInNav && (!v.permission || can(v.permission)),
  )

  // A missing description key renders nothing rather than the raw key.
  const describe = (id: string) =>
    t(`nav:descriptions.${id}`, { defaultValue: '' })

  const searchHits = searchQ.data?.results ?? []

  return (
    <CommandDialog
      open={open}
      onOpenChange={setOpen}
      title={t('common:commandPalette.placeholder')}
      description={t('common:commandPalette.navigation')}
    >
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
                if (!route) return null
                return (
                  <CommandItem
                    key={`${hit.kind}:${hit.id}`}
                    value={`search ${hit.kind} ${hit.id} ${hit.name}`}
                    keywords={[query]}
                    onSelect={() => go(route)}
                  >
                    <Icon />
                    <span className="min-w-0 flex-1 truncate">{hit.name}</span>
                    <span className="ml-2 shrink-0 text-xs text-muted-foreground">
                      {featureId ? t(`nav:items.${featureId}`) : hit.kind}
                      {hit.detail ? ` · ${hit.detail}` : ''}
                    </span>
                  </CommandItem>
                )
              })}
            </CommandGroup>
            {searchQ.data?.truncated ? (
              <p className="px-3 pb-1 text-xs text-muted-foreground">
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
          <p className="px-3 pb-2 pt-1 text-xs text-destructive">
            {t('common:commandPalette.searchDegraded')}
          </p>
        ) : null}

        <CommandGroup heading={t('common:commandPalette.navigation')}>
          {views.map((v) => {
            const desc = describe(v.id)
            return (
              <CommandItem
                key={v.id}
                value={`${t(`nav:items.${v.id}`)} ${v.id}`}
                keywords={desc ? [desc] : undefined}
                onSelect={() => go(v.path)}
              >
                <v.icon />
                <span className="flex min-w-0 flex-col">
                  <span className="truncate">{t(`nav:items.${v.id}`)}</span>
                  {desc ? (
                    <span className="truncate text-xs text-muted-foreground">
                      {desc}
                    </span>
                  ) : null}
                </span>
              </CommandItem>
            )
          })}
          <CommandItem
            value={t('nav:items.settings')}
            onSelect={() => go('/settings')}
          >
            <Settings />
            {t('nav:items.settings')}
          </CommandItem>
        </CommandGroup>

        <CommandSeparator />

        <CommandGroup heading={t('common:commandPalette.actions')}>
          {views
            .filter((v) => v.commandActions?.length)
            .flatMap((v) =>
              (v.commandActions ?? []).map((action) => {
                const label = t(`nav:commandActions.${v.id}.${action}`, {
                  defaultValue: '',
                })
                if (!label) return null
                return (
                  <CommandItem
                    key={`${v.id}:${action}`}
                    value={`${label} ${v.id} ${action}`}
                    onSelect={() => {
                      useCommandStore.getState().setPendingAction(v.id, action)
                      go(v.path)
                    }}
                  >
                    <v.icon />
                    {label}
                  </CommandItem>
                )
              }),
            )}
          <CommandItem
            value={`${t('common:theme.label')} ${t('common:theme.light')}`}
            onSelect={() => {
              setTheme('light')
              setOpen(false)
            }}
          >
            <Sun />
            {t('common:theme.light')}
          </CommandItem>
          <CommandItem
            value={`${t('common:theme.label')} ${t('common:theme.dark')}`}
            onSelect={() => {
              setTheme('dark')
              setOpen(false)
            }}
          >
            <Moon />
            {t('common:theme.dark')}
          </CommandItem>
          <CommandItem
            value={t('auth:account.signOut')}
            onSelect={() => {
              setOpen(false)
              void logout()
            }}
          >
            <LogOut />
            {t('auth:account.signOut')}
          </CommandItem>
        </CommandGroup>
      </CommandList>
    </CommandDialog>
  )
}
