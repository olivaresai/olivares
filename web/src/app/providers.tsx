// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClientProvider } from '@tanstack/react-query'
import { useEffect, useState, type ReactNode } from 'react'
import { TooltipProvider } from '@/components/ui/tooltip'
import { Toaster } from '@/components/ui/toaster'
import { StepUpHost } from '@/components/layout/step-up-host'
import { apiFetch, configureApiClient } from '@/lib/api/client'
import { createQueryClient } from '@/lib/api/query'
import { isolateCacheOnTenantChange } from '@/lib/api/tenant-cache-isolation'
import { AuthProvider } from '@/lib/auth/context'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import { useWorkspaceStore } from '@/stores/workspace'

// Wire the thin HTTP client to the stores ONCE at module load (outside React) so its
// getters always read the latest token/tenant, and an authenticated 401 clears the
// session — a route guard then redirects to /login (see AppLayout). Reading via
// getState() keeps the client decoupled from React and trivially testable.
configureApiClient({
  getToken: () => useSessionStore.getState().token,
  getTenant: () => useTenantStore.getState().activeTenant,
  onUnauthorized: () => {
    useSessionStore.getState().clear()
    useWorkspaceStore.getState().clear()
  },
  // ⛔ AN EXPIRED SESSION IS NOT A REVOKED ONE, AND UNTIL NOW THE CONSOLE TREATED THEM THE
  //    SAME: any authenticated 401 cleared the session and sent the operator to /login,
  //    losing whatever they had half-filled. `POST /v1/auth/refresh` has existed in the
  //    engine all along (core/api/handlers_auth.go:269) and nothing here called it.
  //
  //    The call is AUTHENTICATED — it rotates the CALLING session's credential — and the
  //    client refuses to refresh in response to this path's own 401, so there is no
  //    recursion. A non-200 (an API-token principal gets 400, a dead session 401) resolves
  //    false and the old clear-and-redirect happens exactly as before.
  getExpiresAt: () => useSessionStore.getState().expiresAt,
  refreshSession: async () => {
    try {
      const s = await apiFetch<{
        token: string
        session_id: string
        expires_at: string
      }>('/v1/auth/refresh', { method: 'POST' })
      if (!s?.token) return false
      useSessionStore.getState().setSession({
        token: s.token,
        sessionId: s.session_id,
        expiresAt: s.expires_at,
      })
      return true
    } catch {
      return false
    }
  },
})

// Reset workspace selection when the tenant changes — a workspace id is
// meaningless across tenants.
let prevTenant = useTenantStore.getState().activeTenant
useTenantStore.subscribe((s) => {
  if (s.activeTenant !== prevTenant) {
    prevTenant = s.activeTenant
    useWorkspaceStore.getState().clear()
  }
})

/** App-wide providers: server-state (TanStack Query), identity (AuthProvider),
 * tooltips, the toast surface and the step-up ceremony host. i18n is initialized
 * as a side-effect import in main.tsx before this renders.
 *
 * StepUpHost sits INSIDE AuthProvider on purpose: the ceremony reads the session
 * assurance and re-reads whoami after the backend elevates it. */
export function Providers({ children }: { children: ReactNode }) {
  const [queryClient] = useState(() => createQueryClient())
  // ⛔ LA CLAVE SE CALCULA AL PINTAR Y LA CABECERA SE LEE AL ENVIAR. Entre esos dos instantes cabe
  //    un cambio de inquilino, y la respuesta del nuevo se guardaria bajo la clave del viejo —
  //    donde se enseñaria al volver, sin repedirla, porque `staleTime` son 30 s. La suscripcion de
  //    arriba no puede hacerlo: vive fuera de React y no alcanza a este cliente.
  useEffect(() => isolateCacheOnTenantChange(queryClient), [queryClient])
  return (
    <QueryClientProvider client={queryClient}>
      <AuthProvider>
        <TooltipProvider delayDuration={300}>
          {children}
          <Toaster />
          <StepUpHost />
        </TooltipProvider>
      </AuthProvider>
    </QueryClientProvider>
  )
}
