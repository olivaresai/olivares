// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { NewSessionHost } from '@/features/first-hour/new-session-host'
import { SettingsVisit } from '@/features/navigation/permitted-visit'
import { PersonalNavigationProvider } from '@/features/navigation/personal-navigation'
import { engineFavorites } from '@/features/saved-views/api'
import {
  Outlet,
  useNavigate,
  useRouter,
  useRouterState,
} from '@tanstack/react-router'
import { useEffect, useRef, useState } from 'react'
import { PageActionsProvider } from '@/components/ui/page-actions'
import { Spinner } from '@/components/ui/spinner'
import { useAuth } from '@/lib/auth/context'
import { isSignInPath } from '@/lib/auth/return-path'
import { useServerInfo } from '@/lib/hooks/use-server-info'
import { useSyncModulesNotEnabled } from '@/stores/modules'
import { usePreferencesStore } from '@/stores/preferences'
import { AppFrame } from './app-frame'
import { AppSidebar } from './app-sidebar'
import { BrandMark } from './brand'
import { CommandMenu } from './command-menu'
import { DestinationSections } from './destination-sections'
import { GlobalShortcuts } from './shortcuts'
import { RouteFrame } from './page-frames'
import { PhoneBar } from './phone-bar'
import { SidePanelProvider } from './side-panel'
import { AreasSheet } from './sidebar'
import { TenantGate } from './tenant-gate'
import { Topbar } from './topbar'

function Splash() {
  return (
    <div className="flex min-h-svh items-center justify-center bg-background">
      <div className="flex flex-col items-center gap-3">
        <BrandMark className="size-7 text-foreground" />
        <Spinner />
      </div>
    </div>
  )
}

/**
 * AppLayout is the authenticated shell and the auth GUARD for everything under it:
 * while the principal loads it shows a splash; if there is no session it redirects
 * to /login (the api client's onUnauthorized clears an expired session, which lands
 * here). Authenticated, it renders the frame (app-frame.tsx): the sidebar, the sheet with
 * its top bar and the routed work, the phone bar below 761 px, plus the always-mounted
 * ⌘K palette and the "All areas" sheet.
 *
 * THE SHELL CONTRACT (redesign §3.2):
 *
 *   sidebar 272 px │ top bar 52 px: breadcrumb · page state · page actions · panels
 *                  ├──────────────────────────────────────────────────────────────
 *                  │ work — the whole remaining sheet [+ the side panel a page declares]
 *
 * ⛔ THE SHELL HAS NO ACTION BAR, AND REMOVING IT IS THE POINT OF THIS LANE.
 *    Until 2026-09-18 a `ShellLauncher` sat below `main` on every authenticated route:
 *    a name field, two pickers, a verb and a scope line, about 90 px tall. Its
 *    requirement was right — *starting work is never more than one gesture away* — and
 *    its UNIT was wrong. The console has 77 routes and about 60 of them cannot start a
 *    session; the bar spent 90 px of all 77 viewports to serve 17.
 *
 *    A correction to that reading while we are here, with the file:line, because a
 *    mechanism nobody re-checks gets repeated: the bar was recorded as
 *    *"fixed to the bottom of the viewport"* and occluding content. It was not fixed — it was a
 *    `shrink-0` sibling AFTER a `flex-1 overflow-y-auto` main, so its bounds never
 *    overlapped anything. The sliced rows in those captures are a SCROLL BOUNDARY,
 *    which is what the bottom edge of a scroll container looks like. The defect was
 *    real and its remedy is the same; the defect was the 90 px, not an overlay.
 *
 * ⇒ WHERE THE REQUIREMENT WENT, because it was not dropped:
 *    · the ⌘K palette carries `Start a session` — always mounted, zero pixels;
 *    · the two screens where starting work IS the work (`/`, `/sessions`) mount the
 *      same component, renamed `WorkComposer`, INSIDE their work region, next to the
 *      list it will add to. That is what the composer in the reference does, and it is
 *      something a bar docked to the viewport cannot be.
 *
 * ⛔ AND `main` CARRIES NO PADDING AND NO WIDTH ANY MORE. It used to wrap every route
 *    in `mx-auto max-w-page px-4 py-5`, which silently made every route a DOCUMENT: a
 *    route that wanted to divide the viewport into panes had to fight a frame it did
 *    not ask for (the work surface's `xl:h-[60dvh]` was exactly that fight). The two
 *    frames are now declared, and a route picks one: `ConsolePage` (document) or
 *    `WorkPane` (work). See components/layout/page-frames.tsx.
 */
