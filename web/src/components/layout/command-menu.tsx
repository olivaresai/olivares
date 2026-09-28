// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { LogOut, Moon, Play, Search, Sun } from 'lucide-react'
import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import {
  CommandDialog,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from '@/components/ui/command'
import { Kbd } from '@/components/ui/kbd'
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
  viewById,
  type NavSearchEntry,
} from '@/features/navigation/model'
import { KEYBINDINGS, NAV_LEADER, NAV_SEQUENCES } from '@/lib/keybindings/table'
import { useCommandStore } from '@/stores/command'
import { useThemeStore } from '@/stores/theme'
import { useWorkspaceStore } from '@/stores/workspace'
import { useSessionRail } from './use-session-rail'

/** The characters the operator typed, without a leading ">" command limit. */
function matchQueryOf(query: string): { commandOnly: boolean; text: string } {
  const raw = query.trim()
  const commandOnly = raw.startsWith('>')
  return {
    commandOnly,
    text: (commandOnly ? raw.slice(1) : raw).trim(),
  }
}

/** Mark the first occurrence of the query. The mark is accent text, not a highlight fill. */
function markMatch(label: string, query: string): ReactNode {
  const needle = query.trim()
  if (!needle) return label
  const at = label.toLocaleLowerCase().indexOf(needle.toLocaleLowerCase())
  if (at < 0) return label
  return (
    <>
      {label.slice(0, at)}
      <mark className="bg-transparent font-semibold text-accent-text">
        {label.slice(at, at + needle.length)}
      </mark>
      {label.slice(at + needle.length)}
    </>
  )
}

