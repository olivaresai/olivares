// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  createRootRoute,
  createRoute,
  Outlet,
  redirect,
  lazyRouteComponent,
  type ErrorComponentProps,
} from '@tanstack/react-router'
import { lazy, Suspense } from 'react'
import { useDocumentTitle } from '@/components/layout/document-title'
import { RequirePermission } from '@/components/layout/require-permission'
import { Spinner } from '@/components/ui/spinner'
import { FEATURE_VIEWS, ROUTE_ALIASES, TAB_ALIASES } from '@/features/registry'
import { ANONYMOUS_VIEWS } from '@/features/anonymous-registry'
import { MOVED_SECTIONS } from '@/features/settings/sections'
import { AcceptPairingPage } from './pages/accept-pairing'
import { AcceptInvitePage } from './pages/accept-invite'
import { LoginPage } from './pages/login'
import { NotFoundPage } from './pages/not-found'
import { RouteErrorPage } from './pages/route-error'
import { SetupPage } from './pages/setup'

// Settings embeds the console's license panel, and that graph (with its locale
// bundles) must not load on first paint: code-split it like the feature views.
const SettingsPage = lazy(() =>
  import('./pages/settings').then((m) => ({ default: m.SettingsPage })),
)

const AppLayout = lazyRouteComponent(
  () => import('@/components/layout/app-layout'),
  'AppLayout',
)

const StatusPage = lazy(() =>
  import('@/features/health/status-page').then((m) => ({
    default: m.StatusPage,
  })),
)

/**
 * The route tree is GENERATED from the feature registry: the public auth routes,
 * then the authenticated shell (AppLayout, a pathless layout route that guards) with
 * one child route per FEATURE_VIEW. Each feature route wraps its element in
 * RequirePermission so a deep-link is RBAC-checked, not just hidden in the nav.–
 * only edit the registry — never this file or the shell.
 */
/** Calm centered spinner while a code-split view's chunk loads (features/lazy-view.tsx has its twin). */
function ViewLoading() {
  return (
    <div className="flex min-h-[40vh] items-center justify-center">
      <Spinner />
    </div>
  )
}

function ShellError({ error }: ErrorComponentProps) {
  // A failed browser import stays cached for this document. Use the error
  // page's reload recovery so Retry fetches the shell in a fresh module map.
  return <RouteErrorPage error={error} />
}

/** The root of every route, public or shell: it names the page in the document title. */
function RootLayout() {
  useDocumentTitle()
  return <Outlet />
}

export const rootRoute = createRootRoute({
  component: RootLayout,
  notFoundComponent: NotFoundPage,
  errorComponent: RouteErrorPage,
})

const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/login',
  validateSearch: (search: Record<string, unknown>): { returnTo?: string } =>
    typeof search.returnTo === 'string' ? { returnTo: search.returnTo } : {},
  component: LoginPage,
})
const setupRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/setup',
  component: SetupPage,
})
//public invite-acceptance leg — the accept_url the engine emails
// (core/api/handlers_onboarding.go) lands here; the invitee has no session.
const acceptInviteRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/accept-invite',
  component: AcceptInvitePage,
})

const acceptPairingRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/pair',
  component: AcceptPairingPage,
})

//public status page — no authentication required.
const statusPageRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/status-page',
  component: () => (
    <Suspense
      fallback={
        <div className="flex min-h-screen items-center justify-center">
          <Spinner />
        </div>
      }
    >
      <StatusPage />
    </Suspense>
  ),
})

// Pathless layout route: renders the authenticated shell and guards its children.
const appRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: 'app',
  component: AppLayout,
  pendingComponent: ViewLoading,
  errorComponent: ShellError,
})

const settingsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/settings',
  // A section another place now holds (MOVED_SECTIONS) lands there, with the rest of the
  // query and the hash, replacing the old entry as an alias does.
  beforeLoad: ({ search }) => {
    const { section, ...rest } = search as Record<string, unknown>
    const to =
      typeof section === 'string' && Object.hasOwn(MOVED_SECTIONS, section)
        ? MOVED_SECTIONS[section]
        : undefined
    if (to) throw redirect({ to, search: rest, hash: true, replace: true })
  },
  component: () => (
    <Suspense fallback={<ViewLoading />}>
      <SettingsPage />
    </Suspense>
  ),
})

/**
 * Where an address lands when it moved: the new path, the query it carried with the alias's
 * own added (a retired `tab` dropped), and the alias's anchor or the old one. `replace`, so
 * Back returns where the person came from.
 */
function landing(
  to: string,
  extra: Readonly<Record<string, string>> | undefined,
  hash: string | undefined,
  drop?: string,
) {
  return redirect({
    to: to as never,
    search: ((prev: Record<string, unknown>) => {
      const kept = { ...prev }
      if (drop) delete kept[drop]
      return { ...kept, ...extra }
    }) as never,
    hash: hash ?? true,
    replace: true,
  })
}

const featureRoutes = FEATURE_VIEWS.map((view) =>
  createRoute({
    getParentRoute: () => appRoute,
    path: view.path,
    // A retired tab of this page lands on the place that holds it now (TAB_ALIASES).
    beforeLoad: ({ search }) => {
      const asked = (search as Record<string, unknown>).tab
      const moved = TAB_ALIASES.find(
        (a) => a.path === view.path && a.tab === asked,
      )
      if (moved) throw landing(moved.to, moved.search, moved.hash, 'tab')
    },
    // The WHOLE view, not just its permission string: a view whose authority is a
    // registered capability question needs its declaration, and one whose url names a
    // single entity needs the location. Both are read inside the gate, so this map stays
    // the one-line wiring it has always been.
    component: () => (
      <RequirePermission view={view}>{view.element()}</RequirePermission>
    ),
  }),
)

const anonymousFeatureRoutes = ANONYMOUS_VIEWS.map((view) =>
  createRoute({
    getParentRoute: () => rootRoute,
    path: view.path,
    component: () => view.element(),
  }),
)

/**
 * — retired paths keep resolving. Each ROUTE_ALIASES entry mounts a real route that
 * redirects instead of falling through to NotFoundPage, so an operator's bookmark and a
 * runbook's deep link survive a view moving. `replace` keeps the dead url out of history:
 * Back should return where the operator came from, not bounce through the redirect again.
 *
 * The retired area directories (`/areas/<id>`) are the aliases today, and
 * registry.route-conservation.test.ts pins that every alias target resolves, so this can
 * never mount a redirect into a 404.
 */
const aliasRoutes = ROUTE_ALIASES.map((a) =>
  createRoute({
    getParentRoute: () => appRoute,
    path: a.from,
    beforeLoad: () => {
      // `search: true` / `hash: true` CARRY THE STATE ACROSS. Without them the redirect
      // keeps the path and drops everything after it, and this repo treats search params
      // as canonical shareable state (lib/hooks/use-url-state.ts) — so a bookmarked
      // /audit?from=…&to=… would land on a bare /audit and quietly show a different
      // result set. Preserving the path while losing the query is not conservation.
      throw a.search || a.hash
        ? landing(a.to, a.search, a.hash)
        : redirect({ to: a.to, search: true, hash: true, replace: true })
    },
  }),
)

export const routeTree = rootRoute.addChildren([
  loginRoute,
  setupRoute,
  acceptInviteRoute,
  acceptPairingRoute,
  statusPageRoute,
  ...anonymousFeatureRoutes,
  appRoute.addChildren([settingsRoute, ...featureRoutes, ...aliasRoutes]),
])