/** The ONE guard for a signed-out browser: it decides once per signed-out state
 * where to go, and navigates once. First boot goes straight to the setup wizard;
 * otherwise sign-in, carrying the page that was asked for (never a sign-in page,
 * which would bring the person back to sign-in after signing in). Returns the
 * destination while one is pending, so the shell paints nothing else. */
function useSignedOutRoute(status: string) {
  const navigate = useNavigate()
  const router = useRouter()
  const signedOut = status === 'anonymous' || status === 'error'
  const serverInfo = useServerInfo()
  const decided = signedOut && !serverInfo.isPending
  const target = !decided
    ? null
    : serverInfo.data?.setup_required
      ? '/setup'
      : '/login'
  useEffect(() => {
    if (target === '/setup') navigate({ to: '/setup', replace: true })
    else if (target === '/login') {
      // The page the person asked for, read ONCE, when the decision is made (this effect
      // runs once per signed-out state). The committed location, or before the router
      // commits its first one, the live one: with the HttpOnly cookie restore the
      // signed-out answer can come first, and RC10 sent a deep link to /login with no
      // returnTo (SC on 6e97de81). Read here and not at render: the guard's own
      // redirect moves the live location, and re-reading it on render redirected again
      // with a longer returnTo, forever (refresh 02, HU 016). A sign-in page is never
      // returned to.
      const { resolvedLocation, location } = router.state
      const requested = resolvedLocation?.href ?? location.href
      const returnTo =
        requested && !isSignInPath(requested) ? requested : undefined
      navigate({
        to: '/login',
        search: returnTo ? { returnTo } : {},
        replace: true,
      })
    }
  }, [target, navigate, router])
  return signedOut ? (target ?? 'pending') : null
}

export function AppLayout() {
  const { status } = useAuth()
  // Which engine modules run here (server-info modules_not_enabled, ARCH C1): read once
  // for the navigation gate, the route gate and the Home tiles.
  useSyncModulesNotEnabled()
  // The frame is a function of the ROUTE, resolved here and not declared by each view:
  // how the viewport is divided is the shell's decision (page-frames.tsx).
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  // Where a signed-out browser goes, and the page it returns to (read in the guard's
  // effect, once).
  const signInRoute = useSignedOutRoute(status)
  const sidebarHidden = usePreferencesStore((s) => s.sidebarCollapsed)
  const [areasOpen, setAreasOpen] = useState(false)
  const areasReturnRef = useRef<HTMLElement | null>(null)
  const openAreas = () => {
    areasReturnRef.current =
      document.activeElement instanceof HTMLElement
        ? document.activeElement
        : null
    setAreasOpen(true)
  }

  if (status === 'loading' || signInRoute) return <Splash />

  return (
    <PersonalNavigationProvider engineFavorites={engineFavorites}>
      <SidePanelProvider>
        <AppFrame
          sidebarHidden={sidebarHidden}
          sidebar={<AppSidebar areasOpen={areasOpen} onOpenAreas={openAreas} />}
          topbar={<Topbar />}
          phoneBar={<PhoneBar onMore={openAreas} />}
          overlays={
            <>
              <AreasSheet
                open={areasOpen}
                onOpenChange={setAreasOpen}
                returnFocusTo={areasReturnRef}
              />
              <CommandMenu />
              <GlobalShortcuts />
              <NewSessionHost />
            </>
          }
        >
          {/* No active tenant ⇒ the routed view is never mounted, so it cannot
              fire the tenant-scoped reads the engine would answer with 400
              "tenant required". See TenantGate. */}
          {/* One host per page for the verb a TABBED screen declares from
              inside its active tab. It wraps the routed content, so the header and
              the tab that fills its primary-action slot share it. */}
          <PageActionsProvider>
            <RouteFrame pathname={pathname}>
              <TenantGate>
                <DestinationSections />
                <Outlet />
                <SettingsVisit />
              </TenantGate>
            </RouteFrame>
          </PageActionsProvider>
        </AppFrame>
      </SidePanelProvider>
    </PersonalNavigationProvider>
  )
}