function KeyHint({ keys }: { keys: readonly string[] }) {
  if (keys.length === 0) return null
  return (
    <span className="ml-auto flex shrink-0 items-center gap-1">
      {keys.map((key) => (
        <Kbd key={key}>{key}</Kbd>
      ))}
    </span>
  )
}

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
      className="top-[88px] w-[min(660px,calc(100%-2rem))] max-w-none sm:top-[88px] sm:max-w-[660px]"
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
  const { activeTenant, can, logout } = useAuth()
  const { navigable } = useViewAccess()
  // The verbs' own authority, which is NOT the view's (see features/navigation/command-actions).
  const { authorized: mayRun, capture } = useCommandActionAuthority()
  const setTheme = useThemeStore((s) => s.setTheme)
  const workspaceName = useWorkspaceStore((s) => s.activeWorkspaceName)
  const rail = useSessionRail()
  const [query, setQuery] = useState('')
  const [debounced, setDebounced] = useState('')
  const { commandOnly, text: matchQuery } = matchQueryOf(query)

  useEffect(() => {
    const id = window.setTimeout(() => setDebounced(query), SEARCH_DEBOUNCE_MS)
    return () => window.clearTimeout(id)
  }, [query])

  const term = matchQueryOf(debounced).commandOnly
    ? ''
    : matchQueryOf(debounced).text
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
    () => rankNavMatches(authorized, matchQuery),
    [authorized, matchQuery],
  )
  // Areas and modules stay in ONE ranked list. Splitting them would put a weak area
  // hit above an exact module hit. Settings is its own group, after Go to.
  const goToHits = commandOnly
    ? []
    : ranked.filter((e) => e.kind === 'area' || e.kind === 'view')
  const settingsHits = commandOnly
    ? []
    : ranked.filter((e) => e.kind === 'settings')

  const needle = fold(matchQuery)
  const shows = (label: string) => !needle || fold(label).includes(needle)
  const recentSessions = commandOnly
    ? []
    : rail.groups
        .flatMap((group) => group.rows)
        .filter((row) => row.kind === 'session')
        .filter((row) => shows(row.title ?? row.reference))
        .slice(0, 8)

  const searchHits = searchQ.data?.results ?? []

  /**
   * ONE ROW, AND THE NAME IS WHAT GETS THE WIDTH.
   *
   * ⛔ THE CONTEXT USED TO BE A `shrink-0` SIBLING OF THE NAME, and that one class was
   *    the whole defect. `shrink-0` means "take whatever you need"; the name was the
   *    only flexible element left, so the name was the only thing that could give way.
   *    The result measured at 390 px: five of eight rows cut
   *    the module name to 8–11 characters (`Communi…`, `New cha…`, `Channel a…`) while
   *    `Work & communications › Communications` held 60 % of the row. The palette is
   *    the console's fastest path, and at a phone width it could not be read.
   *
   * ⇒ The context moves to the SECOND LINE, beside the description. Line one is the
   *   name and nothing else, so it truncates LAST — it is the only element there and
   *   it has the whole row. The place an operator typed against is never the place
   *   that gives way.
   *
   * The context is first on line two and the description second, because the context
   * disambiguates (two doors into one screen) while the description elaborates; when
   * the line has to be cut, the disambiguation is what must survive.
   */
  const goKeys = (e: NavSearchEntry): string[] => {
    if (e.kind !== 'view') return []
    const seq = NAV_SEQUENCES.find((s) => s.featureId === e.id)
    if (!seq) return []
    return [NAV_LEADER.toUpperCase(), seq.key.toUpperCase()]
  }
  const renderNav = (e: NavSearchEntry) => {
    const Icon = e.icon
    const context = e.kind === 'area' ? t('nav:directory.area') : e.context
    return (
      <CommandItem
        key={`${e.kind}:${e.id}`}
        value={`${e.kind}:${e.id} ${e.label}`}
        onSelect={() => go(e.path)}
      >
        <Icon />
        <span className="flex min-w-0 flex-1 flex-col">
          <span className="truncate" data-slot="palette-name">
            {markMatch(e.label, matchQuery)}
          </span>
          {context || e.description ? (
            <span
              // `leading-4`: the caption's own 18 px line box put the two-line row
              // at 44 px exactly, which is the budget with nothing left over. 16 px
              // leaves 2 px, and a 12 px glyph does not need 18.
              className="truncate text-caption leading-4 text-muted-foreground"
              data-slot="palette-context"
            >
              {[context, e.description].filter(Boolean).join(' · ')}
            </span>
          ) : null}
        </span>
        <KeyHint keys={goKeys(e)} />
      </CommandItem>
    )
  }

  // A verb stays visible when this principal can open its page but cannot run it.
  // Hiding it made the refusal a blank list. The page gate stays: a verb for a page
  // this principal cannot open has nowhere to land. The write is its own check.
  const refusal = (permission: string) => {
    if (!activeTenant) {
      return {
        reason: t('common:commandPalette.noTargetReason'),
        help: t('common:commandPalette.noTargetHelp'),
      }
    }
    if (!can(permission)) {
      return {
        reason: t('common:commandPalette.deniedReason', { permission }),
        help: t('common:commandPalette.deniedHelp'),
      }
    }
    return null
  }
  const actionItems = views
    .filter((v) => v.commandActions?.length)
    .flatMap((v) =>
      (v.commandActions ?? []).map((action) => {
        const label = t(`nav:commandActions.${v.id}.${action.id}`, {
          defaultValue: '',
        })
        if (!label || !shows(label)) return null
        const blocked = refusal(action.permission)
        return (
          <CommandItem
            key={`${v.id}:${action.id}`}
            value={`action:${v.id}:${action.id} ${label}`}
            disabled={blocked !== null}
            onSelect={() => {
              // Re-asked at selection. A grant or a tenant that moved between the
              // render and this keystroke must not dispatch, and the palette stays
              // open so the list can correct itself.
              const context = capture()
              if (!mayRun(action) || !dispatchable(context)) return
              useCommandStore
                .getState()
                .setPendingAction(v.id, action.id, context)
              go(v.path)
            }}
          >
            <v.icon />
            <span className="flex min-w-0 flex-1 flex-col">
              <span className="truncate" data-slot="palette-name">
                {markMatch(label, matchQuery)}
              </span>
              {blocked ? (
                <span
                  className="truncate text-caption leading-4 text-muted-foreground"
                  data-slot="palette-context"
                >
                  {blocked.reason} {blocked.help}
                </span>
              ) : null}
            </span>
          </CommandItem>
        )
      }),
    )
    .filter((x) => x !== null)
  /**
   * STARTING WORK, FROM ANYWHERE, FOR ZERO PIXELS.
   *
   * The shell used to carry a 90 px composer on all 77 authenticated routes so that
   * starting a session was one gesture away. The palette is already always mounted and
   * already ⌘K, so it satisfies the same requirement and costs nothing; the composer
   * itself now lives in the work pane of the two screens where starting work IS the
   * work, which is where it can stand beside the list the run will join.
   *
   * ⛔ IT IS GATED BY THE VERB'S OWN PERMISSION, not by a page's. `sessions:run:write`
   *    is what `WorkComposer` itself checks before rendering anything, and it is what
   *    the engine enforces — a palette row that led to a 403 would be the dead end the
   *    front door stopped offering.
   *
   * It NAVIGATES and does not launch: the run needs a provider profile the server
   * cannot default, so a one-keystroke launch would either be impossible or would
   * have to invent the one field that must be chosen.
   */
  const startSessionLabel = t('common:commandPalette.startSession')
  const showStartSession = shows(startSessionLabel)
  const startBlocked = refusal('sessions:run:write')
  const sessionsPath = viewById('sessions')?.path
  const startKeys = (
    KEYBINDINGS.find((rule) => rule.command === 'session.new')?.keys ?? ''
  )
    .split('+')
    .filter(Boolean)
    .map((part) => (part.toLowerCase() === 'mod' ? '⌘' : part.toUpperCase()))

  const lightLabel = t('common:theme.light')
  const darkLabel = t('common:theme.dark')
  const signOutLabel = t('auth:account.signOut')
  const themeLabel = t('common:theme.label')
  const showLight = shows(`${themeLabel} ${lightLabel}`)
  const showDark = shows(`${themeLabel} ${darkLabel}`)
  const showSignOut = shows(signOutLabel)
  const anyAction =
    actionItems.length > 0 ||
    showStartSession ||
    showLight ||
    showDark ||
    showSignOut
  const visibleSearch = commandOnly
    ? []
    : searchHits.filter((hit) => {
        const featureId = SEARCH_KIND_FEATURE[hit.kind]
        const route = SEARCH_KIND_ROUTES[hit.kind]
        const view = views.find((v) => v.id === featureId)
        return !!route && !(featureId && !view)
      })
  const anyListed =
    visibleSearch.length > 0 ||
    anyAction ||
    goToHits.length > 0 ||
    recentSessions.length > 0 ||
    settingsHits.length > 0

  return (
    <>
      <div className="relative">
        <CommandInput
          placeholder={t('common:commandPalette.placeholder')}
          value={query}
          onValueChange={setQuery}
          className={workspaceName ? 'pr-40' : 'pr-14'}
        />
        <span className="pointer-events-none absolute inset-y-0 right-3 flex items-center gap-2">
          {workspaceName ? (
            <span className="rounded-md bg-accent-soft px-1.5 py-0.5 text-caption text-accent-text">
              {t('common:commandPalette.scope', { name: workspaceName })}
            </span>
          ) : null}
          <Kbd>Esc</Kbd>
        </span>
      </div>
      <CommandList label={t('common:commandPalette.results')}>
        {anyListed ? null : (
          <CommandItem disabled value="empty:none">
            {t('common:commandPalette.empty')}
          </CommandItem>
        )}

        {anyAction ? (
          <CommandGroup heading={t('common:commandPalette.actions')}>
            {showStartSession ? (
              <CommandItem
                value={`session:start ${startSessionLabel}`}
                data-testid="palette-start-session"
                disabled={startBlocked !== null || !sessionsPath}
                onSelect={() => {
                  if (startBlocked || !sessionsPath) return
                  go(sessionsPath)
                }}
              >
                <Play />
                <span className="flex min-w-0 flex-1 flex-col">
                  <span className="truncate" data-slot="palette-name">
                    {markMatch(startSessionLabel, matchQuery)}
                  </span>
                  <span
                    className="truncate text-caption leading-4 text-muted-foreground"
                    data-slot="palette-context"
                  >
                    {startBlocked
                      ? `${startBlocked.reason} ${startBlocked.help}`
                      : t('nav:items.sessions')}
                  </span>
                </span>
                <KeyHint keys={startKeys} />
              </CommandItem>
            ) : null}
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
                <span className="truncate" data-slot="palette-name">
                  {markMatch(lightLabel, matchQuery)}
                </span>
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
                <span className="truncate" data-slot="palette-name">
                  {markMatch(darkLabel, matchQuery)}
                </span>
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
                <span className="truncate" data-slot="palette-name">
                  {markMatch(signOutLabel, matchQuery)}
                </span>
              </CommandItem>
            ) : null}
          </CommandGroup>
        ) : null}

        {goToHits.length > 0 ? (
          <CommandGroup heading={t('common:commandPalette.goTo')}>
            {goToHits.map(renderNav)}
          </CommandGroup>
        ) : null}

        {recentSessions.length > 0 ? (
          <CommandGroup heading={t('common:commandPalette.recentSessions')}>
            {recentSessions.map((row) => {
              const title = row.title ?? row.reference
              const state =
                row.state === 'need'
                  ? t('common:ui.status.needsYou')
                  : row.state === 'live'
                    ? t('common:ui.status.working')
                    : row.state === 'ended'
                      ? t('common:ui.status.done')
                      : t('common:ui.status.idle')
              return (
                <CommandItem
                  key={row.key}
                  value={`recent-session:${row.key} ${title}`}
                  onSelect={() => {
                    setOpen(false)
                    void navigate({
                      to: row.to as never,
                      search: row.search as never,
                    })
                  }}
                >
                  <span className="flex min-w-0 flex-1 flex-col">
                    <span className="truncate" data-slot="palette-name">
                      {markMatch(title, matchQuery)}
                    </span>
                    <span
                      className="truncate text-caption leading-4 text-muted-foreground"
                      data-slot="palette-context"
                    >
                      {[row.meta, state].filter(Boolean).join(' · ')}
                    </span>
                  </span>
                </CommandItem>
              )
            })}
          </CommandGroup>
        ) : null}

        {settingsHits.length > 0 ? (
          <CommandGroup heading={t('common:commandPalette.settingsGroup')}>
            {settingsHits.map(renderNav)}
          </CommandGroup>
        ) : null}

        {visibleSearch.length > 0 ? (
          <CommandGroup heading={t('common:commandPalette.searchResults')}>
            {visibleSearch.map((hit) => {
              const featureId = SEARCH_KIND_FEATURE[hit.kind]
              const route = SEARCH_KIND_ROUTES[hit.kind]
              const view = views.find((v) => v.id === featureId)
              const Icon = view?.icon ?? Search
              if (!route) return null
              return (
                <CommandItem
                  key={`${hit.kind}:${hit.id}`}
                  value={`search ${hit.kind} ${hit.id} ${hit.name}`}
                  keywords={[matchQuery]}
                  onSelect={() => go(route)}
                >
                  <Icon />
                  <span className="flex min-w-0 flex-1 flex-col">
                    <span className="truncate" data-slot="palette-name">
                      {markMatch(hit.name, matchQuery)}
                    </span>
                    <span
                      className="truncate text-caption leading-4 text-muted-foreground"
                      data-slot="palette-context"
                    >
                      {featureId ? t(`nav:items.${featureId}`) : hit.kind}
                      {hit.detail ? ` · ${hit.detail}` : ''}
                    </span>
                  </span>
                </CommandItem>
              )
            })}
          </CommandGroup>
        ) : null}
      </CommandList>
      {searchQ.data?.truncated ? (
        <p className="px-3 py-1 text-caption text-muted-foreground">
          {t('common:commandPalette.searchTruncated')}
        </p>
      ) : null}
      {searchQ.data?.degraded ? (
        <p className="px-3 py-1 text-caption text-destructive">
          {t('common:commandPalette.searchDegraded')}
        </p>
      ) : null}
      <div
        data-slot="palette-footer"
        className="flex flex-wrap items-center gap-x-3 gap-y-1 border-t border-border px-3 py-2 text-caption text-muted-foreground"
      >
        <span className="inline-flex items-center gap-1">
          <Kbd>↑</Kbd>
          <Kbd>↓</Kbd>
          {t('common:commandPalette.footerMove')}
        </span>
        <span className="inline-flex items-center gap-1">
          <Kbd>↵</Kbd>
          {t('common:commandPalette.footerOpen')}
        </span>
        <span className="ml-auto inline-flex items-center gap-1">
          <Kbd>&gt;</Kbd>
          {t('common:commandPalette.footerCommands')}
        </span>
      </div>
    </>
  )
}
