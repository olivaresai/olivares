// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  createRootRoute,
  createRoute,
  Outlet,
  redirect,
} from '@tanstack/react-router'
import { lazy, Suspense } from 'react'
import { AppLayout } from '@/components/layout/app-layout'
import { RequirePermission } from '@/components/layout/require-permission'
import { Spinner } from '@/components/ui/spinner'
import { FEATURE_VIEWS, ROUTE_ALIASES, type AreaId } from '@/features/registry'
import { AcceptInvitePage } from './pages/accept-invite'
import { LoginPage } from './pages/login'
import { NotFoundPage } from './pages/not-found'
import { RouteErrorPage } from './pages/route-error'
import { SettingsPage } from './pages/settings'
import { SetupPage } from './pages/setup'

// N1 — the nine area directories (features/navigation/area-directory.tsx). One code-split
// chunk for all nine: they are one component with a different `areaId`.
const AreaDirectoryView = lazy(() =>
  import('@/features/navigation/area-directory').then((m) => ({
    default: m.AreaDirectoryView,
  })),
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
/** Calm centered spinner while a code-split view's chunk loads (registry.tsx has its twin). */
function ViewLoading() {
  return (
    <div className="flex min-h-[40vh] items-center justify-center">
      <Spinner />
    </div>
  )
}

export const rootRoute = createRootRoute({
  component: () => <Outlet />,
  notFoundComponent: NotFoundPage,
  errorComponent: RouteErrorPage,
})

const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/login',
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
})

const settingsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/settings',
  component: SettingsPage,
})

/**
 * N1 — the nine area directory routes. They are written out as LITERAL createRoute calls
 * rather than mapped from NAV_AREAS on purpose: the census (route-census.json), the guide
 * generator (scripts/guide-docs/console-dump.mjs) and the capture-coverage guard all read
 * route paths as literals, and a path computed from a table is invisible to every one of
 * them. features/navigation/routes.test.ts pins that the mounted `/areas/*` set equals
 * NAV_AREAS in both directions, so the two lists cannot drift: drop a route here and it
 * goes red, add an area there without a route here and it goes red.
 *
 * They sit under the authenticated shell like every feature route, so AppLayout's auth
 * guard and TenantGate apply unchanged. No RequirePermission: an area is the union of its
 * leaves, and the directory itself shows only what `can()` allows.
 */
function areaDirectory(areaId: AreaId) {
  return () => (
    <Suspense fallback={<ViewLoading />}>
      <AreaDirectoryView areaId={areaId} />
    </Suspense>
  )
}
const areaInfrastructureRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/areas/infrastructure',
  component: areaDirectory('infrastructure'),
})
const areaAiRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/areas/ai',
  component: areaDirectory('ai'),
})
const areaDataContextRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/areas/data-context',
  component: areaDirectory('data-context'),
})
const areaWorkCommunicationsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/areas/work-communications',
  component: areaDirectory('work-communications'),
})
const areaAutomationRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/areas/automation',
  component: areaDirectory('automation'),
})
const areaSecurityIdentityRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/areas/security-identity',
  component: areaDirectory('security-identity'),
})
const areaDeploymentRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/areas/deployment',
  component: areaDirectory('deployment'),
})
const areaObservationRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/areas/observation',
  component: areaDirectory('observation'),
})
const areaSystemRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/areas/system',
  component: areaDirectory('system'),
})
const areaRoutes = [
  areaInfrastructureRoute,
  areaAiRoute,
  areaDataContextRoute,
  areaWorkCommunicationsRoute,
  areaAutomationRoute,
  areaSecurityIdentityRoute,
  areaDeploymentRoute,
  areaObservationRoute,
  areaSystemRoute,
]

const featureRoutes = FEATURE_VIEWS.map((view) =>
  createRoute({
    getParentRoute: () => appRoute,
    path: view.path,
    // The WHOLE view, not just its permission string: a view whose authority is a
    // registered capability question needs its declaration, and one whose url names a
    // single entity needs the location. Both are read inside the gate, so this map stays
    // the one-line wiring it has always been.
    component: () => (
      <RequirePermission view={view}>{view.element()}</RequirePermission>
    ),
  }),
)

/**
 * — retired paths keep resolving. Each ROUTE_ALIASES entry mounts a real route that
 * redirects instead of falling through to NotFoundPage, so an operator's bookmark and a
 * runbook's deep link survive a view moving. `replace` keeps the dead url out of history:
 * Back should return where the operator came from, not bounce through the redirect again.
 *
 * Empty today — Moved no path — and registry.route-conservation.test.ts pins that
 * every alias target resolves, so this can never mount a redirect into a 404.
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
      throw redirect({ to: a.to, search: true, hash: true, replace: true })
    },
  }),
)

export const routeTree = rootRoute.addChildren([
  loginRoute,
  setupRoute,
  acceptInviteRoute,
  statusPageRoute,
  appRoute.addChildren([
    settingsRoute,
    ...areaRoutes,
    ...featureRoutes,
    ...aliasRoutes,
  ]),
])
