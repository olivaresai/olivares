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
import { FEATURE_VIEWS, ROUTE_ALIASES } from '@/features/registry'
import { AcceptInvitePage } from './pages/accept-invite'
import { LoginPage } from './pages/login'
import { NotFoundPage } from './pages/not-found'
import { RouteErrorPage } from './pages/route-error'
import { SettingsPage } from './pages/settings'
import { SetupPage } from './pages/setup'

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

const featureRoutes = FEATURE_VIEWS.map((view) =>
  createRoute({
    getParentRoute: () => appRoute,
    path: view.path,
    component: () => (
      <RequirePermission permission={view.permission}>
        {view.element()}
      </RequirePermission>
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
  appRoute.addChildren([settingsRoute, ...featureRoutes, ...aliasRoutes]),
])
