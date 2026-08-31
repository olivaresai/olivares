// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Navigate, Outlet } from '@tanstack/react-router'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Spinner } from '@/components/ui/spinner'
import { useAuth } from '@/lib/auth/context'
import { BrandMark } from './brand'
import { CommandMenu } from './command-menu'
import { GlobalShortcuts } from './shortcuts'
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
 * here). Authenticated, it renders sidebar + topbar + the routed content, plus the
 * always-mounted ⌘K palette.
 */
export function AppLayout() {
  const { status } = useAuth()
  const { t } = useTranslation('common')
  const [mobileNavOpen, setMobileNavOpen] = useState(false)

  if (status === 'loading') return <Splash />
  if (status === 'anonymous' || status === 'error')
    return <Navigate to="/login" />

  return (
    <div className="flex h-svh overflow-hidden bg-background print:h-auto print:overflow-visible">
      {/* WCAG 2.4.1 Bypass Blocks: a skip link lets keyboard users jump past the
          sidebar nav straight to the routed content on every page. */}
      <a
        href="#main-content"
        className="sr-only z-50 rounded-md bg-accent px-3 py-2 text-sm font-medium text-accent-foreground outline-none focus-visible:not-sr-only focus-visible:absolute focus-visible:left-2 focus-visible:top-2 focus-visible:ring-2 focus-visible:ring-ring"
      >
        {t('a11y.skipToContent')}
      </a>
      <Sidebar />
      <MobileNav open={mobileNavOpen} onOpenChange={setMobileNavOpen} />
      <div className="flex min-w-0 flex-1 flex-col">
        <Topbar onMenuClick={() => setMobileNavOpen(true)} />
        <main
          id="main-content"
          tabIndex={-1}
          className="flex-1 overflow-y-auto outline-none print:overflow-visible"
        >
          <div className="mx-auto w-full max-w-[1440px] px-4 py-5 sm:px-6 print:max-w-none print:px-0 print:py-0">
            {/* No active tenant ⇒ the routed view is never mounted, so it cannot
                fire the tenant-scoped reads the engine would answer with 400
                "tenant required". See TenantGate. */}
            <TenantGate>
              <Outlet />
            </TenantGate>
          </div>
        </main>
      </div>
      <CommandMenu />
      <GlobalShortcuts />
    </div>
  )
}
