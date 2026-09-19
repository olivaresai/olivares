// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { SettingsVisit } from '@/features/navigation/permitted-visit'
import { PersonalNavigationProvider } from '@/features/navigation/personal-navigation'
import { Navigate, Outlet, useRouterState } from '@tanstack/react-router'
import { useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { PageActionsProvider } from '@/components/ui/page-actions'
import { Spinner } from '@/components/ui/spinner'
import { useAuth } from '@/lib/auth/context'
import { BrandMark } from './brand'
import { CommandMenu } from './command-menu'
import { GlobalShortcuts } from './shortcuts'
import { RouteFrame } from './page-frames'
import { MobileNav, Sidebar } from './sidebar'
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
 * here). Authenticated, it renders rail + header + the routed content, plus the
 * always-mounted ⌘K palette.
 *
 * THE SHELL CONTRACT:
 *
 *   rail 16rem │ header 48px: breadcrumb · palette · account
 *              ├──────────────────────────────────────────────
 *              │ work — the whole remaining viewport
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
export function AppLayout() {
  const { status } = useAuth()
  const { t } = useTranslation('common')
  // The frame is a function of the ROUTE, resolved here and not declared by each view:
  // how the viewport is divided is the shell's decision (page-frames.tsx).
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const [mobileNavOpen, setMobileNavOpen] = useState(false)
  const menuButtonRef = useRef<HTMLButtonElement>(null)

  if (status === 'loading') return <Splash />
  if (status === 'anonymous' || status === 'error')
    return <Navigate to="/login" />

  return (
    <PersonalNavigationProvider>
      <div className="flex h-svh overflow-hidden bg-background print:h-auto print:overflow-visible">
        {/* WCAG 2.4.1 Bypass Blocks: a skip link lets keyboard users jump past the
          sidebar nav straight to the routed content on every page. */}
        <a
          href="#main-content"
          onClick={() => {
            document.getElementById('main-content')?.focus()
          }}
          className="sr-only z-50 rounded-md bg-accent px-3 py-2 text-body font-medium text-accent-foreground outline-none focus-visible:not-sr-only focus-visible:absolute focus-visible:left-2 focus-visible:top-2 focus-visible:ring-2 focus-visible:ring-ring"
        >
          {t('a11y.skipToContent')}
        </a>
        <Sidebar />
        <MobileNav
          open={mobileNavOpen}
          onOpenChange={setMobileNavOpen}
          returnFocusTo={menuButtonRef}
        />
        <div className="flex min-w-0 flex-1 flex-col">
          <Topbar
            onMenuClick={() => setMobileNavOpen(true)}
            menuButtonRef={menuButtonRef}
          />
          {/* THE WORK REGION. `min-h-0` is load-bearing and not tidiness: without it a
              flex child refuses to shrink below its content, so a route that wants to
              scroll INSIDE itself would instead grow the shell and scroll the page.
              `flex flex-col` so the frame the route picks can be `flex-1`.
              No padding and no max-width here — see the note above. */}
          <main
            id="main-content"
            tabIndex={-1}
            className="flex min-h-0 flex-1 flex-col overflow-hidden outline-none print:overflow-visible"
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
                  <Outlet />
                  <SettingsVisit />
                </TenantGate>
              </RouteFrame>
            </PageActionsProvider>
          </main>
        </div>
        <CommandMenu />
        <GlobalShortcuts />
      </div>
    </PersonalNavigationProvider>
  )
}
